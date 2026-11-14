package calendar

import (
	"testing"
	"time"
)

func TestRulesAndParsing(t *testing.T) {
	loc := time.FixedZone("demo", 5*3600)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, loc)
	for _, tc := range []struct {
		a, b  string
		days  int
		valid bool
	}{{"2026-11-13", "2026-11-16", 2, true}, {"2027-02-10", "2027-02-10", 1, true}, {"2027-02-11", "2027-02-11", 1, false}, {"2026-11-11", "2026-11-14", 3, false}, {"2026-11-13", "2026-11-23", 7, false}, {"2026-11-14", "2026-11-13", 0, false}, {"bad", "bad", 0, false}, {"2026-12-31", "2027-01-08", 0, false}} {
		n, errors := Validate(now, loc, tc.a, tc.b)
		if n != tc.days || (len(errors) == 0) != tc.valid {
			t.Fatal(tc, n, errors)
		}
	}
	_, errors := Validate(now, loc, "2026-11-11", "2026-11-14")
	if len(errors) < 2 {
		t.Fatal("only first reason returned")
	}
	for _, tc := range []struct {
		q, a, b string
		ok      bool
	}{{"13–16 ноября 2026", "2026-11-13", "2026-11-16", true}, {"13-16 ноября", "2026-11-13", "2026-11-16", true}, {"20 ноября 2026", "2026-11-20", "2026-11-20", true}, {"31 февраля 2027", "", "", false}, {"пятницу и понедельник", "2026-11-13", "2026-11-16", true}, {"в пятницу 20-го", "2026-11-20", "2026-11-20", true}, {"в четверг 20-го", "", "", false}, {"на следующей неделе", "", "", false}, {"полдня завтра", "", "", false}, {"завтра", "2026-11-13", "2026-11-13", true}, {"послезавтра", "2026-11-14", "2026-11-14", true}, {"13.11.2026", "2026-11-13", "2026-11-13", true}, {"2026-11-13 по 2026-11-16", "2026-11-13", "2026-11-16", true}, {"в пятницу и вторник", "", "", false}, {"32.11", "", "", false}, {"2026-13-01", "", "", false}, {"когда-нибудь", "", "", false}} {
		a, b, ok := Parse(tc.q, now, loc)
		if a != tc.a || b != tc.b || ok != tc.ok {
			t.Fatal(tc, a, b, ok)
		}
	}
	endDay := time.Date(2026, 11, 12, 23, 59, 0, 0, loc)
	_, err := Validate(endDay, loc, "2026-11-13", "2026-11-13")
	if len(err) > 0 {
		t.Fatal(err)
	}
	_, err = Validate(endDay.Add(time.Minute), loc, "2026-11-13", "2026-11-13")
	if len(err) == 0 {
		t.Fatal("day boundary ignored")
	}
}
