package clock

import (
	"fmt"
	"time"
	_ "time/tzdata"
)

type Clock interface {
	Now() time.Time
}

type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

type Fixed struct{ Instant time.Time }

func (f Fixed) Now() time.Time { return f.Instant.UTC() }

// New uses an immutable instant for repeatable demonstrations and tests.
func New(demoNow, timezone string) (Clock, *time.Location, error) {
	if timezone == "" || timezone == "Local" {
		return nil, nil, fmt.Errorf("invalid DEMO_TIMEZONE: expected explicit timezone")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid DEMO_TIMEZONE")
	}
	if demoNow == "" {
		return System{}, location, nil
	}
	instant, err := time.Parse(time.RFC3339, demoNow)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid DEMO_NOW: expected RFC3339")
	}
	return Fixed{Instant: instant}, location, nil
}

func Date(value string, location *time.Location) (time.Time, error) {
	date, err := time.ParseInLocation(time.DateOnly, value, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date: expected YYYY-MM-DD")
	}
	return date, nil
}

// DaysBetween counts civil days, including when a DST day is not 24 hours.
func DaysBetween(start, end time.Time, location *time.Location) int {
	civil := func(value time.Time) time.Time {
		y, m, d := value.In(location).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	return int(civil(end).Sub(civil(start)) / (24 * time.Hour))
}

// Week returns zero before the start date, then seven-day periods starting at one.
func Week(start, now time.Time, location *time.Location) int {
	days := DaysBetween(start, now, location)
	if days < 0 {
		return 0
	}
	return days/7 + 1
}
