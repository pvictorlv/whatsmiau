package controllers

import (
	"encoding/base64"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/verbeux-ai/whatsmiau/server/dto"
	"github.com/verbeux-ai/whatsmiau/utils"
	"go.mau.fi/whatsmeow"
	"go.uber.org/zap"
)

// UpdateBlockStatus godoc
// @Summary      Block or unblock a contact
// @Tags         Chat
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                        true  "Instance ID"
// @Param        body      body      dto.UpdateBlockStatusRequest  true  "Block parameters"
// @Success      200       {object}  map[string]interface{}
// @Router       /chat/updateBlockStatus/{instance} [post]
func (s *Chat) UpdateBlockStatus(ctx echo.Context) error {
	var request dto.UpdateBlockStatusRequest
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	jid, err := numberToJid(request.Number)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid number format")
	}

	block := request.Status == "block"
	if err := s.whatsmiau.UpdateBlockStatus(ctx.Request().Context(), request.InstanceID, *jid, block); err != nil {
		zap.L().Error("Whatsmiau.UpdateBlockStatus failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to update block status")
	}

	return ctx.JSON(http.StatusOK, map[string]interface{}{"status": "success", "block": block})
}

// FetchProfile godoc
// @Summary      Fetch a contact profile (name, picture, status, business info)
// @Tags         Chat
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                   true  "Instance ID"
// @Param        body      body      dto.FetchProfileRequest  true  "Number to fetch"
// @Success      200       {object}  whatsmiau.ProfileInfo
// @Router       /chat/fetchProfile/{instance} [post]
func (s *Chat) FetchProfile(ctx echo.Context) error {
	var request dto.FetchProfileRequest
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	jid, err := numberToJid(request.Number)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid number format")
	}

	info, err := s.whatsmiau.FetchProfile(ctx.Request().Context(), request.InstanceID, *jid)
	if err != nil {
		zap.L().Error("Whatsmiau.FetchProfile failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to fetch profile")
	}

	return ctx.JSON(http.StatusOK, info)
}

// FetchProfilePictureUrl godoc
// @Summary      Fetch a contact profile picture URL
// @Tags         Chat
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                   true  "Instance ID"
// @Param        body      body      dto.FetchProfileRequest  true  "Number to fetch"
// @Success      200       {object}  dto.FetchProfilePictureUrlResponse
// @Router       /chat/fetchProfilePictureUrl/{instance} [post]
func (s *Chat) FetchProfilePictureUrl(ctx echo.Context) error {
	var request dto.FetchProfileRequest
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	jid, err := numberToJid(request.Number)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid number format")
	}

	url, err := s.whatsmiau.FetchProfilePictureURL(ctx.Request().Context(), request.InstanceID, *jid)
	if err != nil {
		zap.L().Error("Whatsmiau.FetchProfilePictureURL failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to fetch profile picture url")
	}

	return ctx.JSON(http.StatusOK, dto.FetchProfilePictureUrlResponse{
		Wuid:              jid.ToNonAD().String(),
		ProfilePictureUrl: url,
	})
}

// UpdateMessage godoc
// @Summary      Edit a sent text message
// @Tags         Chat
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                    true  "Instance ID"
// @Param        body      body      dto.UpdateMessageRequest  true  "Edit parameters"
// @Success      200       {object}  map[string]interface{}
// @Router       /chat/updateMessage/{instance} [post]
func (s *Chat) UpdateMessage(ctx echo.Context) error {
	var request dto.UpdateMessageRequest
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	target := request.Key.RemoteJid
	if target == "" {
		target = request.Number
	}
	jid, err := numberToJid(target)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid number format")
	}

	res, err := s.whatsmiau.EditMessage(ctx.Request().Context(), request.InstanceID, *jid, request.Key.Id, request.Text)
	if err != nil {
		zap.L().Error("Whatsmiau.EditMessage failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to edit message")
	}

	return ctx.JSON(http.StatusOK, map[string]interface{}{
		"key": map[string]interface{}{
			"remoteJid": request.Number,
			"fromMe":    true,
			"id":        res.ID,
		},
		"status":           "sent",
		"messageType":      "editedMessage",
		"messageTimestamp": int(res.Timestamp.Unix()),
		"instanceId":       request.InstanceID,
	})
}

