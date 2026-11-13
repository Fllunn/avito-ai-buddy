package clock

import (
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	fixed, location, err := New("2026-11-12T12:00:00+05:00", "Asia/Yekaterinburg")
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Now().Format(time.RFC3339) != "2026-11-12T07:00:00Z" || fixed.Now().In(location).Hour() != 12 {
		t.Fatal("unexpected instant")
	}
	if fixed.Now() != fixed.Now() {
		t.Fatal("fixed clock moved")
	}
	system, _, err := New("", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	got := system.Now()
	if got.Before(before) || got.After(time.Now().UTC()) {
		t.Fatal("system clock outside window")
	}
	for _, input := range []struct{ now, zone string }{{"tomorrow", "UTC"}, {"2026-11-12", "UTC"}, {"", "Invalid/Zone"}, {"", ""}, {"", "Local"}} {
		if _, _, err := New(input.now, input.zone); err == nil {
			t.Errorf("accepted invalid clock: %+v", input)
		}
	}
}

func TestDate(t *testing.T) {
	_, location, _ := New("", "Asia/Yekaterinburg")
	date, err := Date("2026-11-12", location)
	if err != nil || date.Hour() != 0 || date.Location() != location {
		t.Fatal(date, err)
	}
	for _, input := range []string{"2026-02-29", "2026-11-31", "12.11.2026", "2026-11-12T00:00:00Z", ""} {
		if _, err := Date(input, location); err == nil {
			t.Errorf("accepted invalid date %s", input)
		}
	}
}

func TestWeekBoundaries(t *testing.T) {
	_, location, _ := New("", "Asia/Yekaterinburg")
	start, _ := Date("2026-11-09", location)
	for _, tc := range []struct {
		instant string
		week    int
	}{
		{"2026-11-08T23:59:59+05:00", 0}, {"2026-11-09T00:00:00+05:00", 1},
		{"2026-11-12T12:00:00+05:00", 1}, {"2026-11-15T23:59:59+05:00", 1},
		{"2026-11-15T19:00:00Z", 2}, {"2026-11-23T00:00:00+05:00", 3},
	} {
		now, err := time.Parse(time.RFC3339, tc.instant)
		if err != nil {
			t.Fatal(err)
		}
		if got := Week(start, now, location); got != tc.week {
			t.Errorf("%s: week %d, want %d", tc.instant, got, tc.week)
		}
	}
	maxim, _ := Date("2026-11-02", location)
	now, _ := Date("2026-11-12", location)
	if Week(maxim, now, location) != 2 {
		t.Fatal("Maxim must be in week two")
	}
}

func TestCivilDaysAcrossDST(t *testing.T) {
	_, location, err := New("", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		start, end string
		days       int
	}{
		{"2026-03-02", "2026-03-09", 7}, {"2026-10-26", "2026-11-02", 7},
		{"2026-12-31", "2027-01-01", 1}, {"2028-02-28", "2028-03-01", 2}, {"2026-11-02", "2026-10-26", -7},
	} {
		start, _ := Date(tc.start, location)
		end, _ := Date(tc.end, location)
		if got := DaysBetween(start, end, location); got != tc.days {
			t.Errorf("%+v: got %d", tc, got)
		}
		if tc.days == 7 && Week(start, end, location) != 2 {
			t.Fatal("DST changed week")
		}
	}
}
