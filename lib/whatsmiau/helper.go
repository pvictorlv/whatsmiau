package whatsmiau

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/verbeux-ai/whatsmiau/env"
	"github.com/verbeux-ai/whatsmiau/lib/proxypool"
	"github.com/verbeux-ai/whatsmiau/models"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

func b64(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func u64(n uint64) string {
	return strconv.FormatUint(n, 10)
}

func i64(n int64) string {
	return strconv.FormatInt(n, 10)
}

// streamHTTP is the client for transfers whose duration is proportional to the
// size of the body — media downloads and webhook deliveries carrying inline
// media. It is deliberately not httpClient: that one carries a short total
// timeout, and a total timeout on a large transfer measures the file rather than
// the health of the peer, which is exactly what killed big media halfway
// through. The deadline here comes from the caller's context instead.
func (s *Whatsmiau) streamHTTP() *http.Client {
	if s.streamClient != nil {
		return s.streamClient
	}
	return s.httpClient
}

// mediaCtx caps a media transfer end to end. Without it the media client, which
// has no total timeout, would hang forever on a source that stalls mid-body.
func mediaCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := env.Env.MediaTransferTimeout
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *Whatsmiau) getCtx(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	res, err := s.streamHTTP().Do(req)
	if err != nil {
		return nil, err
	}

	// An error page is still a body: without this check a 404 from the storage
	// bucket used to be uploaded to WhatsApp as if it were the file.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("failed to fetch media: unexpected status %d", res.StatusCode)
	}

	return res, nil
}

