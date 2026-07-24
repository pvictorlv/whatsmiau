package controllers

import (
	"encoding/base64"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/skip2/go-qrcode"
	"github.com/verbeux-ai/whatsmiau/server/dto"
	"github.com/verbeux-ai/whatsmiau/utils"
	"go.uber.org/zap"
)

// SetPresence godoc
// @Summary      Set global instance presence (available/unavailable)
// @Tags         Instance
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string                  true  "Instance ID"
// @Param        body      body      dto.SetPresenceRequest  true  "Presence parameters"
// @Success      200       {object}  map[string]interface{}
// @Router       /instance/setPresence/{instance} [post]
func (s *Instance) SetPresence(ctx echo.Context) error {
	var request dto.SetPresenceRequest
	if err := ctx.Bind(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusUnprocessableEntity, err, "failed to bind request body")
	}
	if err := validator.New().Struct(&request); err != nil {
		return utils.HTTPFail(ctx, http.StatusBadRequest, err, "invalid request body")
	}

	available := request.Presence == "available"
	if err := s.whatsmiau.SetPresence(ctx.Request().Context(), request.InstanceID, available); err != nil {
		zap.L().Error("Whatsmiau.SetPresence failed", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to set presence")
	}

	return ctx.JSON(http.StatusOK, map[string]interface{}{"presence": request.Presence})
}

// Qrcode godoc
// @Summary      Get the current QR code for an instance
// @Description  Evolution-compatible alias that returns the QR string and a base64 PNG.
// @Tags         Instance
// @Accept       json
// @Produce      json
// @Security     ApiKeyAuth
// @Param        instance  path      string  true  "Instance ID"
// @Success      200       {object}  dto.QrcodeResponse
// @Router       /instance/qrcode/{instance} [get]
func (s *Instance) Qrcode(ctx echo.Context) error {
	c := ctx.Request().Context()
	id := ctx.Param("instance")
	if id == "" {
		id = ctx.Param("id")
	}
	if id == "" {
		return utils.HTTPFail(ctx, http.StatusBadRequest, nil, "instance ID is required in the URL path")
	}

	result, err := s.repo.List(c, id)
	if err != nil {
		zap.L().Error("failed to list instances", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to list instances")
	}
	if len(result) == 0 {
		return utils.HTTPFail(ctx, http.StatusNotFound, nil, "instance not found")
	}

	qrCode, pairingCode, err := s.whatsmiau.Connect(c, id, "")
	if err != nil {
		zap.L().Error("failed to connect instance", zap.Error(err))
		return utils.HTTPFail(ctx, http.StatusInternalServerError, err, "failed to get qrcode")
	}

	if qrCode == "" {
		return ctx.JSON(http.StatusOK, dto.QrcodeResponse{Connected: true})
	}

	var base64Png string
	if png, err := qrcode.Encode(qrCode, qrcode.Medium, 512); err != nil {
		zap.L().Error("failed to encode qrcode", zap.Error(err))
	} else {
		base64Png = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}

	return ctx.JSON(http.StatusOK, dto.QrcodeResponse{
		Connected:   false,
		Code:        qrCode,
		Base64:      base64Png,
		PairingCode: pairingCode,
		Qrcode: &dto.QrcodeInner{
			Instance:    id,
			Code:        qrCode,
			Base64:      base64Png,
			PairingCode: pairingCode,
		},
	})
}