// GetBase64FromMediaMessage godoc
// @Summary      Download media on demand and return it as base64
// @Tags         Chat
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                true  "Instance ID"
// @Param        body      body      dto.GetBase64Request  true  "Message with media metadata"
// @Success      200       {object}  dto.GetBase64Response
// @Router       /chat/getBase64FromMediaMessage/{instance} [post]
func (s *Chat) GetBase64FromMediaMessage(ctx echo.Context) error {
	var request dto.GetBase64Request
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	locator, mediaType, mimetype := selectMediaLocator(&request.Message)
	if locator == nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, nil, "no downloadable media found in message")
	}

	mediaKey, err := decodeB64(locator.MediaKey)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid mediaKey")
	}
	encHash, err := decodeB64(locator.FileEncSha256)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid fileEncSha256")
	}
	fileHash, err := decodeB64(locator.FileSha256)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid fileSha256")
	}

	data, err := s.whatsmiau.DownloadMediaWithPath(ctx.Request().Context(), request.InstanceID, locator.DirectPath, encHash, fileHash, mediaKey, mediaType, "")
	if err != nil {
		zap.L().Error("Whatsmiau.DownloadMediaWithPath failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to download media")
	}

	return ctx.JSON(http.StatusOK, dto.GetBase64Response{
		Base64:   base64.StdEncoding.EncodeToString(data),
		Mimetype: mimetype,
	})
}

// selectMediaLocator escolhe o bloco de mídia presente e resolve o MediaType do
// whatsmeow correspondente para o download.
func selectMediaLocator(m *dto.GetBase64Message) (*dto.MediaLocator, whatsmeow.MediaType, string) {
	switch {
	case m.ImageMessage != nil:
		return m.ImageMessage, whatsmeow.MediaImage, m.ImageMessage.Mimetype
	case m.StickerMessage != nil:
		return m.StickerMessage, whatsmeow.MediaImage, m.StickerMessage.Mimetype
	case m.VideoMessage != nil:
		return m.VideoMessage, whatsmeow.MediaVideo, m.VideoMessage.Mimetype
	case m.PtvMessage != nil:
		return m.PtvMessage, whatsmeow.MediaVideo, m.PtvMessage.Mimetype
	case m.AudioMessage != nil:
		return m.AudioMessage, whatsmeow.MediaAudio, m.AudioMessage.Mimetype
	case m.DocumentMessage != nil:
		return m.DocumentMessage, whatsmeow.MediaDocument, m.DocumentMessage.Mimetype
	default:
		return nil, "", ""
	}
}

func decodeB64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

// FetchMessageHistory godoc
// @Summary      Request on-demand history sync for a chat
// @Tags         Chat
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                          true  "Instance ID"
// @Param        body      body      dto.FetchMessageHistoryRequest  true  "History sync parameters"
// @Success      200       {object}  map[string]interface{}
// @Router       /chat/fetchMessageHistory/{instance} [post]
func (s *Chat) FetchMessageHistory(ctx echo.Context) error {
	var request dto.FetchMessageHistoryRequest
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	jid, err := numberToJid(request.RemoteJid)
	if err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid remoteJid format")
	}

	if err := s.whatsmiau.FetchMessageHistory(ctx.Request().Context(), request.InstanceID, *jid, request.Count, request.Timestamp, request.MessageId, request.FromMe); err != nil {
		zap.L().Error("Whatsmiau.FetchMessageHistory failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to request message history")
	}

	return ctx.JSON(http.StatusOK, map[string]interface{}{
		"status":    "requested",
		"remoteJid": request.RemoteJid,
		"count":     request.Count,
	})
}

// FindMessages godoc
// @Summary      Paginated stored-message query (Evolution pull compatibility)
// @Description  whatsmiau does not persist messages; history is delivered via the
// @Description  push messages.set webhook. This endpoint returns an empty page so
// @Description  the Evolution pull-import no-ops gracefully.
// @Tags         Chat
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string  true  "Instance ID"
// @Success      200       {object}  dto.FindMessagesResponse
// @Router       /chat/findMessages/{instance} [post]
func (s *Chat) FindMessages(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, dto.FindMessagesResponse{
		Messages: dto.FindMessagesPage{
			Total:       0,
			Pages:       0,
			CurrentPage: 0,
			Records:     []any{},
		},
	})
}

// DeleteOldMessages godoc
// @Summary      Delete old messages (Evolution compatibility no-op)
// @Description  whatsmiau does not persist messages, so there is nothing to purge.
// @Tags         Chat
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string  true  "Instance ID"
// @Success      200       {object}  map[string]interface{}
// @Router       /chat/deleteOldMessages/{instance} [delete]
func (s *Chat) DeleteOldMessages(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, map[string]interface{}{"status": "success", "deleted": 0})
}
