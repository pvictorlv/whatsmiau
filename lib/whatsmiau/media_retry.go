package whatsmiau

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

// ErrMediaGone sinaliza que a mídia não está mais no CDN do WhatsApp nem pôde
// ser reenviada pelo aparelho. É o fim da linha: não adianta tentar de novo.
var ErrMediaGone = errors.New("media is no longer available on the CDN or on the phone")

// mediaRetryTimeout limita a espera pela resposta do aparelho. O celular precisa
// estar online para reenviar; se estiver desligado, não há o que esperar.
const mediaRetryTimeout = 25 * time.Second

// mediaRetryWaiters liga o pedido de reenvio (feito na thread do download) à
// notificação do aparelho, que chega assíncrona pelo event handler.
var mediaRetryWaiters sync.Map // string -> chan *events.MediaRetry

func mediaRetryKey(instanceID, messageID string) string {
	return instanceID + "|" + messageID
}

// MediaRetryTarget identifica a mensagem cuja mídia deve ser reenviada pelo
// aparelho. Sem ela o download só pode contar com o CDN.
type MediaRetryTarget struct {
	MessageID string
	Chat      types.JID
	Sender    types.JID
	FromMe    bool
}

// isMediaGone reconhece as falhas em que o CDN não tem mais o arquivo. Só nesses
// casos vale acordar o aparelho; erro de rede ou hash inválido pedem outra coisa.
func isMediaGone(err error) bool {
	return errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403)
}

// deliverMediaRetry entrega a resposta do aparelho a quem estiver esperando por
// ela. Sem waiter registrado a notificação é descartada — ninguém pediu.
func deliverMediaRetry(instanceID string, evt *events.MediaRetry) {
	value, ok := mediaRetryWaiters.Load(mediaRetryKey(instanceID, string(evt.MessageID)))
	if !ok {
		zap.L().Debug("media retry notification with no pending waiter",
			zap.String("instance", instanceID),
			zap.String("message_id", string(evt.MessageID)))
		return
	}

	select {
	case value.(chan *events.MediaRetry) <- evt:
	default:
	}
}

// requestMediaReupload pede ao aparelho que suba a mídia de novo e devolve o
// novo directPath. As demais chaves (mediaKey, hashes) continuam valendo: o
// aparelho reenvia o mesmo blob cifrado, só muda onde ele está hospedado.
func (s *Whatsmiau) requestMediaReupload(ctx context.Context, instanceID string, client *whatsmeow.Client, target *MediaRetryTarget, mediaKey []byte) (string, error) {
	if target == nil || target.MessageID == "" || len(mediaKey) == 0 {
		return "", ErrMediaGone
	}

	// O canal é registrado antes do receipt: a notificação pode voltar antes de
	// SendMediaRetryReceipt retornar, e sem waiter ela seria descartada.
	key := mediaRetryKey(instanceID, target.MessageID)
	waiter := make(chan *events.MediaRetry, 1)
	mediaRetryWaiters.Store(key, waiter)
	defer mediaRetryWaiters.Delete(key)

	info := &types.MessageInfo{
		ID: types.MessageID(target.MessageID),
		MessageSource: types.MessageSource{
			Chat:     target.Chat,
			Sender:   target.Sender,
			IsFromMe: target.FromMe,
			IsGroup:  target.Chat.Server == types.GroupServer,
		},
	}

	if err := client.SendMediaRetryReceipt(ctx, info, mediaKey); err != nil {
		return "", fmt.Errorf("failed to ask the phone to re-upload the media: %w", err)
	}

	zap.L().Info("asked the phone to re-upload expired media",
		zap.String("instance", instanceID),
		zap.String("message_id", target.MessageID))

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(mediaRetryTimeout):
		return "", fmt.Errorf("%w: the phone did not answer the re-upload request", ErrMediaGone)
	case evt := <-waiter:
		notification, err := whatsmeow.DecryptMediaRetryNotification(evt, mediaKey)
		if err != nil {
			if errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
				return "", ErrMediaGone
			}
			return "", fmt.Errorf("failed to read the re-upload notification: %w", err)
		}

		if notification.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS {
			return "", fmt.Errorf("%w: phone answered %s", ErrMediaGone, notification.GetResult().String())
		}

		directPath := notification.GetDirectPath()
		if directPath == "" {
			return "", fmt.Errorf("%w: phone answered without a direct path", ErrMediaGone)
		}

		return directPath, nil
	}
}

// downloadMediaWithRetry baixa a mídia pelo CDN e, quando o CDN já a descartou,
// pede o reenvio ao aparelho e baixa do novo endereço.
func (s *Whatsmiau) downloadMediaWithRetry(
	ctx context.Context,
	instanceID string,
	client *whatsmeow.Client,
	directPath string,
	encHash, fileHash, mediaKey []byte,
	mediaType whatsmeow.MediaType,
	mmsType string,
	allowNoHash bool,
	target *MediaRetryTarget,
) ([]byte, error) {
	data, err := client.DownloadMediaWithPath(ctx, directPath, encHash, fileHash, mediaKey, mediaType, mmsType, allowNoHash)
	if err == nil {
		return data, nil
	}
	if !isMediaGone(err) {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("%w: %v", ErrMediaGone, err)
	}

	newPath, retryErr := s.requestMediaReupload(ctx, instanceID, client, target, mediaKey)
	if retryErr != nil {
		zap.L().Warn("media re-upload request failed",
			zap.String("instance", instanceID),
			zap.String("message_id", target.MessageID),
			zap.Error(retryErr))
		return nil, retryErr
	}

	return client.DownloadMediaWithPath(ctx, newPath, encHash, fileHash, mediaKey, mediaType, mmsType, allowNoHash)
}

// downloadToFileWithRetry é o downloadMediaWithRetry em streaming: mantém o
// conteúdo fora da memória, o que importa num vídeo grande.
func (s *Whatsmiau) downloadToFileWithRetry(
	ctx context.Context,
	instanceID string,
	client *whatsmeow.Client,
	msg whatsmeow.DownloadableMessage,
	file whatsmeow.File,
	target *MediaRetryTarget,
) error {
	err := client.DownloadToFile(ctx, msg, file)
	if err == nil || !isMediaGone(err) {
		return err
	}
	if target == nil {
		return fmt.Errorf("%w: %v", ErrMediaGone, err)
	}

	newPath, retryErr := s.requestMediaReupload(ctx, instanceID, client, target, msg.GetMediaKey())
	if retryErr != nil {
		zap.L().Warn("media re-upload request failed",
			zap.String("instance", instanceID),
			zap.String("message_id", target.MessageID),
			zap.Error(retryErr))
		return retryErr
	}

	// A tentativa abortada pode ter deixado bytes no arquivo.
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("failed to reset the media file before retrying: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind the media file before retrying: %w", err)
	}

	return client.DownloadMediaWithPathToFile(
		ctx,
		newPath,
		msg.GetFileEncSHA256(),
		msg.GetFileSHA256(),
		msg.GetMediaKey(),
		whatsmeow.GetMediaType(msg),
		"",
		false,
		file,
	)
}