// fetchBytes downloads url contents into memory and guarantees the response body is closed.
// Prefer fetchToTempFile for anything that can be large: this one is only safe
// for media that is already bounded (audio notes, profile pictures).
func (s *Whatsmiau) fetchBytes(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := mediaCtx(ctx)
	defer cancel()

	res, err := s.getCtx(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

// fetchToTempFile downloads url to disk instead of to memory, rewound and ready
// to read. An 80MB PDF held as []byte becomes roughly four times that during the
// upload encryption, which is what made large media fail while small media
// worked. The caller owns the file and must call closeAndRemove on it.
func (s *Whatsmiau) fetchToTempFile(ctx context.Context, url string) (*os.File, error) {
	ctx, cancel := mediaCtx(ctx)
	defer cancel()

	res, err := s.getCtx(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	tmp, err := os.CreateTemp("", "media-*")
	if err != nil {
		return nil, err
	}

	if _, err := io.Copy(tmp, res.Body); err != nil {
		closeAndRemove(tmp)
		return nil, fmt.Errorf("failed to download media: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		closeAndRemove(tmp)
		return nil, err
	}

	return tmp, nil
}

func closeAndRemove(file *os.File) {
	if file == nil {
		return
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		zap.L().Debug("failed to close temp file", zap.String("file", name), zap.Error(err))
	}
	if err := os.Remove(name); err != nil {
		zap.L().Warn("failed to remove temp file", zap.String("file", name), zap.Error(err))
	}
}

// uploadFile encrypts and uploads a file without ever holding it in memory.
// whatsmeow.Upload takes a []byte and makes three full copies of it while
// encrypting; UploadReader streams through a temporary file instead, so RAM
// stays flat no matter how big the attachment is.
func uploadFile(ctx context.Context, client *whatsmeow.Client, file *os.File, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	ctx, cancel := mediaCtx(ctx)
	defer cancel()

	return client.UploadReader(ctx, file, nil, mediaType)
}

// canInlineBase64 decides whether a downloaded attachment can ride inside the
// webhook payload. Base64 inflates the file by a third, so an 80MB document
// becomes a ~107MB JSON body: past a point the delivery is slower than the
// on-demand fetch it replaces and it trips the consumer's own body limit.
//
// A size of -1 means "could not measure", and an unmeasurable file is not worth
// the gamble. A limit of 0 or less disables the cut entirely.
func canInlineBase64(size int64) bool {
	if size < 0 {
		return false
	}
	limit := env.Env.WebhookBase64MaxBytes
	if limit <= 0 {
		return true
	}
	return size <= limit
}

// sniffMimetype derives the mimetype from the file header and rewinds the file,
// for the callers that did not receive one in the request.
func sniffMimetype(file *os.File, fileName string) string {
	sample := make([]byte, 512)
	n, err := file.Read(sample)
	if err != nil && err != io.EOF {
		zap.L().Warn("failed to read media header", zap.Error(err))
		return ""
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		zap.L().Error("failed to rewind media file", zap.Error(err))
		return ""
	}

	mimetype, err := extractMimetype(sample[:n], fileName)
	if err != nil {
		return ""
	}
	return mimetype
}

// loadClientWithJID validates the instance client, ensures jid is non-nil, and returns
// the client together with the resolved JID. Centralises the boilerplate used by every
// Send* method.
func (s *Whatsmiau) loadClientWithJID(ctx context.Context, instanceID string, jid *types.JID) (*whatsmeow.Client, types.JID, error) {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return nil, types.EmptyJID, whatsmeow.ErrClientIsNil
	}
	if jid == nil {
		return nil, types.EmptyJID, fmt.Errorf("remote_jid is required")
	}
	resolved := s.resolveJID(ctx, client, *jid)
	return client, resolved, nil
}

// waveformMaxAmplitude is the highest value a waveform byte may hold. WhatsApp reads
// each byte as a percentage of the bar height, so anything above 100 is out of spec and
// makes the client drop the waveform and fall back to a plain seek bar.
const waveformMaxAmplitude = 100.0

// audioSampleRate is the rate we decode to and encode at. WhatsApp voice notes are
// mono 48kHz Opus; anything else risks the receiver refusing to play the note.
const audioSampleRate = 48000

// Returns audioConverted, waveform, duration and an error
func convertAudio(data []byte, bars int) ([]byte, []byte, float64, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, nil, 0, errors.New("ffmpeg not found in path (install to decode .ogg opus/vorbis)")
	}

	tempIn, err := os.CreateTemp("", "audio-*.ogg")
	if err != nil {
		return nil, nil, 0, err
	}
	defer os.Remove(tempIn.Name())
	if _, err := io.Copy(tempIn, bytes.NewReader(data)); err != nil {
		return nil, nil, 0, err
	}
	if err := tempIn.Close(); err != nil {
		return nil, nil, 0, err
	}

	out, err := exec.Command(
		"ffmpeg",
		"-i", tempIn.Name(),
		"-vn",
		"-ac", "1",
		"-ar", strconv.Itoa(audioSampleRate),
		"-f", "s16le",
		"-hide_banner",
		"-loglevel", "error",
		"pipe:1",
	).Output()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed running ffmpeg: %w", err)
	}
	if len(out) < 2 {
		return nil, nil, 0, errors.New("no audio data after decoding")
	}

	// Also convert to Ogg/Opus for stable playback/sharing. Mono at 48kHz is what the
	// WhatsApp recorder produces; a stereo or resampled note plays back as "something is
	// wrong with the audio file" on Android.
	oggOut, err := exec.Command(
		"ffmpeg",
		"-i", tempIn.Name(),
		"-vn",
		"-map_metadata", "-1",
		"-c:a", "libopus",
		"-b:a", "64k",
		"-ac", "1",
		"-ar", strconv.Itoa(audioSampleRate),
		"-avoid_negative_ts", "make_zero",
		"-f", "ogg",
		"-hide_banner",
		"-loglevel", "error",
		"pipe:1",
	).Output()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed converting to ogg opus: %w", err)
	}
	if len(oggOut) == 0 {
		return nil, nil, 0, errors.New("no data after opus conversion")
	}

	n := len(out) / 2
	durationSec := float64(n) / float64(audioSampleRate)

	samples := make([]int16, n)
	for i := 0; i < n; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(out[2*i : 2*i+2]))
	}

	return oggOut, buildWaveform(samples, bars), durationSec, nil
}

