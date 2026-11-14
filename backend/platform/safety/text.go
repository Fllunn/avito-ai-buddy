// Package safety keeps raw user text outside persistence and provider boundaries.
package safety

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Hidden = "[чувствительное сообщение скрыто]"
const Redacted = "[скрыто]"

type Text struct {
	value string
	form  bool
}

func (t Text) String() string { return t.value }
func (t Text) UseForm() bool  { return t.form }

type Cleaner interface {
	Clean(context.Context, string) (string, bool, error)
}
type Local struct{}

var secrets = regexp.MustCompile(`(?i)(?:\b(?:sk-[a-z0-9_-]{8,}|gh[pousr]_[a-z0-9_]{8,})\b|(?:bearer|api[_ -]?key|token|токен|пароль|password|secret)\s*[:=]?\s*[^\s,;]+)`)
var numbers = regexp.MustCompile(`\b(?:\d[ -]?){12,19}\b`)
var suspicion = regexp.MustCompile(`(?i)(диагноз|боле[юет]|забол|грипп|ковид|covid|онколог|депресси|беремен|лечени|медицин|больниц|болезн|здоровь|температур|врач|поликлиник|симптом|паспорт|снилс|документ.{0,20}номер|банковск|кредитн|семейн|реб[её]нок|реб[её]нк|сын(?:а|у|ом)?(?:$|[^\p{L}])|доч[ьи]|мам[аыу]|пап[аыу]|родител|супруг|развод|умер|инсулин|диабет)`)

func (Local) Clean(ctx context.Context, raw string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", true, err
	}
	if !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > 4000 {
		return Hidden, true, nil
	}
	// Medical and family narrative has no reliable clause boundaries. The entire
	// message is closed rather than sending the remainder to a model for cleanup.
	if suspicion.MatchString(raw) {
		return Hidden, true, nil
	}
	value := secrets.ReplaceAllString(raw, Redacted)
	value = numbers.ReplaceAllString(value, Redacted)
	if strings.ContainsAny(value, "\x00") {
		return Hidden, true, nil
	}
	return strings.TrimSpace(value), false, nil
}

// Prepare is the only constructor of persistable text. A failed cleaner never
// exposes either its diagnostic or the original input to downstream consumers.
func Prepare(ctx context.Context, cleaner Cleaner, raw string) (result Text) {
	result = Text{Hidden, true}
	defer func() {
		if recover() != nil {
			result = Text{Hidden, true}
		}
	}()
	value, form, err := cleaner.Clean(ctx, raw)
	if err != nil || form {
		return result
	}
	return Text{value, false}
}
