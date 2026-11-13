package agent

import (
	"ai-buddy/backend/platform/demo"
	"encoding/json"
	"strings"
)

type Fragment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Documents are untrusted evidence. Demonstration instructions directed at the
// agent are withheld from generation, while authorized document links remain.
func safeDocument(document demo.Document) demo.Document {
	fragments := []Fragment{}
	if !document.Stale {
		var source []Fragment
		if decode(document.Fragments, &source) {
			for _, f := range source {
				if f.ID == "" || f.Text == "" {
					continue
				}
				text := strings.ToLower(f.Text)
				if strings.Contains(text, "агент") && (strings.Contains(text, "выдай") || strings.Contains(text, "игнорируй") || strings.Contains(text, "покажи")) {
					continue
				}
				fragments = append(fragments, f)
			}
		}
	}
	document.Fragments, _ = json.Marshal(fragments)
	return document
}
