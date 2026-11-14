package agent

import (
	"ai-buddy/backend/internal/actions"
	"encoding/json"
)

// AuthorizedHistory prevents persisted answers from bypassing current document
// and resource permissions after a profile or policy change.
func AuthorizedHistory(frame Context, messages []actions.Message) []actions.Message {
	documents := map[string]bool{}
	resources := map[string]bool{}
	for _, d := range frame.Documents {
		documents[d.ID] = true
	}
	for _, r := range frame.Resources {
		resources[r.ID] = true
	}
	result := []actions.Message{}
	for _, message := range messages {
		if message.UserID != frame.Profile.ID {
			continue
		}
		if len(message.Metadata) > 0 && string(message.Metadata) != "null" {
			var metadata struct {
				Items []struct {
					Answer    *Answer       `json:"answer"`
					Card      *actions.Card `json:"card"`
					Resources []struct {
						ID string `json:"id"`
					} `json:"resources"`
				} `json:"items"`
			}
			allowed := json.Unmarshal(message.Metadata, &metadata) == nil
			for _, item := range metadata.Items {
				if item.Answer != nil {
					for _, source := range item.Answer.Sources {
						if !documents[source.ID] {
							allowed = false
						}
					}
				}
				if item.Card != nil {
					var params struct {
						Resource string `json:"resource_id"`
					}
					if json.Unmarshal(item.Card.Parameters, &params) != nil || params.Resource != "" && !resources[params.Resource] {
						allowed = false
					}
				}
				for _, r := range item.Resources {
					if !resources[r.ID] {
						allowed = false
					}
				}
			}
			if !allowed {
				message.Text = "Прежний ответ скрыт: актуальные права на его источники не подтверждены."
				message.Metadata = nil
			}
		}
		result = append(result, message)
	}
	return result
}
