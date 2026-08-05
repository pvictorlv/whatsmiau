package whatsmiau

import (
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"golang.org/x/net/context"
	"google.golang.org/protobuf/proto"
)

// UpdateBlockStatus bloqueia ou desbloqueia um contato (compatível com
// /chat/updateBlockStatus da Evolution API).
func (s *Whatsmiau) UpdateBlockStatus(ctx context.Context, instanceID string, jid types.JID, block bool) error {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return whatsmeow.ErrClientIsNil
	}

	action := events.BlocklistChangeActionUnblock
	if block {
		action = events.BlocklistChangeActionBlock
	}

	_, err := client.UpdateBlocklist(ctx, jid, action)
	return err
}

// SetPresence define a presença global da instância (available/unavailable),
// equivalente ao /instance/setPresence da Evolution API.
func (s *Whatsmiau) SetPresence(ctx context.Context, instanceID string, available bool) error {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return whatsmeow.ErrClientIsNil
	}

	state := types.PresenceUnavailable
	if available {
		state = types.PresenceAvailable
	}

	return client.SendPresence(ctx, state)
}

// EditMessage edita o texto de uma mensagem já enviada (compatível com
// /chat/updateMessage da Evolution API).
func (s *Whatsmiau) EditMessage(ctx context.Context, instanceID string, chat types.JID, messageID, newText string) (whatsmeow.SendResponse, error) {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return whatsmeow.SendResponse{}, whatsmeow.ErrClientIsNil
	}

	resolved := s.resolveJID(ctx, client, chat)
	edited := client.BuildEdit(resolved, types.MessageID(messageID), &waE2E.Message{
		Conversation: proto.String(newText),
	})

	return client.SendMessage(ctx, resolved, edited)
}

// ProfileInfo espelha o retorno do /chat/fetchProfile da Evolution API.
type ProfileInfo struct {
	Wuid         string `json:"wuid,omitempty"`
	Name         string `json:"name,omitempty"`
	NumberExists bool   `json:"numberExists"`
	Picture      string `json:"picture,omitempty"`
	Status       string `json:"status,omitempty"`
	IsBusiness   bool   `json:"isBusiness"`
	Email        string `json:"email,omitempty"`
	Description  string `json:"description,omitempty"`
	Website      string `json:"website,omitempty"`
}

// FetchProfile reúne nome, foto, status e dados de negócio de um contato.
func (s *Whatsmiau) FetchProfile(ctx context.Context, instanceID string, jid types.JID) (*ProfileInfo, error) {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return nil, whatsmeow.ErrClientIsNil
	}

	target := jid.ToNonAD()
	info := &ProfileInfo{Wuid: target.String()}

	if resp, err := client.IsOnWhatsApp(ctx, []string{jid.User}); err == nil {
		for _, item := range resp {
			if item.IsIn {
				info.NumberExists = true
				break
			}
		}
	}

	if client.Store != nil && client.Store.Contacts != nil {
		if contact, err := client.Store.Contacts.GetContact(ctx, target); err == nil && contact.Found {
			switch {
			case contact.FullName != "":
				info.Name = contact.FullName
			case contact.PushName != "":
				info.Name = contact.PushName
			case contact.BusinessName != "":
				info.Name = contact.BusinessName
			}
		}
	}

	if users, err := client.GetUserInfo(ctx, []types.JID{target}); err == nil {
		if u, exists := users[target]; exists {
			info.Status = u.Status
			if info.Name == "" && u.VerifiedName != nil && u.VerifiedName.Details != nil {
				info.Name = u.VerifiedName.Details.GetVerifiedName()
			}
		}
	}

	if pic, err := client.GetProfilePictureInfo(ctx, target, &whatsmeow.GetProfilePictureParams{Preview: false}); err == nil && pic != nil {
		info.Picture = pic.URL
	}

	if business, err := client.GetBusinessProfile(ctx, target); err == nil && business != nil {
		info.IsBusiness = true
		info.Email = business.Email
	}

	return info, nil
}

// FetchProfilePictureURL retorna a URL da foto de perfil (full-res) de um contato.
func (s *Whatsmiau) FetchProfilePictureURL(ctx context.Context, instanceID string, jid types.JID) (string, error) {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return "", whatsmeow.ErrClientIsNil
	}

	pic, err := client.GetProfilePictureInfo(ctx, jid.ToNonAD(), &whatsmeow.GetProfilePictureParams{Preview: false})
	if err != nil {
		return "", err
	}
	if pic == nil {
		return "", nil
	}

	return pic.URL, nil
}

// ErrDeviceNotConnected sinaliza que a instância existe mas não tem sessão
// pareada — condição do chamador, não falha de servidor.
var ErrDeviceNotConnected = errors.New("device is not connected")

// FetchMessageHistory dispara um pedido de sincronização de histórico sob demanda
// ao WhatsApp. O resultado chega de forma assíncrona pelo evento HistorySync e é
// entregue ao CRM via webhook messages.set (paridade com /chat/fetchMessageHistory).
func (s *Whatsmiau) FetchMessageHistory(ctx context.Context, instanceID string, chat types.JID, count int, timestamp int64, messageID string, fromMe bool) error {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return whatsmeow.ErrClientIsNil
	}
	if client.Store == nil || client.Store.ID == nil {
		return ErrDeviceNotConnected
	}

	if count <= 0 {
		count = 50
	}

	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			IsFromMe: fromMe,
		},
		ID: types.MessageID(messageID),
	}
	if timestamp > 0 {
		info.Timestamp = time.Unix(timestamp, 0)
	}

	req := client.BuildHistorySyncRequest(info, count)
	_, err := client.SendMessage(ctx, client.Store.ID.ToNonAD(), req)
	return err
}

// DownloadMediaWithPath baixa e descriptografa uma mídia a partir dos metadados
// (directPath + chaves), reconstruindo o binário sem depender do protobuf
// original. Usado pelo /chat/getBase64FromMediaMessage.
func (s *Whatsmiau) DownloadMediaWithPath(ctx context.Context, instanceID, directPath string, encHash, fileHash, mediaKey []byte, mediaType whatsmeow.MediaType, mmsType string) ([]byte, error) {
	client, ok := s.clients.Load(instanceID)
	if !ok {
		return nil, whatsmeow.ErrClientIsNil
	}

	return client.DownloadMediaWithPath(ctx, directPath, encHash, fileHash, mediaKey, mediaType, mmsType, true)
}