// buildWaveform turns mono PCM into the byte-per-bar waveform WhatsApp renders under a
// voice note. Values are normalised against the 98th percentile so a single spike does
// not flatten the whole preview, then clamped into the 0..100 range the client expects.
func buildWaveform(samples []int16, bars int) []byte {
	values := rmsByBars(samples, bars)

	scale := percentile(values, 0.98)
	if scale <= 0 {
		for _, v := range values {
			if v > scale {
				scale = v
			}
		}
	}
	if scale <= 0 {
		return make([]byte, len(values))
	}

	buf := make([]byte, len(values))
	for i, v := range values {
		x := (v / scale) * waveformMaxAmplitude
		if x < 0 {
			x = 0
		}
		if x > waveformMaxAmplitude {
			x = waveformMaxAmplitude
		}
		buf[i] = byte(math.Round(x))
	}

	return buf
}

// audioSeconds rounds a decoded duration to the value carried in AudioMessage.Seconds.
// A voice note reported as 0s is treated as malformed by the client, so sub-second audio
// is reported as 1s.
func audioSeconds(duration float64) uint32 {
	if duration <= 1 {
		return 1
	}
	return uint32(math.Round(duration))
}

func rmsByBars(samples []int16, bars int) []float64 {
	if bars < 1 {
		bars = 1
	}
	total := len(samples)
	if total == 0 {
		return make([]float64, bars)
	}
	seg := total / bars
	if seg == 0 {
		seg = 1
	}

	values := make([]float64, 0, bars)
	for i := 0; i < bars; i++ {
		start := i * seg
		end := start + seg
		if i == bars-1 || end > total {
			end = total
		}
		if start >= end {
			values = append(values, 0)
			continue
		}

		var sumSq float64
		for _, s := range samples[start:end] {
			f := float64(s) / 32768.0
			sumSq += f * f
		}
		rms := math.Sqrt(sumSq / float64(end-start))
		values = append(values, rms)
	}
	return values
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	if p <= 0 {
		return cp[0]
	}
	if p >= 1 {
		return cp[len(cp)-1]
	}
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func extractMimetype(decodedData []byte, fileName string) (string, error) {
	ext := filepath.Ext(fileName)
	if ext != "" {
		mimeType := mime.TypeByExtension(ext)
		if mimeType != "" {
			return mimeType, nil
		}
	}

	var dataSample []byte
	if len(decodedData) > 512 {
		dataSample = decodedData[:512]
	} else {
		dataSample = decodedData
	}
	detected := http.DetectContentType(dataSample)
	return detected, nil
}

func extractFromBase64(b64Data string) (string, string, []byte, error) {
	var mimeType string
	rawData := b64Data

	if idx := strings.Index(b64Data, ","); idx != -1 {
		header := b64Data[:idx]
		rawData = b64Data[idx+1:]

		if strings.HasPrefix(header, "data:") {
			mimeType = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(rawData)
	if err != nil {
		return "", "", nil, err
	}

	if mimeType == "" {
		limit := 512
		if len(decoded) < 512 {
			limit = len(decoded)
		}
		mimeType = http.DetectContentType(decoded[:limit])
	}

	var extension string
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		extension = strings.TrimPrefix(exts[0], ".")
	}

	return mimeType, extension, decoded, nil
}

func extractExtFromFile(fileName, mimeType string, file *os.File) string {
	ext := filepath.Ext(fileName)
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			if len(exts) > 1 {
				ext = exts[1]
			} else {
				ext = exts[0]
			}
		} else {
			buf := make([]byte, 512)
			n, err := file.Read(buf)
			if err != nil && err != io.EOF {
				zap.L().Error("failed to read file", zap.Error(err))
			}
			detected := http.DetectContentType(buf[:n])
			if exts, _ := mime.ExtensionsByType(detected); len(exts) > 0 {
				ext = exts[0]
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				zap.L().Error("failed to seek image", zap.Error(err))
			}
		}
	}

	return strings.TrimPrefix(ext, ".")
}

func canIgnoreMessage(msg *events.Message) bool {
	return strings.Contains(msg.Info.Chat.String(), "status")
}

