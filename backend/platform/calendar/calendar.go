// Package calendar defines the synthetic absence rules shared by API and mocks.
package calendar

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var Holidays = []string{"2026-12-31", "2027-01-01", "2027-01-02", "2027-01-03", "2027-01-04", "2027-01-05", "2027-01-06", "2027-01-07", "2027-01-08"}

func Working(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	for _, d := range Holidays {
		if t.Format(time.DateOnly) == d {
			return false
		}
	}
	return true
}
func Count(start, end time.Time) int {
	if end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return 0
	}
	n := 0
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if Working(day) {
			n++
		}
	}
	return n
}
func Validate(now time.Time, loc *time.Location, start, end string) (int, map[string]string) {
	errors := map[string]string{}
	a, errA := time.ParseInLocation(time.DateOnly, start, loc)
	b, errB := time.ParseInLocation(time.DateOnly, end, loc)
	if errA != nil {
		errors["start_date"] = "Укажи дату начала в формате ГГГГ-ММ-ДД."
	}
	if errB != nil {
		errors["end_date"] = "Укажи дату окончания в формате ГГГГ-ММ-ДД."
	}
	if len(errors) > 0 {
		return 0, errors
	}
	today, _ := time.ParseInLocation(time.DateOnly, now.In(loc).Format(time.DateOnly), loc)
	next := today.AddDate(0, 0, 1)
	for !Working(next) {
		next = next.AddDate(0, 0, 1)
	}
	if a.Before(next) {
		errors["notice"] = "Начало — не раньше следующего рабочего дня."
	}
	if !Working(a) {
		errors["start_workday"] = "Первый день должен быть рабочим."
	}
	if !Working(b) {
		errors["end_workday"] = "Последний день должен быть рабочим."
	}
	if b.Before(a) {
		errors["order"] = "Конец не может быть раньше начала."
	}
	if b.After(today.AddDate(0, 0, 90)) {
		errors["horizon"] = "Конец — не дальше 90 календарных дней."
	}
	days := Count(a, b)
	if days < 1 || days > 5 {
		errors["workdays"] = "Допустимо 1–5 рабочих дней. Для большего периода обратись к руководителю."
	}
	return days, errors
}

var iso = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
var rangeDash = regexp.MustCompile(`[0-9]\s*-\s*[0-9]`)
var explicitYear = regexp.MustCompile(`\b(20[0-9]{2})\b`)
var monthNames = map[string]time.Month{"январ": time.January, "феврал": time.February, "март": time.March, "апрел": time.April, "мая": time.May, "май": time.May, "июн": time.June, "июл": time.July, "август": time.August, "сентябр": time.September, "октябр": time.October, "ноябр": time.November, "декабр": time.December}
var dayNumber = regexp.MustCompile(`\b([0-9]{1,2})(?:-го|[.]([0-9]{1,2})(?:[.]([0-9]{4}))?)?`)
var weekdays = map[string]time.Weekday{"понедельник": time.Monday, "вторник": time.Tuesday, "сред": time.Wednesday, "четверг": time.Thursday, "пятниц": time.Friday, "суббот": time.Saturday, "воскресень": time.Sunday}

// Parse returns false rather than guessing incomplete or contradictory dates.
func Parse(raw string, now time.Time, loc *time.Location) (string, string, bool) {
	q := strings.ToLower(raw)
	if strings.Contains(q, "полдня") || strings.Contains(q, "половин") || strings.Contains(q, "следующей неделе") {
		return "", "", false
	}
	today := now.In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	var dates []time.Time
	matches := iso.FindAllString(q, -1)
	if len(matches) > 0 {
		for _, v := range matches {
			d, e := time.ParseInLocation(time.DateOnly, v, loc)
			if e != nil {
				return "", "", false
			}
			dates = append(dates, d)
		}
	} else {
		year := today.Year()
		years := explicitYear.FindAllString(q, -1)
		if len(years) > 1 {
			return "", "", false
		}
		if len(years) == 1 {
			year, _ = strconv.Atoi(years[0])
			q = explicitYear.ReplaceAllString(q, " ")
		}
		namedMonth := time.Month(0)
		for name, month := range monthNames {
			if strings.Contains(q, name) {
				if namedMonth != 0 && namedMonth != month {
					return "", "", false
				}
				namedMonth = month
			}
		}
		numbers := dayNumber.FindAllStringSubmatch(q, -1)
		for _, v := range numbers {
			day, _ := strconv.Atoi(v[1])
			month := int(today.Month())
			if namedMonth != 0 {
				month = int(namedMonth)
			}
			if v[2] != "" {
				month, _ = strconv.Atoi(v[2])
			}
			if v[3] != "" {
				year, _ = strconv.Atoi(v[3])
			}
			d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, loc)
			if d.Day() != day || int(d.Month()) != month {
				return "", "", false
			}
			dates = append(dates, d)
		}
	}
	type named struct {
		pos int
		day time.Weekday
	}
	names := []named{}
	for name, day := range weekdays {
		if pos := strings.Index(q, name); pos >= 0 {
			names = append(names, named{pos, day})
		}
	}
	if len(names) == 2 && names[0].pos > names[1].pos {
		names[0], names[1] = names[1], names[0]
	}
	if len(dates) > 0 {
		if len(names) > 0 && (len(names) != len(dates) || names[0].day != dates[0].Weekday() || len(names) == 2 && names[1].day != dates[1].Weekday()) {
			return "", "", false
		}
	} else if len(names) > 0 {
		for _, name := range names {
			d := today.AddDate(0, 0, 1)
			for d.Weekday() != name.day {
				d = d.AddDate(0, 0, 1)
			}
			dates = append(dates, d)
		}
	} else if strings.Contains(q, "послезавтра") {
		dates = append(dates, today.AddDate(0, 0, 2))
	} else if strings.Contains(q, "завтра") {
		dates = append(dates, today.AddDate(0, 0, 1))
	}
	if len(dates) == 0 || len(dates) > 2 {
		return "", "", false
	}
	a, b := dates[0], dates[len(dates)-1]
	// Nonconsecutive weekdays need separate requests, not one implicit range.
	if len(dates) == 2 && !strings.Contains(q, " по ") && !strings.Contains(q, "–") && !rangeDash.MatchString(q) && Count(a, b) > 2 {
		return "", "", false
	}
	return a.Format(time.DateOnly), b.Format(time.DateOnly), true
}
