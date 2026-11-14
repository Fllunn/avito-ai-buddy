package safety

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type broken struct{ panic bool }

func (b broken) Clean(context.Context, string) (string, bool, error) {
	if b.panic {
		panic("raw secret")
	}
	return "raw secret", false, errors.New("raw secret")
}
func TestSensitiveCorpus(t *testing.T) {
	corpus := []string{
		"У меня температура, надо к врачу", "Отгул 13 ноября, запись в поликлинику", "Хочу отгул, симптомы не проходят",
		"Забрать сына из школы", "Нужно помочь сыну", "Останусь с сыном", "Со мной сын",
		"Хочу отгул 13 ноября, у меня грипп", "Отгул: диагноз диабет", "Нужен день, мама заболела", "Забрать ребёнка из школы", "У сына депрессия", "Семейные обстоятельства: развод", "Паспорт 1234 567890", "СНИЛС 123-456-789 00", "Номер банковской карты 4111 1111 1111 1111", "Я принимаю инсулин", "Супруга беременна", "\xff", strings.Repeat("я", 4001), "отгул\x00"}
	for _, raw := range corpus {
		value := Prepare(context.Background(), Local{}, raw)
		if value.String() != Hidden || !value.UseForm() {
			t.Fatalf("corpus case was not closed")
		}
		encoded, _ := json.Marshal(map[string]string{"message": value.String()})
		if strings.Contains(string(encoded), raw) {
			t.Fatal("raw message escaped")
		}
	}
}
func TestRedactionAndAllowedParameters(t *testing.T) {
	for _, raw := range []string{"Доступ к PAY, token: ghp_123456789abc", "token=synthetic-opaque-value", "TOKEN: synthetic-value", "API_KEY=abc-secret123", "токен: abc123secret", "Bearer abc-secret", "sk-example123456789", "4111 1111 1111 1111"} {
		safe := Prepare(context.Background(), Local{}, raw)
		if !strings.Contains(safe.String(), Redacted) || safe.String() == raw {
			t.Fatal("secret not removed")
		}
	}
	for _, raw := range []string{"Хочу отгул 13.11.2026", "Доступ к PAY для задач стажировки", "Как работает трекер?"} {
		safe := Prepare(context.Background(), Local{}, raw)
		if safe.String() != raw || safe.UseForm() {
			t.Fatal("safe input changed", safe)
		}
	}
}
func TestClosedFailureAndNoDiagnostic(t *testing.T) {
	for _, cleaner := range []Cleaner{broken{}, broken{true}, nil} {
		safe := Prepare(context.Background(), cleaner, "raw secret")
		if safe.String() != Hidden || !safe.UseForm() || strings.Contains(fmt.Sprint(safe), "raw secret") {
			t.Fatal("failure leaked raw input")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if safe := Prepare(ctx, Local{}, "raw secret"); safe.String() != Hidden || !safe.UseForm() {
		t.Fatal("cancelled clean did not close")
	}
}
