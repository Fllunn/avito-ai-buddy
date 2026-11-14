package service

import (
	"ai-buddy/backend/platform/clock"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func absenceBody(k, a, b string) string {
	return fmt.Sprintf(`{"operation_key":%q,"start_date":%q,"end_date":%q}`, k, a, b)
}
func TestAbsenceContractConcurrencyAndClosedKey(t *testing.T) {
	s := fixture(t)
	t.Cleanup(func() { s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })
	for _, method := range []string{"GET", "POST"} {
		if w := call(s, method, "/v1/time-off-requests", `{}`, ""); w.Code != 401 {
			t.Fatal(w)
		}
	}
	if w := call(s, "GET", "/v1/time-off-rules", "", "alina"); w.Code != 200 || !strings.Contains(w.Body.String(), "2026-12-31") {
		t.Fatal(w)
	}
	for _, body := range []string{`{}`, absenceBody("bad", "2026-11-13", "2026-11-13"), `{"operation_key":"` + key() + `","start_date":"2026-11-13","end_date":"2026-11-13","reason":"диагноз"}`} {
		if w := call(s, "POST", "/v1/time-off-requests", body, "alina"); w.Code != 400 {
			t.Fatal(w)
		}
	}
	bad := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-11", "2026-11-14"), "alina")
	if bad.Code != 400 || !strings.Contains(bad.Body.String(), "notice") || !strings.Contains(bad.Body.String(), "end_workday") {
		t.Fatal(bad)
	}
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-13", "2026-11-13"), "igor"); w.Code != 403 {
		t.Fatal(w)
	}
	var wg sync.WaitGroup
	out := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			out <- call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-13", "2026-11-16"), "alina").Code
		})
	}
	wg.Wait()
	close(out)
	counts := map[int]int{}
	for status := range out {
		counts[status]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	k := key()
	body := absenceBody(k, "2026-11-20", "2026-11-20")
	first := call(s, "POST", "/v1/time-off-requests", body, "alina")
	second := call(s, "POST", "/v1/time-off-requests", body, "alina")
	if first.Code != 201 || second.Body.String() != first.Body.String() {
		t.Fatal(first, second)
	}
	var item TimeOffRequest
	if json.Unmarshal(first.Body.Bytes(), &item) != nil {
		t.Fatal(first)
	}
	if w := call(s, "GET", "/v1/time-off-requests/"+item.ID, "", "maxim"); w.Code != 404 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(k, "2026-11-23", "2026-11-23"), "alina"); errorCode(t, w) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(w)
	}
	cancel := fmt.Sprintf(`{"operation_key":%q,"expected_version":1}`, key())
	if w := call(s, "POST", "/v1/time-off-requests/"+item.ID+"/cancel", cancel, "alina"); w.Code != 200 || !strings.Contains(w.Body.String(), "cancelled") {
		t.Fatal(w)
	}
	closed := key()
	if w := call(s, "POST", "/v1/operations/"+closed+"/reconcile", `{}`, "alina"); w.Code != 200 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(closed, "2026-11-23", "2026-11-23"), "alina"); errorCode(t, w) != "OPERATION_CLOSED" {
		t.Fatal(w)
	}
}
func TestAbsenceStatusAndCancellationDeadline(t *testing.T) {
	s := panelFixture(t)
	t.Cleanup(func() { s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })
	w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-13", "2026-11-13"), "maxim")
	var a TimeOffRequest
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &a) != nil {
		t.Fatal(w)
	}
	cookie := loginPresenter(t, s)
	endpoint := "/demo/time-off-requests/" + a.ID + "/status"
	if w := panelCall(s, "PATCH", endpoint, `{"expected_version":1,"status":"approved"}`, "application/json", s.Config.PanelOrigin, nil); w.Code != 401 {
		t.Fatal(w)
	}
	if w := panelCall(s, "PATCH", endpoint, `{"expected_version":1,"status":"approved"}`, "application/json", s.Config.PanelOrigin, cookie); w.Code != 200 {
		t.Fatal(w)
	}
	panelPage := panelCall(s, "GET", "/demo", "", "", "", cookie)
	if panelPage.Code != 200 || !strings.Contains(panelPage.Body.String(), a.ID) || !strings.Contains(panelPage.Body.String(), "Запросы отгулов") {
		t.Fatal(panelPage)
	}
	s.Config.Clock = clock.Fixed{Instant: time.Date(2026, 11, 13, 0, 0, 0, 0, s.Config.Location)}
	body := fmt.Sprintf(`{"operation_key":%q,"expected_version":2}`, key())
	late := call(s, "POST", "/v1/time-off-requests/"+a.ID+"/cancel", body, "maxim")
	if errorCode(t, late) != "CANCELLATION_TOO_LATE" {
		t.Fatal(late)
	}
}

func TestAbsenceReadsTransitionsAndFailureModes(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	t.Cleanup(func() { s.Store.Pool.Exec(ctx, `DELETE FROM time_off_requests`) })
	if w := call(s, "GET", "/v1/time-off-requests", "", "alina"); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w)
	}
	w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-20", "2026-11-20"), "alina")
	var item TimeOffRequest
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &item) != nil {
		t.Fatal(w)
	}
	if w := call(s, "GET", "/v1/time-off-requests/"+item.ID, "", "alina"); w.Code != 200 {
		t.Fatal(w)
	}
	if w := call(s, "GET", "/v1/time-off-requests", "", "alina"); w.Code != 200 || !strings.Contains(w.Body.String(), item.ID) {
		t.Fatal(w)
	}
	for _, tc := range []struct {
		version              int64
		status, reason, code string
	}{{0, "approved", "", "VALIDATION_ERROR"}, {99, "approved", "", "VERSION_CONFLICT"}, {1, "rejected", "", "VALIDATION_ERROR"}, {1, "unknown", "", "VALIDATION_ERROR"}} {
		result, e := s.TimeOffStatus(ctx, item.ID, tc.version, tc.status, tc.reason)
		if e != nil || !strings.Contains(string(result.Body), tc.code) {
			t.Fatal(result, e)
		}
	}
	result, e := s.TimeOffStatus(ctx, item.ID, 1, "in_review", "")
	if e != nil || result.Status != 200 {
		t.Fatal(result, e)
	}
	result, e = s.TimeOffStatus(ctx, item.ID, 2, "rejected", "демонстрационный отказ token=synthetic-secret")
	if e != nil || result.Status != 200 || strings.Contains(string(result.Body), "synthetic-secret") {
		t.Fatal(result, e)
	}
	result, e = s.TimeOffStatus(ctx, item.ID, 3, "approved", "")
	if e != nil || !strings.Contains(string(result.Body), "INVALID_STATE") {
		t.Fatal(result, e)
	}
	body := fmt.Sprintf(`{"operation_key":%q,"expected_version":3}`, key())
	if w := call(s, "POST", "/v1/time-off-requests/"+item.ID+"/cancel", body, "alina"); errorCode(t, w) != "INVALID_STATE" {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-20", "2026-11-20"), "alina"); w.Code != 201 {
		t.Fatal("rejected dates block retry", w)
	}
	if _, e = s.Store.Pool.Exec(ctx, `UPDATE faults SET mode='unavailable_before' WHERE service='time_off'`); e != nil {
		t.Fatal(e)
	}
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-23", "2026-11-23"), "alina"); w.Code != 503 {
		t.Fatal(w)
	}
	result, e = s.TimeOffStatus(ctx, "ABS-missing", 1, "approved", "")
	if e != nil || result.Status != 404 {
		t.Fatal(result, e)
	}
}
