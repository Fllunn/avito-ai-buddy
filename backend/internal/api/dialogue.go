package api

import (
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/platform/calendar"
	"strings"
	"time"
	"unicode"
)

type DialogueState struct {
	Fallback           bool           `json:"fallback,omitempty"`
	DeclinedExecutions []string       `json:"declined_executions,omitempty"`
	ExecutionID        string         `json:"execution_id,omitempty"`
	PendingExecutions  []string       `json:"pending_executions,omitempty"`
	OutsideAccepted    string         `json:"outside_accepted,omitempty"`
	RejectedID         string         `json:"rejected_id,omitempty"`
	LastIntent         string         `json:"last_intent,omitempty"`
	Transfer           *TransferPlan  `json:"transfer,omitempty"`
	Current            *agent.Intent  `json:"current,omitempty"`
	Pending            []agent.Intent `json:"pending,omitempty"`
	Stage              string         `json:"stage,omitempty"`
	CardID             string         `json:"card_id,omitempty"`
	CardVersion        int            `json:"card_version,omitempty"`
	StepID             string         `json:"step_id,omitempty"`
	ConfirmationID     string         `json:"confirmation_id,omitempty"`
}

func mergeFollowup(intent *agent.Intent, text string, now time.Time, loc *time.Location) bool {
	q := strings.ToLower(strings.TrimSpace(text))
	changed := false
	if intent.Name == "time_off_request" {
		a, b, ok := calendar.Parse(q, now, loc)
		if ok {
			intent.Slots.StartDate = a
			intent.Slots.EndDate = b
			return true
		}
		return false
	}
	if intent.Name != "access_request" {
		return false
	}
	if q == "чтение" || q == "read" {
		intent.Slots.Level = "read"
		changed = true
	}
	if q == "запись" || q == "write" {
		intent.Slots.Level = "write"
		changed = true
	}
	if q == "редактирование" || q == "edit" {
		intent.Slots.Level = "edit"
		changed = true
	}
	if strings.HasPrefix(q, "для ") && len([]rune(text)) >= 10 {
		intent.Slots.Justification = text
		changed = true
	}
	if strings.HasPrefix(q, "ресурс ") {
		intent.Slots.ResourceID = strings.TrimSpace(strings.TrimPrefix(q, "ресурс "))
		changed = true
	}
	return changed
}

func tokenMatch(query, needle string) bool {
	normalize := func(s string) string {
		return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' }), " ")
	}
	return strings.Contains(" "+normalize(query)+" ", " "+normalize(needle)+" ")
}

type TransferPlan struct {
	OldID       string `json:"old_id"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	FirstCardID string `json:"first_card_id"`
}
