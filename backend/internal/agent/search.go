package agent

import (
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

type Search struct{ Builder Builder }

func (s Search) Find(ctx context.Context, user string, query safety.Text) (Context, error) {
	frame, err := s.Builder.Build(ctx, user)
	if err != nil {
		return Context{}, err
	}
	words := strings.FieldsFunc(strings.ToLower(query.String()), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' })
	normalized := []string{}
	for _, word := range words {
		switch word {
		case "как", "где", "у", "нас", "что", "такое", "почитать", "найти", "мне", "инструкция", "инструкцию", "к", "по", "с", "команда", "команды", "команде":
			continue
		}
		switch {
		case strings.HasPrefix(word, "трекер") || word == "джира":
			word = "трекер"
		case strings.HasPrefix(word, "код") || strings.HasPrefix(word, "пиш"):
			word = "код"
		case strings.HasPrefix(word, "компонент"):
			word = "компонент"
		case strings.HasPrefix(word, "отгул"):
			word = "отгул"
		case strings.HasPrefix(word, "доступ"):
			word = "доступ"
		}
		normalized = append(normalized, word)
	}
	type match struct {
		document demo.Document
		score    int
	}
	matches := []match{}
	for _, d := range frame.Documents {
		var fragments []Fragment
		_ = json.Unmarshal(d.Fragments, &fragments)
		haystack := strings.ToLower(d.ID + " " + d.Title)
		for _, f := range fragments {
			haystack += " " + strings.ToLower(f.Text)
		}
		score := 0
		for _, word := range normalized {
			if strings.Contains(haystack, word) || word == "код" && strings.Contains(haystack, "разработ") {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, match{d, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	frame.Documents = []demo.Document{}
	for i, m := range matches {
		if i == 3 {
			break
		}
		frame.Documents = append(frame.Documents, m.document)
	}
	return frame, nil
}
