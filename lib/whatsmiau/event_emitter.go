package whatsmiau

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
	"github.com/verbeux-ai/whatsmiau/env"
	"github.com/verbeux-ai/whatsmiau/models"
	"github.com/verbeux-ai/whatsmiau/services"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

type emitter struct {
	url     string
	data    any
	headers map[string]string
}

func (s *Whatsmiau) getInstance(id string) *models.Instance {
	ctx, c := context.WithTimeout(context.Background(), time.Second*5)
	defer c()

	res, err := s.repo.List(ctx, id)
	if err != nil {
		zap.L().Panic("failed to get instanceCached by instance", zap.Error(err))
	}

	if len(res) == 0 {
		zap.L().Warn("no instanceCached found by instance", zap.String("instance", id))
		return nil
	}

	return &res[0]
}

func (s *Whatsmiau) getInstanceCached(id string) *models.Instance {
	instanceCached, ok := s.instanceCache.Load(id)
	if ok {
		return &instanceCached
	}

	ctx, c := context.WithTimeout(context.Background(), time.Second*5)
	defer c()

	res, err := s.repo.List(ctx, id)
	if err != nil {
		zap.L().Panic("failed to get instanceCached by instance", zap.Error(err))
	}

	if len(res) == 0 {
		zap.L().Warn("no instanceCached found by instance", zap.String("instance", id))
		return nil
	}

	s.instanceCache.Store(id, res[0])
	go func() {
		// expires in 10sec
		time.Sleep(time.Second * 10)
		s.instanceCache.Delete(id)
	}()

	return &res[0]
}

func (s *Whatsmiau) startEmitter() {
	workers := env.Env.EmitterWorkers
	if workers <= 0 {
		workers = 50
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for event := range s.emitter {
				s.processEmit(event)
			}
		}()
	}
	wg.Wait()
}

func (s *Whatsmiau) processEmit(event emitter) {
	data, err := json.Marshal(event.data)
	if err != nil {
		zap.L().Error("failed to marshal event", zap.Error(err))
		return
	}

	const maxRetries = 2
	backoff := time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
		}

		success, shouldRetry := s.doEmit(data, event.url, event.headers)
		if success || !shouldRetry {
			return
		}

		if attempt < maxRetries {
			zap.L().Warn("webhook delivery failed, retrying",
				zap.String("url", event.url),
				zap.Int("attempt", attempt+1),
				zap.Int("maxRetries", maxRetries),
			)
		}
	}

	zap.L().Error("webhook delivery permanently failed after retries",
		zap.String("url", event.url),
	)
}

// doEmit performs a single webhook delivery attempt with a 10s timeout.
// Returns (success, shouldRetry).
func (s *Whatsmiau) doEmit(data []byte, url string, headers map[string]string) (bool, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		zap.L().Error("failed to create request", zap.Error(err))
		return false, false
	}

	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		zap.L().Error("failed to send webhook", zap.Error(err), zap.String("url", url))
		return false, true // network error, retry
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, false
	}

	if resp.StatusCode >= 500 {
		res, _ := io.ReadAll(resp.Body)
		zap.L().Error("webhook returned server error",
			zap.Int("status", resp.StatusCode),
			zap.String("response", string(res)),
			zap.String("url", url),
		)
		return false, true // server error, retry
	}

	// 4xx: client error, don't retry
	res, _ := io.ReadAll(resp.Body)
	zap.L().Error("webhook returned client error",
		zap.Int("status", resp.StatusCode),
		zap.String("response", string(res)),
		zap.String("url", url),
	)
	return false, false
}

func (s *Whatsmiau) emit(body any, url string, headers map[string]string) {
	if url == "" {
		return
	}
	s.emitter <- emitter{url: url, data: body, headers: headers}
}

func (s *Whatsmiau) Handle(id string) whatsmeow.EventHandler {
	return func(evt any) {
		s.handlerSemaphore <- struct{}{}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					zap.L().Error("panic in event handler",
						zap.String("instance", id),
						zap.Any("panic", r),
						zap.Stack("stack"),
					)
				}
				<-s.handlerSemaphore
			}()
			instance := s.getInstanceCached(id)
			if instance == nil {
				zap.L().Warn("no instance found for event", zap.String("instance", id))
				return
			}

			// Handle lifecycle events regardless of webhook enabled state
			if _, ok := evt.(*events.LoggedOut); ok {
				s.handleLoggedOut(id)
				return
			}

			if instance.Webhook.Enabled != nil && !*instance.Webhook.Enabled {
				return
			}

			eventMap := make(map[string]bool)
			for _, event := range instance.Webhook.Events {
				eventMap[event] = true
			}

			switch e := evt.(type) {
			case *events.Message:
				s.handleMessageEvent(id, instance, e, eventMap)
			case *events.UndecryptableMessage:
				s.handleUndecryptableMessageEvent(id, instance, e, eventMap)
			case *events.Receipt:
				s.handleReceiptEvent(id, instance, e, eventMap)
			case *events.BusinessName:
				s.handleBusinessNameEvent(id, instance, e, eventMap)
			case *events.Contact:
				s.handleContactEvent(id, instance, e, eventMap)
			case *events.Picture:
				s.handlePictureEvent(id, instance, e, eventMap)
			case *events.HistorySync:
				s.handleHistorySyncEvent(id, instance, e, eventMap)
			case *events.GroupInfo:
				s.handleGroupInfoEvent(id, instance, e, eventMap)
				s.handleGroupParticipantsUpdateEvent(id, instance, e, eventMap)
			case *events.JoinedGroup:
				s.handleJoinedGroupEvent(id, instance, e, eventMap)
			case *events.PushName:
				s.handlePushNameEvent(id, instance, e, eventMap)
			case *events.Connected:
				s.handleConnectionUpdateEvent(id, instance, "open", 200, eventMap)
			case *events.Disconnected:
				s.handleConnectionUpdateEvent(id, instance, "close", 0, eventMap)
				s.stopHistorySyncWatchdog(id)
			case *events.ConnectFailure:
				s.handleConnectionUpdateEvent(id, instance, "close", int(e.Reason), eventMap)
				s.stopHistorySyncWatchdog(id)
			case *events.StreamReplaced:
				s.stopHistorySyncWatchdog(id)
			case *events.CallOffer:
				s.handleCallEvent(id, instance, e.CallCreator, e.CallID, e.Timestamp, "offer", eventMap)
			case *events.CallOfferNotice:
				s.handleCallEvent(id, instance, e.CallCreator, e.CallID, e.Timestamp, "offer", eventMap)
			case *events.CallTerminate:
				s.handleCallEvent(id, instance, e.CallCreator, e.CallID, e.Timestamp, "terminate", eventMap)
			case *events.KeepAliveTimeout:
				// The socket can stay CONNECTED while silently receiving nothing
				// (zombie socket). Surfacing this is the only early warning.
				zap.L().Error("keepalive timeout",
					zap.String("instance", id),
					zap.Int("error_count", e.ErrorCount),
					zap.Time("last_success", e.LastSuccess))
			case *events.KeepAliveRestored:
				zap.L().Warn("keepalive restored", zap.String("instance", id))
			case *events.TemporaryBan:
				zap.L().Error("instance temporarily banned",
					zap.String("instance", id),
					zap.String("code", e.Code.String()),
					zap.Duration("expire", e.Expire))
			case *events.OfflineSyncPreview:
				zap.L().Info("offline sync preview",
					zap.String("instance", id),
					zap.Int("messages", e.Messages),
					zap.Int("receipts", e.Receipts),
					zap.Int("notifications", e.Notifications))
			case *events.OfflineSyncCompleted:
				zap.L().Info("offline sync completed",
					zap.String("instance", id),
					zap.Int("count", e.Count))
			case *events.MediaRetryError:
				zap.L().Warn("media retry error", zap.String("instance", id), zap.Int("code", e.Code))
			default:
				zap.L().Debug("unknown event", zap.String("type", fmt.Sprintf("%T", evt)), zap.Any("raw", evt))
			}
		}()
	}
}

