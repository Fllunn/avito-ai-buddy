package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Invariants from the access/time-off contracts and the data-isolation spec:
// each case breaks one promised rule and expects the service to refuse it.

func createAbsence(t *testing.T, s *Server, user, start, end string) TimeOffRequest {
	t.Helper()
	w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), start, end), user)
	var item TimeOffRequest
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &item) != nil {
		t.Fatal(w)
	}
	return item
}

func cancelBody(version int64) string {
	return fmt.Sprintf(`{"operation_key":%q,"expected_version":%d}`, key(), version)
}

func exec(t *testing.T, s *Server, sql string, args ...any) {
	t.Helper()
	if _, err := s.Store.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func absenceStatus(t *testing.T, s *Server, id string) (string, int64) {
	t.Helper()
	var status string
	var version int64
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT status,version FROM time_off_requests WHERE id=$1`, id).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	return status, version
}

func accessStatus(t *testing.T, s *Server, id string) string {
	t.Helper()
	var status string
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT status FROM access_requests WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestInternCannotSeeOrChangeAnotherInternsRequests(t *testing.T) {
	s := fixture(t)
	t.Cleanup(func() { s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })
	absence := createAbsence(t, s, "alina", "2026-11-20", "2026-11-20")
	access := create(t, s, "alina", "monitoring")

	for _, path := range []string{"/v1/time-off-requests", "/v1/access-requests"} {
		w := call(s, "GET", path, "", "maxim")
		if w.Code != 200 || strings.Contains(w.Body.String(), absence.ID) || strings.Contains(w.Body.String(), access.ID) {
			t.Fatal("foreign request listed", path, w)
		}
	}
	if w := call(s, "GET", "/v1/grants", "", "alina"); w.Code != 200 || strings.Contains(w.Body.String(), "ACC-1001") {
		t.Fatal("foreign grant listed", w)
	}

	if w := call(s, "POST", "/v1/time-off-requests/"+absence.ID+"/cancel", cancelBody(1), "maxim"); w.Code != 404 || errorCode(t, w) != "NOT_FOUND" {
		t.Fatal("foreign absence cancelled", w)
	}
	if status, version := absenceStatus(t, s, absence.ID); status != "submitted" || version != 1 {
		t.Fatal("foreign cancel changed absence", status, version)
	}
	if w := call(s, "POST", "/v1/access-requests/"+access.ID+"/cancel", cancelBody(1), "maxim"); w.Code != 404 {
		t.Fatal("foreign access request cancelled", w)
	}
	if status := accessStatus(t, s, access.ID); status != "created" {
		t.Fatal("foreign cancel changed access request", status)
	}
}

func TestOperationKeyOfAnotherInternRevealsNothing(t *testing.T) {
	s := fixture(t)
	// A rejected result is still another intern's data: replay must not return it.
	rejectedKey := key()
	body := createBody(rejectedKey, "security-incidents", "read", "Для знакомства с демонстрационным сервисом")
	if w := call(s, "POST", "/v1/access-requests", body, "alina"); w.Code != 403 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/access-requests", body, "maxim"); w.Code != 404 || errorCode(t, w) != "NOT_FOUND" {
		t.Fatal("foreign rejected result replayed", w)
	}
	// A key closed by reconcile belongs to its owner too.
	closedKey := key()
	if w := call(s, "POST", "/v1/operations/"+closedKey+"/reconcile", `{}`, "alina"); w.Code != 200 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/operations/"+closedKey+"/reconcile", `{}`, "maxim"); w.Code != 404 {
		t.Fatal("foreign closed key exposed", w)
	}
}

func TestDisabledInternCannotAct(t *testing.T) {
	s := fixture(t)
	t.Cleanup(func() { s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })
	absence := createAbsence(t, s, "alina", "2026-11-20", "2026-11-20")
	access := create(t, s, "alina", "monitoring")
	exec(t, s, `UPDATE actors SET enabled=false WHERE id='alina'`)

	for _, tc := range []struct{ path, body string }{
		{"/v1/access-requests", createBody(key(), "product-metrics", "read", "Для знакомства с демонстрационным сервисом")},
		{"/v1/time-off-requests", absenceBody(key(), "2026-11-23", "2026-11-23")},
		{"/v1/access-requests/" + access.ID + "/cancel", cancelBody(1)},
		{"/v1/time-off-requests/" + absence.ID + "/cancel", cancelBody(1)},
	} {
		// A disabled actor is rejected at authentication, before any business rule.
		if w := call(s, "POST", tc.path, tc.body, "alina"); w.Code != 401 || errorCode(t, w) != "UNAUTHENTICATED" {
			t.Fatal("disabled intern acted", tc.path, w)
		}
	}
	if w := call(s, "GET", "/v1/time-off-requests", "", "alina"); w.Code != 401 {
		t.Fatal("disabled intern read requests", w)
	}
	if status, _ := absenceStatus(t, s, absence.ID); status != "submitted" {
		t.Fatal(status)
	}
	if status := accessStatus(t, s, access.ID); status != "created" {
		t.Fatal(status)
	}
	var count int
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM access_requests WHERE user_id='alina' AND resource_id='product-metrics')+(SELECT count(*) FROM time_off_requests WHERE user_id='alina')`).Scan(&count); err != nil || count != 1 {
		t.Fatal("disabled intern created requests", count, err)
	}
}

