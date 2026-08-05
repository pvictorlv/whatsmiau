package models

import (
	"encoding/json"
	"testing"
)

func TestFullHistoryEnabledDefaultsToOn(t *testing.T) {
	// Instância gravada antes do campo existir: o JSON no Redis não tem
	// syncFullHistory. Precisa contar como ligado, senão essas conexões ficam
	// sem histórico para sempre — não há rota que "religue" o passado.
	var legacy Instance
	if err := json.Unmarshal([]byte(`{"id":"wpp_1"}`), &legacy); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !legacy.FullHistoryEnabled() {
		t.Error("instance without syncFullHistory should default to enabled")
	}
}

func TestFullHistoryEnabledRespectsExplicitValues(t *testing.T) {
	cases := map[string]bool{
		`{"id":"wpp_1","syncFullHistory":true}`:  true,
		`{"id":"wpp_1","syncFullHistory":false}`: false,
	}

	for payload, expected := range cases {
		var instance Instance
		if err := json.Unmarshal([]byte(payload), &instance); err != nil {
			t.Fatalf("unmarshal %s failed: %v", payload, err)
		}
		if got := instance.FullHistoryEnabled(); got != expected {
			t.Errorf("%s: FullHistoryEnabled() = %v, want %v", payload, got, expected)
		}
	}
}

func TestFullHistoryEnabledOnNilInstance(t *testing.T) {
	var instance *Instance
	if instance.FullHistoryEnabled() {
		t.Error("nil instance should not report full history enabled")
	}
}

func TestExplicitFalseSurvivesRoundTrip(t *testing.T) {
	// `omitempty` num *bool só omite nil, então o false explícito de quem
	// desligou de propósito não pode ser apagado ao regravar no Redis.
	disabled := false
	instance := Instance{ID: "wpp_1", SyncFullHistory: &disabled}

	data, err := json.Marshal(&instance)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored Instance
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.FullHistoryEnabled() {
		t.Errorf("explicit false was lost on round trip: %s", data)
	}
}