func (s *Whatsmiau) handleLoggedOut(id string) {
	s.stopHistorySyncWatchdog(id)

	client, ok := s.clients.Load(id)
	if ok {
		if err := s.deleteDeviceIfExists(context.Background(), client); err != nil {
			zap.L().Error("failed to delete device for instance", zap.String("instance", id), zap.Error(err))
			return
		}
	}

	s.clients.Delete(id)
}
func (s *Whatsmiau) handleMessageEvent(id string, instance *models.Instance, e *events.Message, eventMap map[string]bool) {
	if e.Message != nil {
		if pm := e.Message.GetProtocolMessage(); pm != nil {
			switch pm.GetType() {
			case waE2E.ProtocolMessage_REVOKE:
				s.handleMessageDeleteEvent(id, instance, e, eventMap)
				return
			case waE2E.ProtocolMessage_MESSAGE_EDIT:
				// Edits keep flowing through MESSAGES_UPSERT: whatsmeow already
				// unwrapped the EditedMessage envelope, so the protocol message
				// reaches the consumer verbatim via the protojson fallback and
				// carries the target key plus the new content.
			default:
				// Ephemeral settings, app state sync keys, peer data operations
				// and friends carry no user-visible content. Emitting them would
				// create empty messages on the consumer side.
				zap.L().Debug("skipping non-content protocol message",
					zap.String("instance", id),
					zap.String("type", pm.GetType().String()))
				return
			}
		}
	}

	if !eventMap["MESSAGES_UPSERT"] {
		return
	}

	if canIgnoreGroup(e, instance) {
		return
	}

	if canIgnoreMessage(e) {
		return
	}

	messageData := s.convertEventMessage(id, instance, e)
	if messageData == nil {
		zap.L().Error("failed to convert event", zap.String("id", id), zap.String("type", fmt.Sprintf("%T", e)), zap.Any("raw", e))
		return
	}

	messageData.InstanceId = instance.ID

	dateTime := time.Unix(int64(messageData.MessageTimestamp), 0)
	wookMessage := &WookEvent[WookMessageData]{
		Instance: instance.ID,
		Data:     messageData,
		DateTime: dateTime,
		Event:    WookMessagesUpsert,
	}

	if wookMessage.Data.Message != nil && len(wookMessage.Data.Message.Base64) > 0 {
		b64Temp := wookMessage.Data.Message.Base64
		wookMessage.Data.Message.Base64 = ""
		zap.L().Debug("message event", zap.String("instance", id), zap.Any("data", wookMessage.Data))
		wookMessage.Data.Message.Base64 = b64Temp
	} else if wookMessage.Data.Message != nil {
		zap.L().Debug("message event", zap.String("instance", id), zap.Any("data", wookMessage.Data))
	}

	s.emit(wookMessage, instance.Webhook.Url, instance.Webhook.Headers)
}

// handleUndecryptableMessageEvent surfaces a message that arrived but could not
// be decrypted. whatsmeow has already exhausted its retry receipts by this
// point; without this handler the message is lost with no trace at all, which
// is what made past "lost message" incidents impossible to diagnose.
func (s *Whatsmiau) handleUndecryptableMessageEvent(id string, instance *models.Instance, e *events.UndecryptableMessage, eventMap map[string]bool) {
	ctx, c := context.WithTimeout(context.Background(), time.Second*10)
	defer c()

	chatJid, chatLid := s.GetJidLid(ctx, id, e.Info.Chat)
	senderJid, _ := s.GetJidLid(ctx, id, e.Info.Sender)

	// Logged unconditionally: this is the signal that a message was lost, and it
	// must not depend on the instance subscribing to the webhook event.
	zap.L().Error("undecryptable message",
		zap.String("instance", id),
		zap.String("message_id", e.Info.ID),
		zap.String("chat", chatJid),
		zap.String("sender", senderJid),
		zap.Bool("is_unavailable", e.IsUnavailable),
		zap.String("unavailable_type", string(e.UnavailableType)),
		zap.String("decrypt_fail_mode", string(e.DecryptFailMode)),
		zap.Time("timestamp", e.Info.Timestamp))

	if !eventMap["MESSAGES_UNDECRYPTABLE"] {
		return
	}

	if canIgnoreGroup(e, instance) {
		return
	}

	addressingMode := "lid"
	if chatLid == "" {
		addressingMode = "jid"
	}

	data := &WookMessageUndecryptableData{
		Key: &WookKey{
			RemoteJid:      chatJid,
			RemoteLid:      chatLid,
			FromMe:         e.Info.IsFromMe,
			Id:             e.Info.ID,
			Participant:    senderJid,
			AddressingMode: addressingMode,
		},
		PushName:         strings.TrimSpace(e.Info.PushName),
		IsUnavailable:    e.IsUnavailable,
		UnavailableType:  string(e.UnavailableType),
		DecryptFailMode:  string(e.DecryptFailMode),
		IsGroup:          e.Info.IsGroup,
		MessageTimestamp: int(e.Info.Timestamp.Unix()),
		InstanceId:       instance.ID,
	}

	s.emit(&WookEvent[WookMessageUndecryptableData]{
		Instance: instance.ID,
		Data:     data,
		DateTime: time.Now(),
		Event:    WookMessagesUndecryptable,
	}, instance.Webhook.Url, instance.Webhook.Headers)
}

