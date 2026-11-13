package actions

import (
	"ai-buddy/backend/platform/safety"
	"context"
	"regexp"
	"time"
)

func KnownIntent(name string) bool {
	switch name {
	case "next_step", "find_doc", "who_to_ask", "access_request", "time_off_info", "time_off_request", "status", "cancel", "mark_done", "card_edit", "card_confirm", "card_cancel", "out_of_scope", "smalltalk", "unclear":
		return true
	}
	return false
}

// Slots separates validated identifiers/dates from potentially sensitive text.
// Text fields can only be built through the local safety gate.
type Slots struct {
	ResourceID, Level, StartDate, EndDate, RequestID, StepID string
	Justification, Query                                     safety.Text
}

var slotID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,79}$`)

func (s Slots) Values() (map[string]string, error) {
	result := map[string]string{}
	for key, value := range map[string]string{"resource_id": s.ResourceID, "request_id": s.RequestID, "step_id": s.StepID} {
		if value != "" {
			safe := safety.Prepare(context.Background(), safety.Local{}, value)
			if safe.UseForm() || safe.String() != value {
				return nil, ErrInvalid
			}
			if !slotID.MatchString(value) {
				return nil, ErrInvalid
			}
			result[key] = value
		}
	}
	if s.Level != "" {
		if s.Level != "read" && s.Level != "edit" && s.Level != "write" {
			return nil, ErrInvalid
		}
		result["level"] = s.Level
	}
	for key, value := range map[string]string{"start_date": s.StartDate, "end_date": s.EndDate} {
		if value != "" {
			if _, err := time.Parse(time.DateOnly, value); err != nil {
				return nil, ErrInvalid
			}
			result[key] = value
		}
	}
	for key, value := range map[string]safety.Text{"justification": s.Justification, "query": s.Query} {
		if value.String() != "" {
			result[key] = value.String()
		}
	}
	return result, nil
}

func (s Slots) RequiresForm() bool { return s.Query.UseForm() || s.Justification.UseForm() }
