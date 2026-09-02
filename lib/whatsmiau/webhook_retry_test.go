package whatsmiau

import (
	"testing"
	"time"
)

func TestWebhookRetryBackoffGrowsAndCaps(t *testing.T) {
	// A primeira reentrega tem de ser rápida: o caso comum é um restart do
	// consumidor, que dura dezenas de segundos, não minutos.
	if got := webhookRetryBackoff(1); got != 15*time.Second {
		t.Fatalf("first retry = %v, want 15s", got)
	}
	if got := webhookRetryBackoff(2); got != 30*time.Second {
		t.Fatalf("second retry = %v, want 30s", got)
	}

	prev := webhookRetryBackoff(1)
	for attempt := 2; attempt <= webhookRetryMaxAttempts; attempt++ {
		got := webhookRetryBackoff(attempt)
		if got < prev {
			t.Fatalf("backoff shrank at attempt %d: %v after %v", attempt, got, prev)
		}
		if got > webhookRetryMaxBackoff {
			t.Fatalf("backoff at attempt %d = %v, above cap %v", attempt, got, webhookRetryMaxBackoff)
		}
		prev = got
	}

	if prev != webhookRetryMaxBackoff {
		t.Fatalf("backoff never reached the cap: last = %v, cap = %v", prev, webhookRetryMaxBackoff)
	}
}

func TestWebhookRetryWindowCoversLongConsumerOutage(t *testing.T) {
	// O ponto da fila é sobreviver a uma queda do consumidor. Se a soma das
	// esperas não cobrir uma janela larga, o evento é descartado do mesmo jeito
	// — só mais tarde.
	var total time.Duration
	for attempt := 1; attempt <= webhookRetryMaxAttempts; attempt++ {
		total += webhookRetryBackoff(attempt)
	}
	if total < time.Hour {
		t.Fatalf("retry window = %v, want at least 1h", total)
	}
}