// handleCallEvent forwards incoming call activity, which was previously dropped
// entirely, so consumers can reject calls or log them against the contact.
func (s *Whatsmiau) handleCallEvent(id string, instance *models.Instance, creator types.JID, callID string, timestamp time.Time, status string, eventMap map[string]bool) {
	if !eventMap["CALL"] {
		return
	}

	ctx, c := context.WithTimeout(context.Background(), time.Second*10)
	defer c()

	fromJid, fromLid := s.GetJidLid(ctx, id, creator)

	data := &WookCallData{
		Id:         callID,
		From:       fromJid,
		FromLid:    fromLid,
		Status:     status,
		Timestamp:  int(timestamp.Unix()),
		InstanceId: instance.ID,
	}

	zap.L().Debug("call event", zap.String("instance", id), zap.Any("data", data))
	s.emit(&WookEvent[WookCallData]{
		Instance: instance.ID,
		Data:     data,
		DateTime: time.Now(),
		Event:    WookCallUpsert,
	}, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) handleMessageDeleteEvent(id string, instance *models.Instance, e *events.Message, eventMap map[string]bool) {
	if !eventMap["MESSAGES_DELETE"] {
		return
	}

	if canIgnoreGroup(e, instance) {
		return
	}

	if canIgnoreMessage(e) {
		return
	}

	pm := e.Message.GetProtocolMessage()
	pKey := pm.GetKey()
	if pKey == nil {
		return
	}

	ctx, c := context.WithTimeout(context.Background(), time.Second*5)
	defer c()

	remoteJid, _ := s.GetJidLid(ctx, id, e.Info.Chat)

	keyRemoteJid := pKey.GetRemoteJID()
	if keyRemoteJid == "" {
		keyRemoteJid = remoteJid
	}

	deleteData := &WookMessageDeleteData{
		Id:          pKey.GetID(),
		RemoteJid:   keyRemoteJid,
		FromMe:      pKey.GetFromMe(),
		Participant: pKey.GetParticipant(),
		Status:      "DELETED",
		InstanceId:  instance.ID,
	}

	wookEvent := &WookEvent[WookMessageDeleteData]{
		Instance: instance.ID,
		Data:     deleteData,
		DateTime: time.Now(),
		Event:    WookMessagesDelete,
	}

	zap.L().Debug("message delete event", zap.String("instance", id), zap.Any("data", deleteData))
	s.emit(wookEvent, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) handleReceiptEvent(id string, instance *models.Instance, e *events.Receipt, eventMap map[string]bool) {
	if !eventMap["MESSAGES_UPDATE"] {
		return
	}

	if canIgnoreGroup(e, instance) {
		return
	}

	data := s.convertEventReceipt(id, e)
	if data == nil {
		return
	}

	for _, event := range data {
		wookData := &WookEvent[WookMessageUpdateData]{
			Instance: instance.ID,
			Data:     &event,
			DateTime: e.Timestamp,
			Event:    WookMessagesUpdate,
		}

		s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
	}
}

func (s *Whatsmiau) handleBusinessNameEvent(id string, instance *models.Instance, e *events.BusinessName, eventMap map[string]bool) {
	if !eventMap["CONTACTS_UPSERT"] {
		return
	}

	data := s.convertBusinessName(id, e)
	if data == nil {
		zap.L().Error("failed to convert business name", zap.String("id", id), zap.String("type", fmt.Sprintf("%T", e)), zap.Any("raw", e))
		return
	}

	wookData := &WookEvent[WookContactUpsertData]{
		Instance: instance.ID,
		Data:     &WookContactUpsertData{*data},
		DateTime: time.Now(),
		Event:    WookContactsUpsert,
	}

	s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) handleContactEvent(id string, instance *models.Instance, e *events.Contact, eventMap map[string]bool) {
	if !eventMap["CONTACTS_UPSERT"] {
		return
	}

	if canIgnoreGroup(e, instance) {
		return
	}

	data := s.convertContact(id, e)
	if data == nil {
		zap.L().Error("failed to convert contact", zap.String("id", id), zap.String("type", fmt.Sprintf("%T", e)), zap.Any("raw", e))
		return
	}

	wookData := &WookEvent[WookContactUpsertData]{
		Instance: instance.ID,
		Data:     &WookContactUpsertData{*data},
		DateTime: time.Now(),
		Event:    WookContactsUpsert,
	}

	s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) handlePictureEvent(id string, instance *models.Instance, e *events.Picture, eventMap map[string]bool) {
	if !eventMap["CONTACTS_UPSERT"] {
		return
	}

	data := s.convertPicture(id, e)
	if data == nil {
		return
	}

	wookData := &WookEvent[WookContactUpsertData]{
		Instance: instance.ID,
		Data:     &WookContactUpsertData{*data},
		DateTime: e.Timestamp,
		Event:    WookContactsUpsert,
	}

	s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
}

var (
	historySyncWatchdog sync.Map
	historySyncProgress sync.Map
)

const historySyncTimeout = 180 * time.Second

func (s *Whatsmiau) handleHistorySyncEvent(id string, instance *models.Instance, e *events.HistorySync, eventMap map[string]bool) {
	if e == nil || e.Data == nil {
		return
	}

	progress := e.Data.GetProgress()
	isLatest := progress >= 100

	// An on-demand sync is the answer to an explicit recovery request (a lost
	// message being re-fetched from the phone). It must be delivered regardless
	// of SyncFullHistory, which only governs the bulk import at pairing time.
	onDemand := e.Data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND

	if (instance.FullHistoryEnabled() || onDemand) && eventMap["MESSAGES_SET"] {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute*2)
		defer cancel()

		var messages []WookMessageData
		for _, conv := range e.Data.Conversations {
			for _, msg := range conv.GetMessages() {
				if msg.Message == nil || msg.Message.Message == nil {
					continue
				}

				messageData := s.buildMessageDataFromHistory(ctx, id, msg.Message, conv.GetName(), conv.GetDisplayName())
				if messageData == nil {
					continue
				}

				messageData.InstanceId = instance.ID
				messages = append(messages, *messageData)
			}
		}

		if len(messages) > 0 {
			prog := int(progress)
			wookEvent := &WookEvent[[]WookMessageData]{
				Instance: instance.ID,
				Data:     &messages,
				DateTime: time.Now(),
				Event:    WookMessagesSet,
				IsLatest: &isLatest,
				Progress: &prog,
			}
			s.emit(wookEvent, instance.Webhook.Url, instance.Webhook.Headers)
		}

		// The watchdog tracks the bulk import only. An on-demand sync carries no
		// meaningful progress, so letting it touch the watchdog would either
		// abort a running import or keep one alive forever.
		if !onDemand {
			if isLatest {
				s.stopHistorySyncWatchdog(id)
			} else {
				historySyncProgress.Store(id, progress)
				s.resetHistorySyncWatchdog(id)
			}
		}
	}

	if !eventMap["CONTACTS_UPSERT"] {
		return
	}

	data := s.convertContactHistorySync(id, e.Data.GetPushnames(), e.Data.Conversations)
	if data == nil {
		return
	}

	wookData := &WookEvent[WookContactUpsertData]{
		Instance: instance.ID,
		Data:     &data,
		DateTime: time.Now(),
		Event:    WookContactsUpsert,
	}

	s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
}

func cleanHistorySyncState(id string) {
	historySyncWatchdog.Delete(id)
	historySyncProgress.Delete(id)
}

func (s *Whatsmiau) resetHistorySyncWatchdog(id string) {
	if existing, ok := historySyncWatchdog.Load(id); ok {
		existing.(*time.Timer).Reset(historySyncTimeout)
		return
	}

	newTimer := time.AfterFunc(historySyncTimeout, func() {
		cleanHistorySyncState(id)
	})
	actual, loaded := historySyncWatchdog.LoadOrStore(id, newTimer)
	if loaded {
		newTimer.Stop()
		actual.(*time.Timer).Reset(historySyncTimeout)
	}
}

func (s *Whatsmiau) stopHistorySyncWatchdog(id string) {
	if watchdog, ok := historySyncWatchdog.Load(id); ok {
		watchdog.(*time.Timer).Stop()
	}
	cleanHistorySyncState(id)
}

func (s *Whatsmiau) handleGroupInfoEvent(id string, instance *models.Instance, e *events.GroupInfo, eventMap map[string]bool) {
	if !eventMap["CONTACTS_UPSERT"] {
		return
	}

	if instance.GroupsIgnore != nil && *instance.GroupsIgnore {
		return
	}

	data := s.convertGroupInfo(id, e)
	if data == nil {
		zap.L().Debug("failed to convert group info", zap.String("id", id), zap.String("type", fmt.Sprintf("%T", e)), zap.Any("raw", e))
		return
	}

	wookData := &WookEvent[WookContactUpsertData]{
		Instance: instance.ID,
		Data:     &WookContactUpsertData{*data},
		DateTime: time.Now(),
		Event:    WookContactsUpsert,
	}

	s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) emitGroupParticipantsUpdate(id string, instance *models.Instance, groupJID string, author string, participantJIDs []types.JID, timestamp time.Time, action string, admin *bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	participants := make([]WookGroupParticipantJID, 0, len(participantJIDs))
	participantsData := make([]WookGroupParticipantsDataItem, 0, len(participantJIDs))

	for _, j := range participantJIDs {
		jid, lid := s.GetJidLid(ctx, id, j)

		p := WookGroupParticipantJID{
			ID:          lid,
			PhoneNumber: jid,
			Admin:       admin,
		}

		participants = append(participants, p)
		participantsData = append(participantsData, WookGroupParticipantsDataItem{
			JID: p,
		})
	}

	data := &WookGroupParticipantsUpdateData{
		ID:               groupJID,
		Author:           author,
		Participants:     participants,
		Action:           action,
		ParticipantsData: participantsData,
	}

	wookEvent := &WookEvent[WookGroupParticipantsUpdateData]{
		Instance: instance.ID,
		Data:     data,
		DateTime: timestamp,
		Sender:   instance.RemoteJID,
		Event:    WookGroupParticipantsUpdate,
	}

	s.emit(wookEvent, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) handleGroupParticipantsUpdateEvent(id string, instance *models.Instance, e *events.GroupInfo, eventMap map[string]bool) {
	if !eventMap["GROUP_PARTICIPANTS_UPDATE"] {
		return
	}

	if instance.GroupsIgnore != nil && *instance.GroupsIgnore {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	groupJID := e.JID.ToNonAD().String()

	var author string
	if e.Sender != nil {
		_, author = s.GetJidLid(ctx, id, *e.Sender)
	}

	if len(e.Join) > 0 {
		s.emitGroupParticipantsUpdate(id, instance, groupJID, author, e.Join, e.Timestamp, "add", nil)
	}

	if len(e.Leave) > 0 {
		s.emitGroupParticipantsUpdate(id, instance, groupJID, author, e.Leave, e.Timestamp, "remove", nil)
	}

	if len(e.Promote) > 0 {
		admin := true
		s.emitGroupParticipantsUpdate(id, instance, groupJID, author, e.Promote, e.Timestamp, "promote", &admin)
	}

	if len(e.Demote) > 0 {
		admin := false
		s.emitGroupParticipantsUpdate(id, instance, groupJID, author, e.Demote, e.Timestamp, "demote", &admin)
	}
}

func (s *Whatsmiau) handleJoinedGroupEvent(id string, instance *models.Instance, e *events.JoinedGroup, eventMap map[string]bool) {
	if !eventMap["GROUP_PARTICIPANTS_UPDATE"] {
		return
	}

	if instance.GroupsIgnore != nil && *instance.GroupsIgnore {
		return
	}

	if instance.RemoteJID == "" {
		return
	}

	instanceJID, err := types.ParseJID(instance.RemoteJID)
	if err != nil {
		zap.L().Warn("failed to parse instance RemoteJID for JoinedGroup event",
			zap.String("instance", id),
			zap.String("remoteJID", instance.RemoteJID),
			zap.Error(err),
		)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var author string
	if e.Sender != nil {
		_, author = s.GetJidLid(ctx, id, *e.Sender)
	}

	s.emitGroupParticipantsUpdate(id, instance, e.JID.ToNonAD().String(), author, []types.JID{instanceJID}, time.Now(), "add", nil)
}

func (s *Whatsmiau) handlePushNameEvent(id string, instance *models.Instance, e *events.PushName, eventMap map[string]bool) {
	if !eventMap["CONTACTS_UPSERT"] {
		return
	}

	if canIgnoreGroup(e, instance) {
		return
	}

	data := s.convertPushName(id, e)
	if data == nil {
		zap.L().Error("failed to convert pushname", zap.String("id", id), zap.String("type", fmt.Sprintf("%T", e)), zap.Any("raw", e))
		return
	}

	wookData := &WookEvent[WookContactUpsertData]{
		Instance: instance.ID,
		Data:     &WookContactUpsertData{*data},
		DateTime: time.Now(),
		Event:    WookContactsUpsert,
	}

	s.emit(wookData, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) handleConnectionUpdateEvent(id string, instance *models.Instance, state string, statusReason int, eventMap map[string]bool) {
	if !eventMap["CONNECTION_UPDATE"] {
		return
	}

	data := &WookConnectionUpdateData{
		Instance:     instance.ID,
		State:        state,
		StatusReason: statusReason,
	}

	if state == "open" {
		if client, ok := s.clients.Load(id); ok && client.Store != nil && client.Store.ID != nil {
			data.Wuid = client.Store.ID.ToNonAD().String()
			data.ProfileName = client.Store.PushName
		}
	}

	wookEvent := &WookEvent[WookConnectionUpdateData]{
		Instance: instance.ID,
		Data:     data,
		DateTime: time.Now(),
		Event:    WookConnectionUpdate,
	}

	zap.L().Debug("connection update event", zap.String("instance", id), zap.Any("data", data))
	s.emit(wookEvent, instance.Webhook.Url, instance.Webhook.Headers)
}

// emitQRCodeUpdated envia o evento qrcode.updated para o webhook, permitindo
// que o CRM exiba o QR de forma assíncrona (paridade com a Evolution API, que
// entrega o QR por evento além da resposta síncrona de /connect).
func (s *Whatsmiau) emitQRCodeUpdated(id, code, pairingCode string) {
	instance := s.getInstanceCached(id)
	if instance == nil || instance.Webhook.Enabled == nil || !*instance.Webhook.Enabled {
		return
	}

	eventMap := make(map[string]bool)
	for _, evt := range instance.Webhook.Events {
		eventMap[evt] = true
	}
	if !eventMap["QRCODE_UPDATED"] {
		return
	}

	var base64Png string
	if png, err := qrcode.Encode(code, qrcode.Medium, 256); err != nil {
		zap.L().Error("failed to encode qrcode for webhook", zap.String("instance", id), zap.Error(err))
	} else {
		base64Png = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}

	wookEvent := &WookEvent[WookQRCodeUpdateData]{
		Instance: instance.ID,
		Data: &WookQRCodeUpdateData{
			Instance:   instance.ID,
			StatusCode: 200,
			QRCode: &WookQRCode{
				Instance:    instance.ID,
				Code:        code,
				Base64:      base64Png,
				PairingCode: pairingCode,
			},
		},
		DateTime: time.Now(),
		Event:    WookQRCodeUpdated,
	}

	zap.L().Debug("qrcode updated event", zap.String("instance", id))
	s.emit(wookEvent, instance.Webhook.Url, instance.Webhook.Headers)
}

func (s *Whatsmiau) emitConnectionUpdate(id string, state string, statusReason int) {
	instance := s.getInstanceCached(id)
	if instance == nil || instance.Webhook.Enabled == nil || !*instance.Webhook.Enabled {
		return
	}

	eventMap := make(map[string]bool)
	for _, evt := range instance.Webhook.Events {
		eventMap[evt] = true
	}

	s.handleConnectionUpdateEvent(id, instance, state, statusReason, eventMap)
}

// parseWAMessage converts a raw waE2E.Message into our internal representation.
// It only inspects the content of the protobuf message itself –
// media upload (URL/Base64 generation) is handled later by the caller.
func (s *Whatsmiau) parseWAMessage(m *waE2E.Message) (string, *WookMessageRaw, *waE2E.ContextInfo) {
	var messageType string
	raw := &WookMessageRaw{}
	var ci *waE2E.ContextInfo

	// === Prioritize action-like messages ===
	if r := m.GetReactionMessage(); r != nil {
		messageType = "reactionMessage"
		reactionKey := &WookKey{}
		if rk := r.GetKey(); rk != nil {
			reactionKey.RemoteJid = rk.GetRemoteJID()
			reactionKey.FromMe = rk.GetFromMe()
			reactionKey.Id = rk.GetID()
			reactionKey.Participant = rk.GetParticipant()
		}
		raw.ReactionMessage = &ReactionMessageRaw{
			Text:              r.GetText(),
			SenderTimestampMs: i64(r.GetSenderTimestampMS()),
			Key:               reactionKey,
		}
	} else if lr := m.GetListResponseMessage(); lr != nil {
		messageType = "listResponseMessage"
		listType := lr.GetListType().String()
		var selectedRowID string
		if ssr := lr.GetSingleSelectReply(); ssr != nil {
			selectedRowID = ssr.GetSelectedRowID()
		}
		raw.ListResponseMessage = &WookListMessageRaw{
			ListType: listType,
			SingleSelectReply: &WookListMessageRawListSingleSelectReply{
				SelectedRowId: selectedRowID,
			},
		}
	} else if br := m.GetButtonsResponseMessage(); br != nil {
		messageType = "buttonsResponseMessage"
		raw.Conversation = br.GetSelectedDisplayText()
		ci = br.GetContextInfo()
	} else if img := m.GetImageMessage(); img != nil {
		messageType = "imageMessage"
		ci = img.GetContextInfo()
		raw.ImageMessage = &WookImageMessageRaw{
			Url:               img.GetURL(),
			Mimetype:          img.GetMimetype(),
			FileSha256:        b64(img.GetFileSHA256()),
			FileLength:        u64(img.GetFileLength()),
			Height:            int(img.GetHeight()),
			Width:             int(img.GetWidth()),
			Caption:           img.GetCaption(),
			MediaKey:          b64(img.GetMediaKey()),
			FileEncSha256:     b64(img.GetFileEncSHA256()),
			DirectPath:        img.GetDirectPath(),
			MediaKeyTimestamp: i64(img.GetMediaKeyTimestamp()),
			JpegThumbnail:     b64(img.GetJPEGThumbnail()),
			ViewOnce:          img.GetViewOnce(),
		}
	} else if aud := m.GetAudioMessage(); aud != nil {
		messageType = "audioMessage"
		ci = aud.GetContextInfo()
		raw.AudioMessage = &WookAudioMessageRaw{
			Url:               aud.GetURL(),
			Mimetype:          aud.GetMimetype(),
			FileSha256:        b64(aud.GetFileSHA256()),
			FileLength:        u64(aud.GetFileLength()),
			Seconds:           int(aud.GetSeconds()),
			Ptt:               aud.GetPTT(),
			MediaKey:          b64(aud.GetMediaKey()),
			FileEncSha256:     b64(aud.GetFileEncSHA256()),
			DirectPath:        aud.GetDirectPath(),
			MediaKeyTimestamp: i64(aud.GetMediaKeyTimestamp()),
			Waveform:          b64(aud.GetWaveform()),
			ViewOnce:          aud.GetViewOnce(),
		}
	} else if doc := m.GetDocumentMessage(); doc != nil {
		messageType = "documentMessage"
		ci = doc.GetContextInfo()
		raw.DocumentMessage = &WookDocumentMessageRaw{
			Url:               doc.GetURL(),
			Mimetype:          doc.GetMimetype(),
			Title:             doc.GetTitle(),
			FileSha256:        b64(doc.GetFileSHA256()),
			FileLength:        u64(doc.GetFileLength()),
			PageCount:         int(doc.GetPageCount()),
			MediaKey:          b64(doc.GetMediaKey()),
			FileName:          doc.GetFileName(),
			FileEncSha256:     b64(doc.GetFileEncSHA256()),
			DirectPath:        doc.GetDirectPath(),
			MediaKeyTimestamp: i64(doc.GetMediaKeyTimestamp()),
			ContactVcard:      doc.GetContactVcard(),
			JpegThumbnail:     b64(doc.GetJPEGThumbnail()),
			Caption:           doc.GetCaption(),
		}
	} else if video := m.GetVideoMessage(); video != nil {
		messageType = "videoMessage"
		raw.VideoMessage = &WookVideoMessageRaw{
			Url:               video.GetURL(),
			Mimetype:          video.GetMimetype(),
			Caption:           video.GetCaption(),
			FileSha256:        b64(video.GetFileSHA256()),
			FileLength:        u64(video.GetFileLength()),
			Seconds:           video.GetSeconds(),
			MediaKey:          b64(video.GetMediaKey()),
			FileEncSha256:     b64(video.GetFileEncSHA256()),
			DirectPath:        video.GetDirectPath(),
			MediaKeyTimestamp: i64(video.GetMediaKeyTimestamp()),
			JPEGThumbnail:     b64(video.GetJPEGThumbnail()),
			GIFPlayback:       video.GetGifPlayback(),
			ViewOnce:          video.GetViewOnce(),
			Height:            int(video.GetHeight()),
			Width:             int(video.GetWidth()),
		}
		ci = video.GetContextInfo()
	} else if contact := m.GetContactMessage(); contact != nil {
		card, err := vcard.NewDecoder(strings.NewReader(contact.GetVcard())).Decode()
		if err != nil {
			zap.L().Error("decode card error", zap.Error(err))
		}

		messageType = "contactMessage"
		raw.ContactMessage = &ContactMessageRaw{
			VCard:        contact.GetVcard(),
			DisplayName:  contact.GetDisplayName(),
			DecodedVcard: card,
		}
		ci = contact.GetContextInfo()
	} else if contactArray := m.GetContactsArrayMessage(); contactArray != nil {
		messageType = "contactsArrayMessage"
		var contacts []ContactMessageRaw
		for _, contact := range contactArray.Contacts {
			card, err := vcard.NewDecoder(strings.NewReader(contact.GetVcard())).Decode()
			if err != nil {
				zap.L().Error("decode card error", zap.Error(err))
			}

			contacts = append(contacts, ContactMessageRaw{
				VCard:        contact.GetVcard(),
				DisplayName:  contact.GetDisplayName(),
				DecodedVcard: card,
			})
		}
		raw.ContactsArrayMessage = &ContactsArrayMessageRaw{
			DisplayName: contactArray.GetDisplayName(),
			Contacts:    contacts,
		}
		ci = contactArray.GetContextInfo()
	} else if sticker := m.GetStickerMessage(); sticker != nil {
		messageType = "stickerMessage"
		ci = sticker.GetContextInfo()
		raw.StickerMessage = &WookStickerMessageRaw{
			Url:               sticker.GetURL(),
			FileSha256:        b64(sticker.GetFileSHA256()),
			FileEncSha256:     b64(sticker.GetFileEncSHA256()),
			MediaKey:          b64(sticker.GetMediaKey()),
			Mimetype:          sticker.GetMimetype(),
			DirectPath:        sticker.GetDirectPath(),
			FileLength:        u64(sticker.GetFileLength()),
			MediaKeyTimestamp: i64(sticker.GetMediaKeyTimestamp()),
			IsAnimated:        sticker.GetIsAnimated(),
			PngThumbnail:      b64(sticker.GetPngThumbnail()),
			Height:            int(sticker.GetHeight()),
			Width:             int(sticker.GetWidth()),
		}
	} else if loc := m.GetLocationMessage(); loc != nil {
		messageType = "locationMessage"
		ci = loc.GetContextInfo()
		raw.LocationMessage = &WookLocationMessageRaw{
			DegreesLatitude:  loc.GetDegreesLatitude(),
			DegreesLongitude: loc.GetDegreesLongitude(),
			Name:             loc.GetName(),
			Address:          loc.GetAddress(),
			Url:              loc.GetURL(),
			JpegThumbnail:    b64(loc.GetJPEGThumbnail()),
		}
	} else if live := m.GetLiveLocationMessage(); live != nil {
		messageType = "liveLocationMessage"
		ci = live.GetContextInfo()
		raw.LiveLocationMessage = &WookLiveLocationMessageRaw{
			DegreesLatitude:              live.GetDegreesLatitude(),
			DegreesLongitude:             live.GetDegreesLongitude(),
			AccuracyInMeters:             live.GetAccuracyInMeters(),
			SpeedInMps:                   live.GetSpeedInMps(),
			DegreesClockwiseFromMagNorth: live.GetDegreesClockwiseFromMagneticNorth(),
			Caption:                      live.GetCaption(),
			SequenceNumber:               live.GetSequenceNumber(),
			TimeOffset:                   live.GetTimeOffset(),
			JpegThumbnail:                b64(live.GetJPEGThumbnail()),
		}
	} else if poll := m.GetPollCreationMessage(); poll != nil {
		messageType = "pollCreationMessage"
		ci = poll.GetContextInfo()
		options := make([]WookPollOption, 0, len(poll.GetOptions()))
		for _, opt := range poll.GetOptions() {
			options = append(options, WookPollOption{OptionName: opt.GetOptionName()})
		}
		raw.PollCreationMessage = &WookPollCreationMessageRaw{
			Name:                   poll.GetName(),
			Options:                options,
			SelectableOptionsCount: poll.GetSelectableOptionsCount(),
		}
	} else if poll := m.GetPollCreationMessageV3(); poll != nil {
		messageType = "pollCreationMessageV3"

		ci = poll.GetContextInfo()

		options := make([]WookPollOption, 0, len(poll.GetOptions()))
		for _, opt := range poll.GetOptions() {
			options = append(options, WookPollOption{
				OptionName: opt.GetOptionName(),
			})
		}

		raw.PollCreationMessage = &WookPollCreationMessageRaw{
			Name:                   poll.GetName(),
			Options:                options,
			SelectableOptionsCount: poll.GetSelectableOptionsCount(),
		}

	} else if pollUp := m.GetPollUpdateMessage(); pollUp != nil {
		messageType = "pollUpdateMessage"
		updKey := &WookKey{}
		if k := pollUp.GetPollCreationMessageKey(); k != nil {
			updKey.RemoteJid = k.GetRemoteJID()
			updKey.FromMe = k.GetFromMe()
			updKey.Id = k.GetID()
			updKey.Participant = k.GetParticipant()
		}
		raw.PollUpdateMessage = &WookPollUpdateMessageRaw{
			PollCreationMessageKey: updKey,
			SenderTimestampMs:      i64(pollUp.GetSenderTimestampMS()),
			Vote: &WookPollVote{
				EncPayload: b64(pollUp.GetVote().GetEncPayload()),
				EncIv:      b64(pollUp.GetVote().GetEncIV()),
			},
		}
	} else if ptv := m.GetPtvMessage(); ptv != nil {
		messageType = "ptvMessage"
		ci = ptv.GetContextInfo()
		raw.PtvMessage = &WookPtvMessageRaw{
			Url:           ptv.GetURL(),
			Mimetype:      ptv.GetMimetype(),
			FileSha256:    b64(ptv.GetFileSHA256()),
			FileLength:    u64(ptv.GetFileLength()),
			Seconds:       ptv.GetSeconds(),
			MediaKey:      b64(ptv.GetMediaKey()),
			FileEncSha256: b64(ptv.GetFileEncSHA256()),
			DirectPath:    ptv.GetDirectPath(),
			JpegThumbnail: b64(ptv.GetJPEGThumbnail()),
		}
	} else if conv := strings.TrimSpace(m.GetConversation()); conv != "" {
		messageType = "conversation"
		raw.Conversation = conv
	} else if et := m.GetExtendedTextMessage(); et != nil && len(et.GetText()) > 0 {
		messageType = "conversation"
		raw.Conversation = et.GetText()
		ci = et.GetContextInfo()
	} else if ec := m.GetEncCommentMessage(); ec != nil {
		messageType = "encCommentMessage"
		raw.EncCommentMessage = &WookEncCommentMessageRaw{
			EncPayload: b64(ec.GetEncPayload()),
			EncIv:      b64(ec.GetEncIV()),
		}
		if targetKey := ec.GetTargetMessageKey(); targetKey != nil {
			raw.EncCommentMessage.TargetMessageKey = &WookKey{
				RemoteJid:   targetKey.GetRemoteJID(),
				FromMe:      targetKey.GetFromMe(),
				Id:          targetKey.GetID(),
				Participant: targetKey.GetParticipant(),
			}
		}
	}

	// Everything the branches above did not map is still forwarded verbatim via
	// the protojson fallback, and the type is derived from the protobuf field
	// name so consumers see e.g. "templateMessage" instead of "unknown".
	raw.Fallback = buildRawFallback(m)
	if messageType == "" {
		messageType = firstContentKey(raw.Fallback)
		if messageType == "" {
			messageType = "unknown"
		}
	}

	if ci == nil {
		ci = contextInfoOf(m)
	}

	return messageType, raw, ci
}

// contextInfoOf digs the ContextInfo out of a message whose type parseWAMessage
// does not map explicitly, so quoted messages, mentions and ad replies keep
// working for those types too.
func contextInfoOf(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetTemplateMessage() != nil:
		return m.GetTemplateMessage().GetContextInfo()
	case m.GetTemplateButtonReplyMessage() != nil:
		return m.GetTemplateButtonReplyMessage().GetContextInfo()
	case m.GetButtonsMessage() != nil:
		return m.GetButtonsMessage().GetContextInfo()
	case m.GetListMessage() != nil:
		return m.GetListMessage().GetContextInfo()
	case m.GetInteractiveMessage() != nil:
		return m.GetInteractiveMessage().GetContextInfo()
	case m.GetInteractiveResponseMessage() != nil:
		return m.GetInteractiveResponseMessage().GetContextInfo()
	case m.GetOrderMessage() != nil:
		return m.GetOrderMessage().GetContextInfo()
	case m.GetProductMessage() != nil:
		return m.GetProductMessage().GetContextInfo()
	case m.GetGroupInviteMessage() != nil:
		return m.GetGroupInviteMessage().GetContextInfo()
	case m.GetEventMessage() != nil:
		return m.GetEventMessage().GetContextInfo()
	case m.GetPollCreationMessageV2() != nil:
		return m.GetPollCreationMessageV2().GetContextInfo()
	case m.GetRequestPhoneNumberMessage() != nil:
		return m.GetRequestPhoneNumberMessage().GetContextInfo()
	default:
		return nil
	}
}

func (s *Whatsmiau) convertContactHistorySync(id string, event []*waHistorySync.Pushname, conversations []*waHistorySync.Conversation) WookContactUpsertData {
	resultMap := make(map[string]WookContact)
	for _, pushName := range event {

		if len(pushName.GetPushname()) == 0 {
			continue
		}

		if dt := strings.Split(pushName.GetPushname(), "@"); len(dt) == 2 && (dt[1] == "g.us" || dt[1] == "s.whatsapp.net") {
			return nil
		}

		jid, err := types.ParseJID(pushName.GetID())
		if err != nil {
			zap.L().Error("failed to parse jid", zap.String("pushname", pushName.GetPushname()))
			return nil
		}

		jidParsed, lid := s.GetJidLid(context.Background(), id, jid)

		resultMap[jidParsed] = WookContact{
			RemoteJid:  jidParsed,
			PushName:   pushName.GetPushname(),
			InstanceId: id,
			RemoteLid:  lid,
		}
	}

	for _, conversation := range conversations {
		name := conversation.GetName()
		if len(name) == 0 {
			name = conversation.GetDisplayName()
		}
		if len(name) == 0 {
			name = conversation.GetUsername()
		}
		if len(name) == 0 {
			continue
		}
		if dt := strings.Split(name, "@"); len(dt) == 2 && (dt[1] == "g.us" || dt[1] == "s.whatsapp.net") {
			return nil
		}

		jid, err := types.ParseJID(conversation.GetID())
		if err != nil {
			zap.L().Error("failed to parse jid", zap.String("name", conversation.GetName()))
			return nil
		}
		jidParsed, lid := s.GetJidLid(context.Background(), id, jid)

		resultMap[conversation.GetID()] = WookContact{
			RemoteJid:  jidParsed,
			PushName:   name,
			InstanceId: id,
			RemoteLid:  lid,
		}
	}

	var result []WookContact
	for _, c := range resultMap {
		jid, err := types.ParseJID(c.RemoteJid)
		if err != nil {
			continue
		}

		url, b64Pic, err := s.getPic(id, jid)
		if err != nil {
			zap.L().Error("failed to get pic", zap.Error(err))
		}

		picUrl, err := s.uploadPic(context.Background(), jid.ToNonAD().String(), b64Pic)
		if err != nil {
			zap.L().Error("failed to upload pic", zap.Error(err))
		} else {
			url = picUrl
		}

		c.ProfilePicUrl = url
		c.Base64Pic = b64Pic
		result = append(result, c)
	}

	return result
}

func (s *Whatsmiau) convertEventMessage(id string, instance *models.Instance, evt *events.Message) *WookMessageData {
	ctx, c := context.WithTimeout(context.Background(), time.Second*60)
	defer c()

	client, ok := s.clients.Load(id)
	if !ok {
		zap.L().Warn("no client for event", zap.String("id", id))
		return nil
	}

	if evt == nil || evt.Message == nil {
		return nil
	}

	jid, lid := s.GetJidLid(ctx, id, evt.Info.Chat)
	senderJid, _ := s.GetJidLid(ctx, id, evt.Info.Sender)

	// Always unwrap to work with the real content
	e := evt.UnwrapRaw()
	m := e.Message

	// Build the key
	addressingMode := "lid"
	if lid == "" {
		addressingMode = "jid"
	}
	key := &WookKey{
		RemoteJid:      jid,
		RemoteLid:      lid,
		FromMe:         e.Info.IsFromMe,
		Id:             e.Info.ID,
		Participant:    senderJid,
		AddressingMode: addressingMode,
	}

	// Determine status
	status := "received"
	if e.Info.IsFromMe {
		status = "sent"
	}

	// Timestamp
	ts := e.Info.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	// Convert the WA protobuf message into our internal raw structure
	messageType, raw, ci := s.parseWAMessage(m)

	// Store poll creation options in Redis for later vote decryption
	if raw.PollCreationMessage != nil {
		chatJid := e.Info.Chat.ToNonAD().String()
		cacheKey := "poll:cache:" + chatJid + ":" + e.Info.ID
		entry := PollCreationEntry{
			Options:   make([]PollOptionHash, len(raw.PollCreationMessage.Options)),
			CreatedAt: time.Now(),
		}
		for i, opt := range raw.PollCreationMessage.Options {
			hash := sha256.Sum256([]byte(opt.OptionName))
			entry.Options[i] = PollOptionHash{Name: opt.OptionName, Hash: hash}
		}
		if data, err := json.Marshal(entry); err == nil {
			if err := services.Redis().Set(context.Background(), cacheKey, data, 7*24*time.Hour).Err(); err != nil {
				zap.L().Error("failed to store poll creation in redis",
					zap.Error(err),
					zap.String("cache_key", cacheKey))
			}
		} else {
			zap.L().Error("failed to marshal poll creation entry",
				zap.Error(err),
				zap.String("cache_key", cacheKey))
		}
	}

	// Upload media (URL / Base64) when needed
	switch messageType {
	case "imageMessage":
		if img := m.GetImageMessage(); img != nil {
			raw.MediaURL, raw.Base64 = s.uploadMessageFile(ctx, instance, client, img, img.GetMimetype(), "")
		}
	case "audioMessage":
		if aud := m.GetAudioMessage(); aud != nil {
			raw.MediaURL, raw.Base64 = s.uploadMessageFile(ctx, instance, client, aud, aud.GetMimetype(), "")
		}
	case "documentMessage":
		if doc := m.GetDocumentMessage(); doc != nil {
			raw.MediaURL, raw.Base64 = s.uploadMessageFile(ctx, instance, client, doc, doc.GetMimetype(), doc.GetFileName())
		}
	case "videoMessage":
		if vid := m.GetVideoMessage(); vid != nil {
			raw.MediaURL, raw.Base64 = s.uploadMessageFile(ctx, instance, client, vid, vid.GetMimetype(), "")
		}
	case "stickerMessage":
		if st := m.GetStickerMessage(); st != nil {
			raw.MediaURL, raw.Base64 = s.uploadMessageFile(ctx, instance, client, st, st.GetMimetype(), "")
		}
	case "ptvMessage":
		if ptv := m.GetPtvMessage(); ptv != nil {
			raw.MediaURL, raw.Base64 = s.uploadMessageFile(ctx, instance, client, ptv, ptv.GetMimetype(), "")
		}
	}

	// Map MessageContextInfo (quoted, mentions, disappearing mode, external ad reply)
	var messageContext WookMessageContextInfo
	if ci != nil {
		messageContext.EphemeralSettingTimestamp = i64(ci.GetEphemeralSettingTimestamp())
		messageContext.StanzaId = ci.GetStanzaID()
		messageContext.Participant = ci.GetParticipant()
		messageContext.Expiration = int(ci.GetExpiration())
		messageContext.MentionedJid = ci.GetMentionedJID()
		messageContext.ConversionSource = ci.GetConversionSource()
		messageContext.ConversionData = b64(ci.GetConversionData())
		messageContext.ConversionDelaySeconds = int(ci.GetConversionDelaySeconds())
		messageContext.EntryPointConversionSource = ci.GetEntryPointConversionSource()
		messageContext.EntryPointConversionApp = ci.GetEntryPointConversionApp()
		messageContext.EntryPointConversionDelaySeconds = int(ci.GetEntryPointConversionDelaySeconds())
		messageContext.TrustBannerAction = ci.GetTrustBannerAction()

		if dm := ci.GetDisappearingMode(); dm != nil {
			messageContext.DisappearingMode = &ContextInfoDisappearingMode{
				Initiator:     dm.GetInitiator().String(),
				Trigger:       dm.GetTrigger().String(),
				InitiatedByMe: dm.GetInitiatedByMe(),
			}
		}

		if ear := ci.GetExternalAdReply(); ear != nil {
			messageType = "conversation"
			messageContext.ExternalAdReply = &WookMessageContextInfoExternalAdReply{
				Title:                 ear.GetTitle(),
				Body:                  ear.GetBody(),
				MediaType:             ear.GetMediaType().String(),
				ThumbnailUrl:          ear.GetThumbnailURL(),
				Thumbnail:             b64(ear.GetThumbnail()),
				SourceType:            ear.GetSourceType(),
				SourceId:              ear.GetSourceID(),
				SourceUrl:             ear.GetSourceURL(),
				ContainsAutoReply:     ear.GetContainsAutoReply(),
				RenderLargerThumbnail: ear.GetRenderLargerThumbnail(),
				ShowAdAttribution:     ear.GetShowAdAttribution(),
				CtwaClid:              ear.GetCtwaClid(),
			}
		}

		if qm := ci.GetQuotedMessage(); qm != nil {
			_, qmRaw, _ := s.parseWAMessage(qm)
			messageContext.QuotedMessage = qmRaw
		}
	}

	// Decrypt poll vote and build pollUpdates
	var pollUpdates []WookPollUpdate
	if raw.PollUpdateMessage != nil && raw.PollUpdateMessage.PollCreationMessageKey != nil {
		chatJid := e.Info.Chat.ToNonAD().String()
		origMsgID := raw.PollUpdateMessage.PollCreationMessageKey.Id
		cacheKey := "poll:cache:" + chatJid + ":" + origMsgID

		if decrypted, err := client.DecryptPollVote(ctx, evt); err == nil && decrypted != nil {
			selectedHashes := decrypted.GetSelectedOptions()

			data, err := services.Redis().Get(context.Background(), cacheKey).Bytes()
			if err == nil {
				var entry PollCreationEntry
				if err := json.Unmarshal(data, &entry); err == nil {
					for _, selectedHash := range selectedHashes {
						for _, opt := range entry.Options {
							if bytes.Equal(selectedHash, opt.Hash[:]) {
								raw.PollUpdateMessage.Vote.SelectedOptions = append(
									raw.PollUpdateMessage.Vote.SelectedOptions,
									opt.Name,
								)
								pollUpdates = append(pollUpdates, WookPollUpdate{
									Name:   opt.Name,
									Voters: []string{senderJid},
								})
								break
							}
						}
					}
				} else {
					zap.L().Error("failed to unmarshal poll creation entry",
						zap.Error(err),
						zap.String("cache_key", cacheKey),
						zap.String("instance_id", id))
				}
			} else if err == redis.Nil {
				zap.L().Warn("poll creation entry not found in redis",
					zap.String("cache_key", cacheKey),
					zap.String("instance_id", id))
			} else {
				zap.L().Error("failed to get poll creation from redis",
					zap.Error(err),
					zap.String("cache_key", cacheKey),
					zap.String("instance_id", id))
			}
		} else if err != nil {
			zap.L().Error("failed to decrypt poll vote",
				zap.Error(err),
				zap.String("instance_id", id))
		}
	}

	return &WookMessageData{
		Key:              key,
		PushName:         strings.TrimSpace(e.Info.PushName),
		Status:           status,
		Message:          raw,
		ContextInfo:      &messageContext,
		MessageType:      messageType,
		MessageTimestamp: int(ts.Unix()),
		InstanceId:       id,
		Source:           "whatsapp",
		PollUpdates:      pollUpdates,
	}
}

// receiptStatus traduz um receipt do whatsmeow no status do webhook. O segundo
// retorno diz se o evento deve ser emitido.
//
// Receipts com IsFromMe são emitidos pela PRÓPRIA conta e falam das mensagens
// que NÓS lemos — inclusive o "chat lido" que o WhatsApp gera no instante em que
// enviamos algo. Propagá-los marcava a mensagem recém-enviada como LIDA em menos
// de um segundo, sem o destinatário ter aberto nada. O ack de uma mensagem
// enviada só pode mudar por receipt de quem a recebeu.
func receiptStatus(evt *events.Receipt) (WookMessageUpdateStatus, bool) {
	if evt.IsFromMe {
		return "", false
	}

	switch evt.Type {
	case types.ReceiptTypeRead:
		return MessageStatusRead, true
	case types.ReceiptTypeDelivered:
		return MessageStatusDeliveryAck, true
	default:
		// Inclui read-self (leitura nossa em outro device), sender (entrega aos
		// nossos próprios dispositivos), retry, played e os de erro.
		return "", false
	}
}

func (s *Whatsmiau) convertEventReceipt(id string, evt *events.Receipt) []WookMessageUpdateData {
	status, ok := receiptStatus(evt)
	if !ok {
		return nil
	}

	chatJid, chatLid := s.GetJidLid(context.Background(), id, evt.Chat)
	participantJid, _ := s.GetJidLid(context.Background(), id, evt.Sender)

	var result []WookMessageUpdateData
	for _, messageID := range evt.MessageIDs {
		result = append(result, WookMessageUpdateData{
			MessageId:   messageID,
			KeyId:       messageID,
			RemoteJid:   chatJid,
			RemoteLid:   chatLid,
			FromMe:      evt.IsFromMe,
			Participant: participantJid,
			Status:      status,
			InstanceId:  id,
		})
	}

	return result
}

func (s *Whatsmiau) uploadMessageFile(ctx context.Context, instance *models.Instance, client *whatsmeow.Client, fileMessage whatsmeow.DownloadableMessage, mimetype, fileName string) (string, string) {
	var (
		b64Result string
		urlResult string
		ext       string
	)

	tmpFile, err := os.CreateTemp("", "file-*")
	if err != nil {
		panic(err)
	}

	defer os.Remove(tmpFile.Name())
	if err := client.DownloadToFile(ctx, fileMessage, tmpFile); err != nil {
		zap.L().Error("failed to download image", zap.Error(err))
		return "", ""
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		zap.L().Error("failed to seek image", zap.Error(err))
	}

	ext = extractExtFromFile(fileName, mimetype, tmpFile)
	if instance.Webhook.Base64 != nil && *instance.Webhook.Base64 {
		data, err := io.ReadAll(tmpFile)
		if err != nil {
			zap.L().Error("failed to read image", zap.Error(err))
		} else {
			b64Result = base64.StdEncoding.EncodeToString(data)
		}
	}
	if s.fileStorage != nil {
		if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
			zap.L().Error("failed to seek image", zap.Error(err))
		}

		urlResult, _, err = s.fileStorage.Upload(ctx, uuid.NewString()+"."+ext, mimetype, tmpFile)
		if err != nil {
			zap.L().Error("failed to upload image", zap.Error(err))
		}
	}

	return urlResult, b64Result
}

func (s *Whatsmiau) uploadPic(ctx context.Context, waId, b64Data string) (string, error) {
	if s.fileStorage == nil {
		return "", nil
	}

	mimetype, ext, _, err := extractFromBase64(b64Data)
	if err != nil {
		return "", err
	}

	waIdTreated := strings.Split(waId, "@")

	urlResult, err := s.fileStorage.UploadBase64IfDontExists(ctx, waIdTreated[0]+"."+ext, mimetype, b64Data)
	if err != nil {
		zap.L().Error("failed to upload image", zap.Error(err))
		return "", err
	}

	return urlResult, nil
}

func (s *Whatsmiau) convertContact(id string, evt *events.Contact) *WookContact {
	url, b64Pic, err := s.getPic(id, evt.JID)
	if err != nil {
		zap.L().Error("failed to get pic", zap.Error(err))
	}

	name := evt.Action.GetFirstName()
	if name == "" {
		name = evt.Action.GetFullName()
	}
	if name == "" {
		name = evt.Action.GetUsername()
	}
	if name == "" {
		return nil
	}

	if dt := strings.Split(name, "@"); len(dt) == 2 && (dt[1] == "g.us" || dt[1] == "s.whatsapp.net") {
		return nil
	}

	picUrl, err := s.uploadPic(context.Background(), evt.JID.ToNonAD().String(), b64Pic)
	if err != nil {
		zap.L().Error("failed to upload pic", zap.Error(err))
	} else {
		url = picUrl
	}

	jid, lid := s.GetJidLid(context.Background(), id, evt.JID)
	return &WookContact{
		RemoteJid:     jid,
		RemoteLid:     lid,
		PushName:      name,
		ProfilePicUrl: url,
		InstanceId:    id,
		Base64Pic:     b64Pic,
	}
}

func (s *Whatsmiau) convertGroupInfo(id string, evt *events.GroupInfo) *WookContact {
	url, b64Pic, err := s.getPic(id, evt.JID)
	if err != nil {
		zap.L().Error("failed to get pic", zap.Error(err))
	}

	if evt.Name == nil || len(evt.Name.Name) == 0 {
		return nil
	}

	if dt := strings.Split(evt.Name.Name, "@"); len(dt) == 2 && (dt[1] == "g.us" || dt[1] == "s.whatsapp.net") {
		return nil
	}

	picUrl, err := s.uploadPic(context.Background(), evt.JID.ToNonAD().String(), b64Pic)
	if err != nil {
		zap.L().Error("failed to upload pic", zap.Error(err))
	} else {
		url = picUrl
	}

	jid, lid := s.GetJidLid(context.Background(), id, evt.JID)

	return &WookContact{
		RemoteJid:     jid,
		PushName:      evt.Name.Name,
		ProfilePicUrl: url,
		InstanceId:    id,
		RemoteLid:     lid,
		Base64Pic:     b64Pic,
	}
}

func (s *Whatsmiau) convertPushName(id string, evt *events.PushName) *WookContact {
	url, b64Pic, err := s.getPic(id, evt.JID)
	if err != nil {
		zap.L().Error("failed to get pic", zap.Error(err))
	}

	name := evt.NewPushName
	if len(name) == 0 {
		name = evt.OldPushName
	}

	if name == "" {
		return nil
	}

	if dt := strings.Split(name, "@"); len(dt) == 2 && (dt[1] == "g.us" || dt[1] == "s.whatsapp.net") {
		return nil
	}

	picUrl, err := s.uploadPic(context.Background(), evt.JID.ToNonAD().String(), b64Pic)
	if err != nil {
		zap.L().Error("failed to upload pic", zap.Error(err))
	} else {
		url = picUrl
	}

	jid, lid := s.GetJidLid(context.Background(), id, evt.JID)

	return &WookContact{
		RemoteJid:     jid,
		PushName:      evt.NewPushName,
		InstanceId:    id,
		ProfilePicUrl: url,
		RemoteLid:     lid,
		Base64Pic:     b64Pic,
	}
}

func (s *Whatsmiau) convertPicture(id string, evt *events.Picture) *WookContact {
	url, b64Pic, err := s.getPic(id, evt.JID)
	if err != nil {
		zap.L().Error("failed to get pic", zap.Error(err))
	}

	if len(url) <= 0 {
		return nil
	}

	picUrl, err := s.uploadPic(context.Background(), evt.JID.ToNonAD().String(), b64Pic)
	if err != nil {
		zap.L().Error("failed to upload pic", zap.Error(err))
	} else {
		url = picUrl
	}

	jid, lid := s.GetJidLid(context.Background(), id, evt.JID)

	return &WookContact{
		RemoteJid:     jid,
		InstanceId:    id,
		Base64Pic:     b64Pic,
		ProfilePicUrl: url,
		RemoteLid:     lid,
	}
}

func (s *Whatsmiau) convertBusinessName(id string, evt *events.BusinessName) *WookContact {
	url, b64Pic, err := s.getPic(id, evt.JID)
	if err != nil {
		zap.L().Error("failed to get pic", zap.Error(err))
	}

	name := evt.NewBusinessName
	if name == "" {
		name = evt.OldBusinessName
	}
	if name == "" && evt.Message != nil {
		name = evt.Message.PushName
	}
	if name == "" && evt.Message != nil && evt.Message.VerifiedName != nil && evt.Message.VerifiedName.Details != nil {
		name = evt.Message.VerifiedName.Details.GetVerifiedName()
	}

	if dt := strings.Split(name, "@"); len(dt) == 2 && (dt[1] == "g.us" || dt[1] == "s.whatsapp.net") {
		return nil
	}

	picUrl, err := s.uploadPic(context.Background(), evt.JID.ToNonAD().String(), b64Pic)
	if err != nil {
		zap.L().Error("failed to upload pic", zap.Error(err))
	} else {
		url = picUrl
	}

	jid, lid := s.GetJidLid(context.Background(), id, evt.JID)

	return &WookContact{
		RemoteJid:     jid,
		InstanceId:    id,
		Base64Pic:     b64Pic,
		ProfilePicUrl: url,
		PushName:      name,
		RemoteLid:     lid,
	}
}

func (s *Whatsmiau) getPic(id string, jid types.JID) (string, string, error) {
	client, ok := s.clients.Load(id)
	if !ok || client == nil {
		zap.L().Warn("no client for event", zap.String("id", id))
		return "", "", fmt.Errorf("no client for event %s", id)
	}

	if !client.IsConnected() {
		zap.L().Warn("client not connected, skipping getPic", zap.String("id", id))
		return "", "", fmt.Errorf("client not connected for %s", id)
	}

	pic, err := client.GetProfilePictureInfo(context.TODO(), jid, &whatsmeow.GetProfilePictureParams{
		Preview:     true,
		IsCommunity: false,
	})
	if err != nil {
		return "", "", nil
	}

	if pic == nil {
		return "", "", err
	}

	res, err := s.httpClient.Get(pic.URL)
	if err != nil {
		zap.L().Error("get profile picture error", zap.String("id", id), zap.Error(err))
		return "", "", err
	}

	picRaw, err := io.ReadAll(res.Body)
	if err != nil {
		zap.L().Error("get profile picture error", zap.String("id", id), zap.Error(err))
		return "", "", err
	}

	return pic.URL, base64.StdEncoding.EncodeToString(picRaw), nil
}
