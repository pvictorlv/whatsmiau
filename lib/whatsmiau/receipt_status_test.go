package whatsmiau

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Regression: um receipt com IsFromMe é da própria conta — inclusive o "chat
// lido" que o WhatsApp gera no instante do envio. Emiti-lo fazia o CRM marcar a
// mensagem recém-enviada como LIDA em menos de um segundo, com o destinatário
// nunca tendo aberto a conversa.
func TestReceiptStatus(t *testing.T) {
	tests := []struct {
		name       string
		receipt    events.Receipt
		wantStatus WookMessageUpdateStatus
		wantEmit   bool
	}{
		{
			name:       "recipient read our message",
			receipt:    events.Receipt{Type: types.ReceiptTypeRead},
			wantStatus: MessageStatusRead,
			wantEmit:   true,
		},
		{
			name:       "recipient device got our message",
			receipt:    events.Receipt{Type: types.ReceiptTypeDelivered},
			wantStatus: MessageStatusDeliveryAck,
			wantEmit:   true,
		},
		{
			name:     "our own read receipt never acks a sent message",
			receipt:  events.Receipt{Type: types.ReceiptTypeRead, MessageSource: types.MessageSource{IsFromMe: true}},
			wantEmit: false,
		},
		{
			name:     "our own delivery receipt is not the recipient's",
			receipt:  events.Receipt{Type: types.ReceiptTypeDelivered, MessageSource: types.MessageSource{IsFromMe: true}},
			wantEmit: false,
		},
		{
			name:     "read-self is our own read from another device",
			receipt:  events.Receipt{Type: types.ReceiptTypeReadSelf},
			wantEmit: false,
		},
		{
			name:     "sender receipt is delivery to our own devices",
			receipt:  events.Receipt{Type: types.ReceiptTypeSender},
			wantEmit: false,
		},
		{
			name:     "played is not mapped",
			receipt:  events.Receipt{Type: types.ReceiptTypePlayed},
			wantEmit: false,
		},
		{
			name:     "retry is not an ack",
			receipt:  events.Receipt{Type: types.ReceiptTypeRetry},
			wantEmit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := tt.receipt
			status, emit := receiptStatus(&evt)

			if emit != tt.wantEmit {
				t.Fatalf("emit = %v, want %v", emit, tt.wantEmit)
			}
			if emit && status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", status, tt.wantStatus)
			}
		})
	}
}

// ReceiptTypeDelivered é a string vazia, que também é o zero value do tipo. Este
// teste trava o fato: um receipt sem tipo preenchido é indistinguível de
// "entregue", então qualquer mudança nesse switch precisa ser deliberada.
func TestReceiptDeliveredIsTheZeroValue(t *testing.T) {
	if types.ReceiptTypeDelivered != "" {
		t.Fatalf("ReceiptTypeDelivered mudou de valor: %q", types.ReceiptTypeDelivered)
	}

	var untyped events.Receipt
	status, emit := receiptStatus(&untyped)

	if !emit || status != MessageStatusDeliveryAck {
		t.Fatalf("receipt sem tipo = (%q, %v), want (%q, true)", status, emit, MessageStatusDeliveryAck)
	}
}
