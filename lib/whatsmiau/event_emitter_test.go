package whatsmiau

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestQRCodeUpdatedPayloadContract trava o formato JSON do evento qrcode.updated
// que o CRM consome: data.statusCode e data.qrcode.code (paridade Evolution).
func TestQRCodeUpdatedPayloadContract(t *testing.T) {
	evt := &WookEvent[WookQRCodeUpdateData]{
		Instance: "wpp_1",
		Event:    WookQRCodeUpdated,
		DateTime: time.Unix(0, 0),
		Data: &WookQRCodeUpdateData{
			Instance:   "wpp_1",
			StatusCode: 200,
			QRCode: &WookQRCode{
				Instance:    "wpp_1",
				Code:        "2@abc123",
				Base64:      "data:image/png;base64,AAAA",
				PairingCode: "ABCD-1234",
			},
		},
	}

	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if got["event"] != "qrcode.updated" {
		t.Errorf("expected event qrcode.updated, got %v", got["event"])
	}
	data, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %T", got["data"])
	}
	if data["statusCode"].(float64) != 200 {
		t.Errorf("expected data.statusCode 200, got %v", data["statusCode"])
	}
	qr, ok := data["qrcode"].(map[string]any)
	if !ok {
		t.Fatalf("data.qrcode is not an object: %T", data["qrcode"])
	}
	if qr["code"] != "2@abc123" {
		t.Errorf("expected data.qrcode.code 2@abc123, got %v", qr["code"])
	}
	if qr["base64"] != "data:image/png;base64,AAAA" {
		t.Errorf("unexpected data.qrcode.base64: %v", qr["base64"])
	}
}

// TestDoEmitSendsCustomHeaders garante que headers configurados no webhook da
// instância (ex.: token de autenticação do CRM) são enviados na requisição,
// além do Content-Type padrão.
func TestDoEmitSendsCustomHeaders(t *testing.T) {
	var gotAuth, gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Whatsmiau{httpClient: srv.Client()}
	success, retry := s.doEmit([]byte(`{"event":"messages.upsert"}`), srv.URL, map[string]string{
		"Authorization": "Bearer secret-token",
	})

	if !success || retry {
		t.Fatalf("expected success without retry, got success=%v retry=%v", success, retry)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("expected Authorization %q, got %q", "Bearer secret-token", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", gotContentType)
	}
	if gotBody != `{"event":"messages.upsert"}` {
		t.Errorf("unexpected body: %q", gotBody)
	}
}

// TestDoEmitWithoutHeaders confirma que a ausência de headers não quebra o envio.
func TestDoEmitWithoutHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Whatsmiau{httpClient: srv.Client()}
	success, retry := s.doEmit([]byte(`{}`), srv.URL, nil)
	if !success || retry {
		t.Fatalf("expected success without retry, got success=%v retry=%v", success, retry)
	}
}

// TestDoEmit4xxDoesNotRetry garante que erros de cliente (4xx) não sejam
// reprocessados, enquanto 5xx sinaliza retry.
func TestDoEmitRetrySemantics(t *testing.T) {
	cases := []struct {
		status      int
		wantSuccess bool
		wantRetry   bool
	}{
		{http.StatusOK, true, false},
		{http.StatusNoContent, true, false},
		{http.StatusUnauthorized, false, false},
		{http.StatusBadRequest, false, false},
		{http.StatusInternalServerError, false, true},
		{http.StatusBadGateway, false, true},
	}

	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(tc.status)
		}))

		s := &Whatsmiau{httpClient: srv.Client()}
		success, retry := s.doEmit([]byte(`{}`), srv.URL, nil)
		if success != tc.wantSuccess || retry != tc.wantRetry {
			t.Errorf("status %d: got success=%v retry=%v, want success=%v retry=%v",
				tc.status, success, retry, tc.wantSuccess, tc.wantRetry)
		}
		srv.Close()
	}
}
