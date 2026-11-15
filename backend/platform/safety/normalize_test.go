package safety

import (
	"context"
	"strings"
	"testing"
)

// Regression for security review SR-03 (normalization scope agreed for stage 1).
func TestNormalizationClosesInvisibleSplits(t *testing.T) {
	for name, input := range map[string]string{
		"zero-width joiner":  "Нужен отгул, у меня диа\u200dгноз ангина",
		"zero-width space":   "Нужен отгул, у меня диа\u200bгноз ангина",
		"soft hyphen":        "Нужен отгул, у меня диаг\u00adноз ангина",
		"word joiner":        "Нужен отгул, у меня диаг\u2060ноз ангина",
		"byte order mark":    "Нужен отгул, у меня диаг\ufeffноз ангина",
		"invisible passport": "Мой пас\u200dпорт 4510 123456",
	} {
		t.Run(name, func(t *testing.T) {
			out := Prepare(context.Background(), Local{}, input)
			if !out.UseForm() || out.String() != Hidden {
				t.Fatalf("not closed: %q", out.String())
			}
		})
	}
}

func TestNormalizationMasksCardNumbers(t *testing.T) {
	for name, input := range map[string]string{
		"dots":       "Номер карты 4111.1111.1111.1111",
		"slashes":    "Номер карты 4111/1111/1111/1111",
		"spaces":     "Номер карты 4111 1111 1111 1111",
		"fullwidth":  "Номер карты ４１１１４１１１４１１１４１１１",
		"hyphens":    "Номер карты 4111-1111-1111-1111",
		"zero-width": "Номер карты 4111\u200b1111\u200b1111\u200b1111",
	} {
		t.Run(name, func(t *testing.T) {
			out := Prepare(context.Background(), Local{}, input)
			if out.UseForm() || strings.ContainsAny(strings.TrimPrefix(out.String(), "Номер карты "), "0123456789") || !strings.Contains(out.String(), Redacted) {
				t.Fatalf("number not masked: %q", out.String())
			}
		})
	}
}

func TestNormalizationKeepsOrdinaryText(t *testing.T) {
	for _, input := range []string{
		"Нужен отгул 16.11.2026",
		"Отгул с 16.11.2026 по 20.11.2026",
		"Отгул 16.11.2026-20.11.2026",
		"Отгул 2026-11-16",
		"Нужен доступ в space-payments на чтение",
		"Что с заявкой ACC-1001 и запросом ABS-2001?",
		"Версия 1.16.1, порт 127.0.0.1:8080",
		"Для задач стажировки в команде «Платежи»",
		"Ёлка и йогурт — обычные слова",
	} {
		out := Prepare(context.Background(), Local{}, input)
		if out.UseForm() || out.String() != input {
			t.Fatalf("ordinary text changed: %q -> %q (form=%v)", input, out.String(), out.UseForm())
		}
	}
}
