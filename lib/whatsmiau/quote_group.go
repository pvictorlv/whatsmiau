package whatsmiau

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"
)

// quoteForChat ajusta o autor da citação ao endereçamento do chat.
//
// Os eventos que o whatsmiau entrega trazem o autor como telefone (o LID é
// traduzido na entrada), então a key guardada pelo consumidor carrega o
// telefone. Num grupo de endereçamento LID o celular identifica o autor pelo
// LID: com o telefone no ContextInfo.Participant ele não acha a mensagem
// citada e mostra a resposta solta, sem a marcação (o WhatsApp Web, que lê o
// texto citado, não sofre disso).
//
// Qualquer dúvida (chat que não é grupo LID, autor já em LID, sem mapeamento)
// devolve a citação como veio: citar com o telefone continua valendo para
// quem não depende do LID.
func (s *Whatsmiau) quoteForChat(ctx context.Context, client *whatsmeow.Client, chat types.JID, q *Quote) *Quote {
	if q == nil || q.Participant == nil || chat.Server != types.GroupServer || q.Participant.Server != types.DefaultUserServer {
		return q
	}
	if client == nil || client.Store == nil || client.Store.LIDs == nil {
		return q
	}

	info, err := client.GetGroupInfo(ctx, chat)
	if err != nil {
		zap.L().Warn("could not read group addressing mode, quoting with the original author",
			zap.String("group", chat.String()), zap.Error(err))
		return q
	}
	if !usesLIDAddressing(info) {
		return q
	}

	lid, err := client.Store.LIDs.GetLIDForPN(ctx, *q.Participant)
	if err != nil || lid.IsEmpty() {
		zap.L().Warn("no LID for the quoted author, quoting with the phone number",
			zap.String("group", chat.String()), zap.String("author", q.Participant.String()), zap.Error(err))
		return q
	}

	lid = lid.ToNonAD()
	localized := *q
	localized.Participant = &lid
	return &localized
}

func usesLIDAddressing(info *types.GroupInfo) bool {
	return info != nil && info.AddressingMode == types.AddressingModeLID
}
