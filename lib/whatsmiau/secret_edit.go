package whatsmiau

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// unwrapSecretEdit devolve o evento com a edição já decifrada quando ela chegou
// como secretEncryptedMessage (MESSAGE_EDIT).
//
// Edições feitas no iOS e em grupos não vêm como o ProtocolMessage em claro que
// o Android manda em conversa direta: vêm cifradas com o segredo da mensagem
// original. Só quem guardou esse segredo (o store do whatsmeow, na hora em que a
// mensagem original passou por aqui) consegue abrir; o consumidor não tem como.
// Sem esta etapa a edição chegava como um envelope opaco e sumia sem aviso.
//
// Qualquer outro evento, ou uma edição que não foi possível decifrar, volta
// intacto: o consumidor ainda a registra como edição não decifrável.
func (s *Whatsmiau) unwrapSecretEdit(id string, e *events.Message) *events.Message {
	enc := e.Message.GetSecretEncryptedMessage()
	if enc == nil || enc.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
		return e
	}

	client, ok := s.clients.Load(id)
	if !ok {
		return e
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	decrypted, err := client.DecryptSecretEncryptedMessage(ctx, e)
	if err != nil {
		zap.L().Warn("could not decrypt secret message edit",
			zap.String("instance", id),
			zap.String("message_id", e.Info.ID),
			zap.String("target_id", enc.GetTargetMessageKey().GetID()),
			zap.String("chat", e.Info.Chat.String()),
			zap.Error(err))
		return e
	}

	edit := secretEditAsProtocolMessage(enc.GetTargetMessageKey(), decrypted, e.Info.Timestamp)
	if edit == nil {
		return e
	}

	unwrapped := *e
	unwrapped.RawMessage = edit
	return unwrapped.UnwrapRaw()
}

// secretEditAsProtocolMessage reescreve o conteúdo decifrado no formato que o
// consumidor já entende: protocolMessage.editedMessage com a key da mensagem
// alvo. O conteúdo decifrado pode vir já embrulhado num ProtocolMessage ou ser
// o próprio texto novo; os dois casos viram o mesmo resultado.
func secretEditAsProtocolMessage(target *waCommon.MessageKey, decrypted *waE2E.Message, at time.Time) *waE2E.Message {
	content := decrypted
	if pm := decrypted.GetProtocolMessage(); pm.GetEditedMessage() != nil {
		content = pm.GetEditedMessage()
		if target == nil {
			target = pm.GetKey()
		}
	}

	if target == nil || target.GetID() == "" || content == nil {
		return nil
	}

	return &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Key:           target,
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			EditedMessage: content,
			TimestampMS:   proto.Int64(at.UnixMilli()),
		},
	}
}