// canIgnoreGroup returns true if group can be ignored
func canIgnoreGroup(evt interface{}, instance *models.Instance) bool {
	if instance.GroupsIgnore == nil || !*instance.GroupsIgnore {
		return false
	}

	var jid string
	switch evt.(type) {
	case *events.Message:
		msg, ok := evt.(*events.Message)
		if !ok {
			return false
		}

		jid = msg.Info.Chat.String()
	case *events.UndecryptableMessage:
		msg, ok := evt.(*events.UndecryptableMessage)
		if !ok {
			return false
		}

		jid = msg.Info.Chat.String()
	case *events.GroupInfo:
		gInfo, ok := evt.(*events.GroupInfo)
		if !ok {
			return false
		}

		jid = gInfo.JID.String()
	case *events.Receipt:
		rcp, ok := evt.(*events.Receipt)
		if !ok {
			return false
		}

		jid = rcp.Chat.String()
	case *events.Contact:
		ctc, ok := evt.(*events.Contact)
		if !ok {
			return false
		}

		jid = ctc.JID.String()
	case *events.Picture:
		pic, ok := evt.(*events.Picture)
		if !ok {
			return false
		}

		jid = pic.JID.String()
	case *events.PushName:
		pushName, ok := evt.(*events.PushName)
		if !ok {
			return false
		}

		jid = pushName.JID.String()
	}

	return strings.HasSuffix(jid, "@g.us")
}

// configProxy points the client at the proxy it must use before connecting:
// the instance's own proxy when it has one, otherwise one taken from the
// global pool (PROXY_POOL_FILE).
//
// It returns an error when a pool is configured but has no proxy to hand out.
// Callers must then skip Connect: going direct would expose the server IP to
// WhatsApp, which is the exact thing the pool exists to prevent.
func configProxy(client *whatsmeow.Client, instanceID string, instanceProxy models.InstanceProxy) error {
	proxy := instanceProxy
	source := "instance"

	if len(proxy.ProxyHost) <= 0 {
		if !proxypool.Configured() {
			return nil
		}

		picked, err := proxypool.Acquire(instanceID)
		if err != nil {
			return fmt.Errorf("no proxy available for instance %s: %w", instanceID, err)
		}

		proxy = picked
		source = "pool"
	}

	opts := whatsmeow.SetProxyOptions{
		NoMedia: env.Env.ProxyNoMedia,
	}

	if err := client.SetProxyAddress(proxypool.URL(proxy), opts); err != nil {
		return fmt.Errorf("failed to set proxy %s:%s on instance %s: %w",
			proxy.ProxyHost, proxy.ProxyPort, instanceID, err)
	}

	zap.L().Info("proxy configured",
		zap.String("instance", instanceID),
		zap.String("source", source),
		zap.String("proxy", proxy.ProxyHost+":"+proxy.ProxyPort))

	return nil
}

func buildVCard(fullName, wuid, phone, organization, email, urlValue string) string {
	if wuid == "" {
		wuid = strings.TrimPrefix(phone, "+")
		wuid = strings.ReplaceAll(wuid, " ", "")
	}

	var b strings.Builder
	b.WriteString("BEGIN:VCARD\n")
	b.WriteString("VERSION:3.0\n")
	fmt.Fprintf(&b, "N:;%s;;;\n", fullName)
	fmt.Fprintf(&b, "FN:%s\n", fullName)
	if organization != "" {
		fmt.Fprintf(&b, "ORG:%s\n", organization)
	}
	fmt.Fprintf(&b, "TEL;type=CELL;type=VOICE;waid=%s:%s\n", wuid, phone)
	if email != "" {
		fmt.Fprintf(&b, "EMAIL;type=INTERNET:%s\n", email)
	}
	if urlValue != "" {
		fmt.Fprintf(&b, "URL:%s\n", urlValue)
	}
	b.WriteString("END:VCARD")
	return b.String()
}

// parseStatusBackgroundARGB converts a hex color string (#RRGGBB or #AARRGGBB) to an ARGB uint32.
// Returns 0 if the input is empty or malformed.
func parseStatusBackgroundARGB(s string) uint32 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	if s == "" {
		return 0
	}
	if len(s) == 6 {
		s = "FF" + s
	}
	if len(s) != 8 {
		return 0
	}
	var v uint32
	if _, err := fmt.Sscanf(s, "%08x", &v); err != nil {
		return 0
	}
	return v
}