func TestRequestsNeedAnActiveManagerApprover(t *testing.T) {
	s := fixture(t)
	t.Cleanup(func() { s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })

	// Resource approver "manager" resolves to the intern's own manager.
	if item := create(t, s, "alina", "launch-plans"); item.ApproverID != "olga" {
		t.Fatal("manager approver not resolved", item.ApproverID)
	}

	exec(t, s, `UPDATE actors SET enabled=false WHERE id='olga'`)
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-20", "2026-11-20"), "alina"); w.Code != 403 || errorCode(t, w) != "NOT_ALLOWED" {
		t.Fatal("time-off sent to disabled manager", w)
	}
	exec(t, s, `UPDATE actors SET enabled=true WHERE id='olga'`)

	exec(t, s, `UPDATE actors SET payload=jsonb_set(payload,'{manager_id}','null') WHERE id='maxim'`)
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-20", "2026-11-20"), "maxim"); w.Code != 403 || errorCode(t, w) != "NOT_ALLOWED" {
		t.Fatal("time-off without manager", w)
	}
	if w := call(s, "POST", "/v1/access-requests", createBody(key(), "launch-plans", "read", "Для знакомства с планами запусков"), "maxim"); w.Code != 403 || errorCode(t, w) != "NOT_ALLOWED" {
		t.Fatal("manager-approved access without manager", w)
	}
	var count int
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM time_off_requests`).Scan(&count); err != nil || count != 0 {
		t.Fatal("request created without approver", count, err)
	}
}

func TestCancelRefusesStaleVersionAndHiddenResource(t *testing.T) {
	s := fixture(t)
	t.Cleanup(func() { s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })

	absence := createAbsence(t, s, "alina", "2026-11-20", "2026-11-20")
	if w := call(s, "POST", "/v1/time-off-requests/"+absence.ID+"/cancel", cancelBody(2), "alina"); w.Code != 409 || errorCode(t, w) != "VERSION_CONFLICT" {
		t.Fatal("stale version accepted", w)
	}
	if status, version := absenceStatus(t, s, absence.ID); status != "submitted" || version != 1 {
		t.Fatal(status, version)
	}

	access := create(t, s, "alina", "monitoring")
	exec(t, s, `UPDATE resources SET payload=jsonb_set(payload,'{kind}','"hidden"') WHERE id='monitoring'`)
	if w := call(s, "POST", "/v1/access-requests/"+access.ID+"/cancel", cancelBody(1), "alina"); w.Code != 404 || errorCode(t, w) != "NOT_FOUND" {
		t.Fatal("hidden resource revealed by cancel", w)
	}
	if status := accessStatus(t, s, access.ID); status != "created" {
		t.Fatal(status)
	}
}

func TestTimeOffFailuresAreNotReportedAsSuccessOrPlainOutage(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		s.Store.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_operation_commit ON operations;DROP FUNCTION IF EXISTS fail_operation_commit();DELETE FROM time_off_requests`)
	})

	// Lost response after the effect: the client sees no success, reconcile finds the result.
	exec(t, s, `UPDATE faults SET mode='lose_response_after' WHERE service='time_off'`)
	operationKey := key()
	body := absenceBody(operationKey, "2026-11-20", "2026-11-20")
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/v1/time-off-requests", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.Config.Secret)
	req.Header.Set("X-Actor-ID", "alina")
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: time.Second}).Do(req)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("lost response returned normal success")
	}
	exec(t, s, `UPDATE faults SET mode='normal' WHERE service='time_off'`)
	reconciled := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "alina")
	if reconciled.Code != 200 || !strings.Contains(reconciled.Body.String(), `"state":"completed"`) {
		t.Fatal(reconciled)
	}

	// Commit outcome unknown: must not claim the request surely did not happen.
	exec(t, s, `CREATE FUNCTION fail_operation_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic commit failure'; END $$;CREATE CONSTRAINT TRIGGER fail_operation_commit AFTER INSERT ON operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_operation_commit()`)
	if w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), "2026-11-23", "2026-11-23"), "alina"); w.Code != 500 || errorCode(t, w) != "SERVICE_ERROR" {
		t.Fatal("ambiguous commit advertised as guaranteed unavailability", w)
	}
}
