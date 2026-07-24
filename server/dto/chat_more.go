package dto

// UpdateBlockStatusRequest espelha o corpo do /chat/updateBlockStatus da Evolution.
type UpdateBlockStatusRequest struct {
	InstanceID string `param:"instance" validate:"required"`
	Number     string `json:"number" validate:"required"`
	Status     string `json:"status" validate:"required,oneof=block unblock"`
}

// FetchProfileRequest espelha o corpo do /chat/fetchProfile e
// /chat/fetchProfilePictureUrl da Evolution.
type FetchProfileRequest struct {
	InstanceID string `param:"instance" validate:"required"`
	Number     string `json:"number" validate:"required"`
}

// FetchProfilePictureUrlResponse é o retorno do /chat/fetchProfilePictureUrl.
// O CRM lê o campo profilePictureUrl.
type FetchProfilePictureUrlResponse struct {
	Wuid              string `json:"wuid,omitempty"`
	ProfilePictureUrl string `json:"profilePictureUrl,omitempty"`
}

// UpdateMessageRequest espelha o corpo do /chat/updateMessage (edição) da Evolution.
type UpdateMessageRequest struct {
	InstanceID string             `param:"instance" validate:"required"`
	Number     string             `json:"number" validate:"required"`
	Text       string             `json:"text" validate:"required"`
	Key        UpdateMessageKey   `json:"key" validate:"required"`
}

type UpdateMessageKey struct {
	Id        string `json:"id" validate:"required"`
	FromMe    bool   `json:"fromMe"`
	RemoteJid string `json:"remoteJid,omitempty"`
}

// SetPresenceRequest espelha o corpo do /instance/setPresence da Evolution.
type SetPresenceRequest struct {
	InstanceID string `param:"instance" validate:"required"`
	Presence   string `json:"presence" validate:"required,oneof=available unavailable"`
}

// GetBase64Request espelha o corpo do /chat/getBase64FromMediaMessage. O CRM
// envia de volta a mensagem recebida no webhook para recuperar a mídia sob demanda.
type GetBase64Request struct {
	InstanceID   string           `param:"instance" validate:"required"`
	ConvertToMp4 bool             `json:"convertToMp4,omitempty"`
	Message      GetBase64Message `json:"message" validate:"required"`
}

// GetBase64Message captura os blocos de mídia possíveis (mesmos json tags que o
// whatsmiau emite no webhook), com os metadados necessários para o download.
type GetBase64Message struct {
	ImageMessage    *MediaLocator `json:"imageMessage,omitempty"`
	AudioMessage    *MediaLocator `json:"audioMessage,omitempty"`
	VideoMessage    *MediaLocator `json:"videoMessage,omitempty"`
	DocumentMessage *MediaLocator `json:"documentMessage,omitempty"`
	StickerMessage  *MediaLocator `json:"stickerMessage,omitempty"`
	PtvMessage      *MediaLocator `json:"ptvMessage,omitempty"`
}

// MediaLocator carrega os metadados de mídia (base64 nas chaves, igual à emissão
// do whatsmiau) necessários para reconstruir e baixar o conteúdo.
type MediaLocator struct {
	Url           string `json:"url,omitempty"`
	DirectPath    string `json:"directPath,omitempty"`
	MediaKey      string `json:"mediaKey,omitempty"`
	FileEncSha256 string `json:"fileEncSha256,omitempty"`
	FileSha256    string `json:"fileSha256,omitempty"`
	Mimetype      string `json:"mimetype,omitempty"`
}

// GetBase64Response é o retorno do /chat/getBase64FromMediaMessage. O CRM lê base64.
type GetBase64Response struct {
	Base64   string `json:"base64,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
}

// QrcodeResponse é o retorno do alias /instance/qrcode (compat Evolution).
type QrcodeResponse struct {
	Connected   bool         `json:"connected"`
	Code        string       `json:"code,omitempty"`
	Base64      string       `json:"base64,omitempty"`
	PairingCode string       `json:"pairingCode,omitempty"`
	Qrcode      *QrcodeInner `json:"qrcode,omitempty"`
}

type QrcodeInner struct {
	Instance    string `json:"instance,omitempty"`
	Code        string `json:"code,omitempty"`
	Base64      string `json:"base64,omitempty"`
	PairingCode string `json:"pairingCode,omitempty"`
}

// FetchMessageHistoryRequest espelha o corpo do /chat/fetchMessageHistory da Evolution.
type FetchMessageHistoryRequest struct {
	InstanceID string `param:"instance" validate:"required"`
	RemoteJid  string `json:"remoteJid" validate:"required"`
	Count      int    `json:"count,omitempty"`
	Timestamp  int64  `json:"timestamp,omitempty"`
	MessageId  string `json:"messageId,omitempty"`
	FromMe     bool   `json:"fromMe,omitempty"`
}

// FindMessagesResponse espelha o retorno paginado do /chat/findMessages da Evolution.
// O whatsmiau não persiste mensagens; o histórico é entregue via push (messages.set),
// portanto o pull retorna vazio de forma graciosa.
type FindMessagesResponse struct {
	Messages FindMessagesPage `json:"messages"`
}

type FindMessagesPage struct {
	Total       int   `json:"total"`
	Pages       int   `json:"pages"`
	CurrentPage int   `json:"currentPage"`
	Records     []any `json:"records"`
}
