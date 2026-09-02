package whatsmiau

import (
	"encoding/json"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/verbeux-ai/whatsmiau/services"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

// Metadado de grupo muda pouco e custa uma IQ ao servidor do WhatsApp a cada
// chamada. O CRM pede o grupo na PRIMEIRA mensagem de cada grupo e descarta a
// mensagem quando a busca falha ("Could not find group infos" ->
// wbotMessageListener retorna null), então rate limit, timeout ou socket
// reconectando viram buraco no histórico do grupo.
//
// A Evolution nunca sofreu disso porque o Baileys resolve o metadado por um
// cache (`cachedGroupMetadata`, TTL de 1h, revalidação assíncrona). Isto aqui é
// a mesma ideia, com uma garantia a mais: valor vencido continua servindo
// quando a revalidação falha — nome de grupo velho é infinitamente melhor do
// que mensagem perdida.
const (
	// Abaixo disso o valor é servido direto, sem tocar no servidor.
	groupInfoFreshFor = time.Hour
	// Até aqui o valor sobrevive no Redis só para ser a rede de proteção da
	// revalidação que falhou.
	groupInfoRetention = 7 * 24 * time.Hour
)

type groupInfoCacheEntry struct {
	CachedAt time.Time          `json:"cachedAt"`
	Data     *GroupInfoResponse `json:"data"`
}

func (e *groupInfoCacheEntry) fresh() bool {
	return e != nil && e.Data != nil && time.Since(e.CachedAt) < groupInfoFreshFor
}

func groupInfoCacheKey(instanceID string, jid types.JID) string {
	return "whatsmiau:group:info:" + instanceID + ":" + jid.ToNonAD().String()
}

// loadGroupInfoCache devolve nil quando não há nada utilizável. Falha de Redis
// nunca é fatal aqui: o caminho sem cache continua sendo a busca ao vivo.
func loadGroupInfoCache(ctx context.Context, instanceID string, jid types.JID) *groupInfoCacheEntry {
	raw, err := services.Redis().Get(ctx, groupInfoCacheKey(instanceID, jid)).Bytes()
	if err != nil {
		if err != redis.Nil {
			zap.L().Warn("failed to read group info cache",
				zap.Error(err), zap.String("instance", instanceID), zap.String("group", jid.String()))
		}
		return nil
	}

	var entry groupInfoCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil || entry.Data == nil {
		return nil
	}
	return &entry
}

func storeGroupInfoCache(ctx context.Context, instanceID string, jid types.JID, data *GroupInfoResponse) {
	if data == nil {
		return
	}

	raw, err := json.Marshal(groupInfoCacheEntry{CachedAt: time.Now(), Data: data})
	if err != nil {
		zap.L().Warn("failed to marshal group info cache", zap.Error(err))
		return
	}

	if err := services.Redis().Set(ctx, groupInfoCacheKey(instanceID, jid), raw, groupInfoRetention).Err(); err != nil {
		zap.L().Warn("failed to write group info cache",
			zap.Error(err), zap.String("instance", instanceID), zap.String("group", jid.String()))
	}
}

// invalidateGroupInfoCache é chamada quando o próprio WhatsApp avisa que o
// grupo mudou (nome, tópico, participantes, entrada/saída). Apagar em vez de
// reescrever mantém uma única fonte de verdade: a próxima leitura revalida.
func invalidateGroupInfoCache(instanceID string, jid types.JID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := services.Redis().Del(ctx, groupInfoCacheKey(instanceID, jid)).Err(); err != nil && err != redis.Nil {
		zap.L().Warn("failed to invalidate group info cache",
			zap.Error(err), zap.String("instance", instanceID), zap.String("group", jid.String()))
	}
}
