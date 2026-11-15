package safety

// Тест-доказательство: обход локального очистителя чувствительного ввода.
// Кладётся во временную копию: backend/platform/safety/secreview_safety_test.go.

import (
	"context"
	"strings"
	"testing"
)

func TestSecReviewSanitizerBypass(t *testing.T) {
	cases := []struct{ name, input, marker string }{
		{"control-plain", "Нужен отгул, у меня диагноз ангина", "ангина"},
		{"zero-width-joiner", "Нужен отгул, у меня диа‍гноз ангина", "ангина"},
		{"soft-hyphen", "Нужен отгул, у меня диаг­ноз ангина", "ангина"},
		{"latin-homoglyph", "Нужен отгул, у меня диагнoз ангина", "ангина"}, // латинская o
		{"spaced-letters", "Нужен отгул, у меня д и а г н о з ангина", "ангина"},
		{"card-dots", "Номер карты 4111.1111.1111.1111", "4111.1111"},
		{"passport-homoglyph", "Мой пaспорт 4510 123456", "4510 123456"}, // латинская a
	}
	failed := []string{}
	for _, c := range cases {
		out := Prepare(context.Background(), Local{}, c.input)
		leaked := !out.UseForm() && strings.Contains(out.String(), c.marker)
		t.Logf("%-20s form=%v leaked=%v output=%q", c.name, out.UseForm(), leaked, out.String())
		if c.name != "control-plain" && leaked {
			failed = append(failed, c.name)
		}
		if c.name == "control-plain" && leaked {
			t.Fatal("control case must be closed")
		}
	}
	if len(failed) > 0 {
		t.Fatalf("sensitive text passes the cleaner unchanged: %v", failed)
	}
}