// Adding or removing the 9th digit. Returns empty if not applicable.
func buildBrazilianAlternate(number string) string {
	if !strings.HasPrefix(number, "55") || len(number) < 12 || len(number) > 13 {
		return ""
	}

	ddd := number[2:4]
	local := number[4:]

	switch {
	case len(local) == 9 && local[0] == '9':
		return "55" + ddd + local[1:]
	case len(local) == 8:
		return "55" + ddd + "9" + local
	default:
		return ""
	}
}

// resolveHistoryJID turns a raw JID string from a history sync key into the
// (jid, lid) pair used everywhere else. Unparseable or empty input is returned
// untouched so a malformed key never drops the whole message.
func (s *Whatsmiau) resolveHistoryJID(ctx context.Context, id, rawJID string) (string, string) {
	if rawJID == "" {
		return "", ""
	}

	parsed, err := types.ParseJID(rawJID)
	if err != nil {
		zap.L().Warn("failed to parse history sync jid", zap.String("jid", rawJID), zap.Error(err))
		return rawJID, ""
	}

	return s.GetJidLid(ctx, id, parsed)
}

func (s *Whatsmiau) buildMessageDataFromHistory(ctx context.Context, id string, msg *waWeb.WebMessageInfo, convName, displayName string) *WookMessageData {
	if msg == nil || msg.Message == nil {
		return nil
	}

	key := msg.GetKey()
	if key == nil {
		return nil
	}

	messageType, raw, ci := s.parseWAMessage(msg.Message)
	if messageType == "" {
		return nil
	}

	// Since the LID migration, history sync chats can be addressed by LID. Left
	// unresolved they reach the consumer as a bare "<lid>@lid" and create a
	// phantom contact, so resolve them the same way the live path does.
	remoteJid, remoteLid := s.resolveHistoryJID(ctx, id, key.GetRemoteJID())
	participant, _ := s.resolveHistoryJID(ctx, id, key.GetParticipant())

	addressingMode := "lid"
	if remoteLid == "" {
		addressingMode = "jid"
	}

	wookKey := &WookKey{
		RemoteJid:      remoteJid,
		RemoteLid:      remoteLid,
		FromMe:         key.GetFromMe(),
		Id:             key.GetID(),
		Participant:    participant,
		AddressingMode: addressingMode,
	}

	status := statusFromHistory(msg.GetStatus(), key.GetFromMe())

	pushName := msg.GetPushName()
	if key.GetFromMe() {
		if pushName == "" {
			pushName = fromMePushName("pt-BR")
		}
	} else if pushName == "" {

		if idx := strings.Index(remoteJid, "@"); idx != -1 {
			pushName = remoteJid[:idx]
		}
		if pushName == "" {
			switch {
			case convName != "":
				pushName = convName
			case displayName != "":
				pushName = displayName
			}
		}
	}

	var contextInfo *WookMessageContextInfo
	if ci != nil {
		contextInfo = &WookMessageContextInfo{
			StanzaId:     ci.GetStanzaID(),
			Participant:  ci.GetParticipant(),
			Expiration:   int(ci.GetExpiration()),
			MentionedJid: ci.GetMentionedJID(),
		}
		if qm := ci.GetQuotedMessage(); qm != nil {
			_, qmRaw, _ := s.parseWAMessage(qm)
			contextInfo.QuotedMessage = qmRaw
		}
	}

	return &WookMessageData{
		Key:              wookKey,
		PushName:         pushName,
		Status:           status,
		Message:          raw,
		MessageType:      messageType,
		MessageTimestamp: int(msg.GetMessageTimestamp()),
		Source:           "unknown",
		ContextInfo:      contextInfo,
	}
}

func statusFromHistory(status waWeb.WebMessageInfo_Status, fromMe bool) string {
	if fromMe {
		return "SENT"
	}
	if status == waWeb.WebMessageInfo_ERROR {
		return "DELIVERY_ACK"
	}
	return waWeb.WebMessageInfo_Status_name[int32(status)]
}

// fromMePushNames mapeia locale → push name para mensagens enviadas pelo usuário.
// O padrão é pt-BR (base de usuários atual). Adicione novos idiomas estendendo o mapa.
var fromMePushNames = map[string]string{
	"pt-BR": "Você",
	"en":    "You",
}

func fromMePushName(locale string) string {
	if name, ok := fromMePushNames[locale]; ok {
		return name
	}
	return "You"
}
