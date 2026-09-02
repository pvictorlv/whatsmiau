package whatsmiau

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/verbeux-ai/whatsmiau/services"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

// Fila de reentrega de webhook.
//
// O emissor tentava 3 vezes com backoff de 1s e 2s — uma janela de ~3 segundos —
// e depois descartava o evento. Um restart do consumidor leva 10 a 30 segundos,
// então toda mensagem que chegasse nessa janela sumia em silêncio: existe no
// WhatsApp e nunca aparece no ticket. Não havia como reconstruir porque o
// whatsmiau é push-only: diferente da Evolution, ele não guarda mensagem para
// ser buscada depois.
//
// Aqui o evento que esgotou as tentativas imediatas vai para um ZSET no Redis
// (que já é dependência do serviço), ordenado pelo instante da próxima
// tentativa, e um laço de fundo o reentrega. Sobrevive a restart do consumidor
// e a restart do próprio whatsmiau.
const (
	webhookRetryKey = "whatsmiau:webhook:retry"

	// Cobre restart de consumidor, deploy e queda curta de rede. Passou disso,
	// o problema não é transporte e reter mais só atrasa o diagnóstico.
	webhookRetryMaxAttempts = 12

	// Teto do backoff exponencial entre reentregas.
	webhookRetryMaxBackoff = 10 * time.Minute

	// Intervalo do laço. Curto o bastante para que um restart de 30s custe uma
	// reentrega, não um minuto de atraso.
	webhookRetryTick = 5 * time.Second

	// Quantos itens vencidos são reclamados por rodada.
	webhookRetryBatch = 64

	// Teto de segurança: consumidor fora do ar por horas não pode encher o
	// Redis a ponto de derrubar o resto do serviço.
	webhookRetryMaxQueued = 20000
)

type webhookRetryItem struct {
	// ID torna o membro do ZSET único: dois eventos idênticos são duas
	// entregas, não uma.
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body"`
	Attempt int               `json:"attempt"`
	FirstAt time.Time         `json:"firstAt"`
}

func webhookRetryBackoff(attempt int) time.Duration {
	backoff := 15 * time.Second
	for i := 1; i < attempt && backoff < webhookRetryMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > webhookRetryMaxBackoff {
		backoff = webhookRetryMaxBackoff
	}
	return backoff
}

// enqueueWebhookRetry guarda o evento para reentrega. Falha de Redis aqui só
// pode ser logada: é o último recurso, não há para onde escalar.
func enqueueWebhookRetry(item webhookRetryItem) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rdb := services.Redis()

	if item.Attempt >= webhookRetryMaxAttempts {
		zap.L().Error("webhook permanently dropped after retry queue exhausted",
			zap.String("url", item.URL),
			zap.Int("attempts", item.Attempt),
			zap.Time("first_attempt", item.FirstAt))
		return
	}

	if size, err := rdb.ZCard(ctx, webhookRetryKey).Result(); err == nil && size >= webhookRetryMaxQueued {
		zap.L().Error("webhook retry queue is full, dropping event",
			zap.String("url", item.URL), zap.Int64("queued", size))
		return
	}

	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.FirstAt.IsZero() {
		item.FirstAt = time.Now()
	}
	item.Attempt++

	raw, err := json.Marshal(item)
	if err != nil {
		zap.L().Error("failed to marshal webhook retry item", zap.Error(err))
		return
	}

	nextAt := time.Now().Add(webhookRetryBackoff(item.Attempt))
	if err := rdb.ZAdd(ctx, webhookRetryKey, &redis.Z{
		Score:  float64(nextAt.Unix()),
		Member: raw,
	}).Err(); err != nil {
		zap.L().Error("failed to queue webhook for retry", zap.Error(err), zap.String("url", item.URL))
		return
	}

	zap.L().Warn("webhook queued for retry",
		zap.String("url", item.URL),
		zap.Int("attempt", item.Attempt),
		zap.Duration("in", time.Until(nextAt)))
}

func (s *Whatsmiau) startWebhookRetryLoop() {
	ticker := time.NewTicker(webhookRetryTick)
	defer ticker.Stop()

	for range ticker.C {
		s.drainWebhookRetryQueue()
	}
}

func (s *Whatsmiau) drainWebhookRetryQueue() {
	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("panic draining webhook retry queue", zap.Any("panic", r), zap.Stack("stack"))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	rdb := services.Redis()
	now := strconv.FormatInt(time.Now().Unix(), 10)
	members, err := rdb.ZRangeByScore(ctx, webhookRetryKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   now,
		Count: webhookRetryBatch,
	}).Result()
	if err != nil {
		if err != redis.Nil {
			zap.L().Error("failed to read webhook retry queue", zap.Error(err))
		}
		return
	}

	for _, member := range members {
		// Só quem consegue remover é dono da entrega. Duas instâncias do
		// whatsmiau sobre o mesmo Redis não reentregam o mesmo evento duas
		// vezes.
		removed, err := rdb.ZRem(ctx, webhookRetryKey, member).Result()
		if err != nil || removed == 0 {
			continue
		}

		var item webhookRetryItem
		if err := json.Unmarshal([]byte(member), &item); err != nil {
			zap.L().Error("discarding unreadable webhook retry item", zap.Error(err))
			continue
		}

		success, shouldRetry := s.doEmit(item.Body, item.URL, item.Headers)
		if success {
			zap.L().Info("webhook delivered from retry queue",
				zap.String("url", item.URL),
				zap.Int("attempt", item.Attempt),
				zap.Duration("delayed_by", time.Since(item.FirstAt)))
			continue
		}
		if !shouldRetry {
			// 4xx: o consumidor recusou o conteúdo, reentregar não muda nada.
			zap.L().Error("webhook rejected by consumer, dropping",
				zap.String("url", item.URL), zap.Int("attempt", item.Attempt))
			continue
		}

		enqueueWebhookRetry(item)
	}
}
