package actions

import (
	"ai-buddy/backend/platform/safety"
	"context"
	"testing"
)

func TestSlots(t *testing.T) {
	safe := safety.Prepare(context.Background(), safety.Local{}, "Демонстрационный запрос")
	values, err := (Slots{ResourceID: "tracker-pay", Level: "read", StartDate: "2026-11-13", EndDate: "2026-11-16", RequestID: "ACC-1", StepID: "alina-P5", Justification: safe, Query: safe}).Values()
	if err != nil || len(values) != 8 {
		t.Fatal(values, err)
	}
	for _, slots := range []Slots{{ResourceID: "invalid raw value"}, {Level: "admin"}, {StartDate: "not-a-date"}} {
		if _, err := slots.Values(); err != ErrInvalid {
			t.Fatal(slots, err)
		}
	}
	if !KnownIntent("access_request") || KnownIntent("tool.execute") {
		t.Fatal("intent allowlist")
	}
}
