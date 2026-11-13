package service

import (
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func key() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func fixture(t *testing.T) *Server {
	t.Helper()
	url := os.Getenv("TEST_MOCKS_DATABASE_URL")
	if url == "" {
		t.Skip("make coverage starts isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool.Exec(context.Background(), `DELETE FROM grants WHERE source_request_id<>'ACC-1001';DELETE FROM access_requests WHERE id<>'ACC-1001';DELETE FROM operations;UPDATE faults SET mode='normal',delay_ms=0`)
		for _, p := range demo.Profiles() {
			data, _ := json.Marshal(p)
			_, _ = store.Pool.Exec(context.Background(), `UPDATE actors SET payload=$2,enabled=true WHERE id=$1`, p.ID, data)
		}
		for _, r := range demo.Resources() {
			data, _ := json.Marshal(r)
			_, _ = store.Pool.Exec(context.Background(), `UPDATE resources SET payload=$2 WHERE id=$1`, r.ID, data)
		}
	})
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	return &Server{store, Config{Secret: strings.Repeat("s", 32), Clock: clock.Fixed{Instant: now}, Location: time.FixedZone("demo", 5*3600), Demo: true}}
}
func call(s *Server, method, path, body, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if user != "" {
		r.Header.Set("Authorization", "Bearer "+s.Config.Secret)
		r.Header.Set("X-Actor-ID", user)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func createBody(operationKey, resource, level, justification string) string {
	data, _ := json.Marshal(accessInput{operationKey, resource, level, justification})
	return string(data)
}
func create(t *testing.T, s *Server, user, resource string) AccessRequest {
	t.Helper()
	w := call(s, "POST", "/v1/access-requests", createBody(key(), resource, "read", "Для задач демонстрационной стажировки"), user)
	var result AccessRequest
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w)
	}
	return result
}
func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body ErrorBody
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Fields == nil {
		t.Fatal(w)
	}
	return body.Error.Code
}
func TestIdentityCatalogueAndSeed(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if err := s.Store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "GET", "/v1/resources", "", ""); w.Code != 401 {
		t.Fatal(w)
	}
	if w := call(s, "GET", "/v1/resources", "", "missing"); w.Code != 401 {
		t.Fatal(w)
	}
	w := call(s, "GET", "/v1/resources", "", "alina")
	var resources struct {
		Items []Resource `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resources) != nil || len(resources.Items) != 10 {
		t.Fatal(w)
	}
	for _, r := range resources.Items {
		if r.ID == "salary-reports" {
			t.Fatal("hidden resource")
		}
		if r.ID == "security-incidents" && (r.CanRequest || r.ApproverID != nil) {
			t.Fatal(r)
		}
	}
	r := httptest.NewRequest("GET", "/v1/resources", nil)
	r.Header.Set("Authorization", "Bearer "+s.Config.Secret)
	r.Header.Set("X-Actor-ID", "alina")
	r.Header.Set("Origin", "http://localhost:5173")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("browser service access", w)
	}
	w = call(s, "GET", "/v1/grants?resource_id=space-search", "", "maxim")
	var grants struct {
		Items []Grant `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &grants) != nil || len(grants.Items) != 1 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/access-requests", createBody(key(), "space-search", "read", "Для знакомства с командой"), "maxim"); w.Code != 409 || errorCode(t, w) != "ALREADY_GRANTED" {
		t.Fatal(w)
	}
	for _, resource := range []string{"salary-reports", "unknown"} {
		for _, path := range []string{"/v1/grants?resource_id=", "/v1/access-requests?resource_id="} {
			if w := call(s, "GET", path+resource, "", "alina"); w.Code != 404 || errorCode(t, w) != "NOT_FOUND" {
				t.Fatal(w)
			}
		}
	}
}
func TestCreateReplayAndOwnership(t *testing.T) {
	s := fixture(t)
	operationKey := key()
	body := createBody(operationKey, "monitoring", "read", "Для знакомства с демонстрационным мониторингом")
	first := call(s, "POST", "/v1/access-requests", body, "alina")
	if first.Code != 201 {
		t.Fatal(first)
	}
	same := call(s, "POST", "/v1/access-requests", body, "alina")
	if same.Code != 201 || same.Body.String() != first.Body.String() {
		t.Fatal("replay differs", first, same)
	}
	var object AccessRequest
	if json.Unmarshal(first.Body.Bytes(), &object) != nil {
		t.Fatal(first)
	}
	if w := call(s, "GET", "/v1/access-requests/"+object.ID, "", "maxim"); w.Code != 404 {
		t.Fatal("foreign object exposed", w)
	}
	if w := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "maxim"); w.Code != 404 {
		t.Fatal("foreign key exposed", w)
	}
	changed := createBody(operationKey, "monitoring", "read", "Другая задача демонстрационной стажировки")
	if w := call(s, "POST", "/v1/access-requests", changed, "alina"); w.Code != 409 || errorCode(t, w) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/access-requests", body, "maxim"); w.Code != 404 {
		t.Fatal(w)
	}
	if w := call(s, "GET", "/v1/access-requests/"+object.ID, "", "alina"); w.Code != 200 {
		t.Fatal(w)
	}
	if w := call(s, "GET", "/v1/access-requests?resource_id=monitoring", "", "alina"); w.Code != 200 || !strings.Contains(w.Body.String(), object.ID) {
		t.Fatal(w)
	}
	reconciled := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "alina")
	if reconciled.Code != 200 || !strings.Contains(reconciled.Body.String(), `"state":"completed"`) || !strings.Contains(reconciled.Body.String(), `"http_status":201`) {
		t.Fatal(reconciled)
	}
}
func TestAccessSchemaAndRules(t *testing.T) {
	s := fixture(t)
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{}`, 400, "VALIDATION_ERROR"}, {`{"operation_key":"bad","resource_id":"monitoring","level":"read","justification":"Для знакомства с сервисом"}`, 400, "VALIDATION_ERROR"},
		{createBody(key(), "salary-reports", "read", "Для знакомства с сервисом"), 404, "NOT_FOUND"},
		{createBody(key(), "unknown", "read", "Для знакомства с сервисом"), 404, "NOT_FOUND"},
		{createBody(key(), "security-incidents", "read", "Для знакомства с сервисом"), 403, "NOT_ALLOWED"},
		{createBody(key(), "monitoring", "admin", "Для знакомства с сервисом"), 409, "LEVEL_NOT_ALLOWED"},
		{createBody(key(), "monitoring", "read", "коротко"), 400, "VALIDATION_ERROR"},
		{createBody(key(), "monitoring", "read", strings.Repeat("я", 501)), 400, "VALIDATION_ERROR"},
		{createBody(key(), "monitoring", "read", "диагноз диабет"), 400, "VALIDATION_ERROR"},
		{`{"operation_key":"` + key() + `","resource_id":"monitoring","level":"read","justification":"Для знакомства с сервисом","user_id":"maxim"}`, 400, "VALIDATION_ERROR"},
		{`{} {}`, 400, "VALIDATION_ERROR"},
	} {
		w := call(s, "POST", "/v1/access-requests", tc.body, "alina")
		if w.Code != tc.status || errorCode(t, w) != tc.code || strings.Contains(w.Body.String(), "диабет") {
			t.Fatal(tc, w)
		}
	}
	for _, user := range []string{"igor", "olga", "daria"} {
		w := call(s, "POST", "/v1/access-requests", createBody(key(), "monitoring", "read", "Для знакомства с сервисом"), user)
		if w.Code != 403 {
			t.Fatal(user, w)
		}
	}
	if _, err := s.Store.Pool.Exec(context.Background(), `UPDATE actors SET payload=jsonb_set(payload,'{start_date}','"2026-11-13"') WHERE id='alina'`); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "POST", "/v1/access-requests", createBody(key(), "monitoring", "read", "Для знакомства с сервисом"), "alina"); w.Code != 403 {
		t.Fatal("future start allowed", w)
	}
}
func TestConcurrentOpenDuplicateAndSameKey(t *testing.T) {
	s := fixture(t)
	sameKey := key()
	body := createBody(sameKey, "tracker-pay", "read", "Для знакомства с сервисом")
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Go(func() { results <- call(s, "POST", "/v1/access-requests", body, "alina") })
	}
	wg.Wait()
	close(results)
	var first string
	for w := range results {
		if w.Code != 201 {
			t.Fatal(w)
		}
		if first != "" && first != w.Body.String() {
			t.Fatal("same key created different results")
		}
		first = w.Body.String()
	}
	w := call(s, "POST", "/v1/access-requests", createBody(key(), "tracker-pay", "edit", "Для другой демонстрационной задачи"), "alina")
	if w.Code != 409 || errorCode(t, w) != "DUPLICATE_OPEN_REQUEST" || !strings.Contains(w.Body.String(), "existing_id") {
		t.Fatal(w)
	}
}
func TestCancelVersionsAndTerminalState(t *testing.T) {
	s := fixture(t)
	object := create(t, s, "alina", "monitoring")
	operationKey := key()
	body := fmt.Sprintf(`{"operation_key":%q,"expected_version":1}`, operationKey)
	if w := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", fmt.Sprintf(`{"operation_key":%q,"expected_version":1}`, key()), "maxim"); w.Code != 404 {
		t.Fatal(w)
	}
	w := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", body, "alina")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatal(w)
	}
	same := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", body, "alina")
	if same.Body.String() != w.Body.String() {
		t.Fatal("cancel replay changed")
	}
	if w := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", fmt.Sprintf(`{"operation_key":%q,"expected_version":1}`, key()), "alina"); w.Code != 409 || errorCode(t, w) != "VERSION_CONFLICT" {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", fmt.Sprintf(`{"operation_key":%q,"expected_version":2}`, key()), "alina"); w.Code != 409 || errorCode(t, w) != "INVALID_STATE" {
		t.Fatal(w)
	}
	for _, id := range []string{"bad", "ACC-no", "ACC-999999"} {
		if w := call(s, "POST", "/v1/access-requests/"+id+"/cancel", fmt.Sprintf(`{"operation_key":%q,"expected_version":1}`, key()), "alina"); w.Code != 404 {
			t.Fatal(w)
		}
	}
	if w := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", `{"operation_key":"bad","expected_version":0}`, "alina"); w.Code != 400 {
		t.Fatal(w)
	}
}
func TestClosedOperationAndPresenterRace(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	closed := key()
	first := call(s, "POST", "/v1/operations/"+closed+"/reconcile", `{}`, "alina")
	if first.Code != 200 || !strings.Contains(first.Body.String(), `"state":"not_created"`) {
		t.Fatal(first)
	}
	same := call(s, "POST", "/v1/operations/"+closed+"/reconcile", `{}`, "alina")
	if same.Body.String() != first.Body.String() {
		t.Fatal("closed key changed")
	}
	w := call(s, "POST", "/v1/access-requests", createBody(closed, "monitoring", "read", "Для знакомства с сервисом"), "alina")
	if w.Code != 409 || errorCode(t, w) != "OPERATION_CLOSED" {
		t.Fatal(w)
	}
	object := create(t, s, "alina", "tracker-srch")
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := s.AccessStatus(ctx, object.ID, 1, "approved", "")
		if err != nil {
			t.Error(err)
		}
	})
	wg.Go(func() {
		w := call(s, "POST", "/v1/access-requests/"+object.ID+"/cancel", fmt.Sprintf(`{"operation_key":%q,"expected_version":1}`, key()), "alina")
		if w.Code != 200 && w.Code != 409 {
			t.Error(w)
		}
	})
	wg.Wait()
	var status string
	var grants int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT status FROM access_requests WHERE id=$1`, object.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM grants WHERE source_request_id=$1`, object.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if status == "approved" && grants != 1 || status == "cancelled" && grants != 0 || status != "approved" && status != "cancelled" {
		t.Fatal("race produced inconsistent grant", status, grants)
	}
}
func TestFaultModesDistinguishNoProcessingAndLostResponse(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	set := func(mode string, delay int) {
		t.Helper()
		if _, err := s.Store.Pool.Exec(ctx, `UPDATE faults SET mode=$1,delay_ms=$2 WHERE service='access'`, mode, delay); err != nil {
			t.Fatal(err)
		}
	}
	operationKey := key()
	body := createBody(operationKey, "monitoring", "read", "Для знакомства с сервисом")
	set("unavailable_before", 0)
	if w := call(s, "POST", "/v1/access-requests", body, "alina"); w.Code != 503 || errorCode(t, w) != "SERVICE_UNAVAILABLE" {
		t.Fatal(w)
	}
	var count int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE operation_key=$1`, operationKey).Scan(&count); err != nil || count != 0 {
		t.Fatal("unavailable processed request", count, err)
	}
	set("lose_response_after", 0)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/v1/access-requests", strings.NewReader(body))
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
	set("normal", 0)
	reconciled := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "alina")
	if reconciled.Code != 200 || !strings.Contains(reconciled.Body.String(), `"state":"completed"`) {
		t.Fatal(reconciled)
	}
	replay := call(s, "POST", "/v1/access-requests", body, "alina")
	if replay.Code != 201 {
		t.Fatal(replay)
	}
	var object AccessRequest
	_ = json.Unmarshal(replay.Body.Bytes(), &object)
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM access_requests WHERE id=$1`, object.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	set("reconciliation_unavailable", 0)
	if w := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "alina"); w.Code != 503 {
		t.Fatal(w)
	}
	set("delay_before", 20)
	before := time.Now()
	w := call(s, "POST", "/v1/access-requests", createBody(key(), "repo-payments", "read", "Для знакомства с сервисом"), "alina")
	if w.Code != 201 || time.Since(before) < 20*time.Millisecond {
		t.Fatal(w)
	}
}
func TestRawSensitiveTextIsNotPersisted(t *testing.T) {
	s := fixture(t)
	operationKey := key()
	raw := "Отгул из-за диагноза диабет"
	w := call(s, "POST", "/v1/access-requests", createBody(operationKey, "monitoring", "read", raw), "alina")
	if w.Code != 400 {
		t.Fatal(w)
	}
	var data []byte
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT body FROM operations WHERE operation_key=$1`, operationKey).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("диабет")) || bytes.Contains(data, []byte(raw)) {
		t.Fatal("sensitive text in journal")
	}
	object := create(t, s, "alina", "product-metrics")
	result, err := s.AccessStatus(context.Background(), object.ID, 1, "rejected", "диагноз диабет")
	if err != nil || result.Status != 200 || bytes.Contains(result.Body, []byte("диабет")) {
		t.Fatal(string(result.Body), err)
	}
}
func TestClosedStoreAndMalformedTransport(t *testing.T) {
	s := fixture(t)
	r := httptest.NewRequest("POST", "/v1/access-requests", io.NopCloser(strings.NewReader(`{}`)))
	r.Header.Set("Authorization", "Bearer "+s.Config.Secret)
	r.Header.Set("X-Actor-ID", "alina")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/operations/bad/reconcile", `{}`, "alina"); w.Code != 400 {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/operations/"+key()+"/reconcile", `{"extra":true}`, "alina"); w.Code != 400 {
		t.Fatal(w)
	}
	if _, err := Open(context.Background(), "postgres://%zz"); err == nil {
		t.Fatal("invalid URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, "postgres://localhost:1/missing"); err == nil {
		t.Fatal("unavailable database")
	}
	closed, err := Open(context.Background(), os.Getenv("TEST_MOCKS_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if closed.Migrate(context.Background()) == nil || closed.Seed(context.Background()) == nil {
		t.Fatal("closed database")
	}
	copyServer := *s
	copyServer.Store = closed
	if w := call(&copyServer, "GET", "/v1/resources", "", "alina"); w.Code != 503 {
		t.Fatal(w)
	}
}

func TestPresenterLifecycleAndHistoricalKeyResult(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	operationKey := key()
	body := createBody(operationKey, "monitoring", "read", "Для демонстрационных задач стажировки")
	created := call(s, "POST", "/v1/access-requests", body, "alina")
	if created.Code != 201 {
		t.Fatal(created)
	}
	var item AccessRequest
	_ = json.Unmarshal(created.Body.Bytes(), &item)
	for _, tc := range []struct {
		version        int64
		status, reason string
		expected       int
	}{{0, "approved", "", 400}, {1, "created", "", 400}, {1, "rejected", "", 400}, {1, "rejected", strings.Repeat("я", 501), 400}, {2, "approved", "", 409}} {
		got, err := s.AccessStatus(ctx, item.ID, tc.version, tc.status, tc.reason)
		if err != nil || got.Status != tc.expected {
			t.Fatal(tc, got, err)
		}
	}
	if result, err := s.AccessStatus(ctx, "ACC-99999", 1, "approved", ""); err != nil || result.Status != 404 {
		t.Fatal(result, err)
	}
	if result, err := s.AccessStatus(ctx, item.ID, 1, "in_review", ""); err != nil || result.Status != 200 {
		t.Fatal(result, err)
	}
	if result, err := s.AccessStatus(ctx, item.ID, 2, "approved", ""); err != nil || result.Status != 200 {
		t.Fatal(result, err)
	}
	if result, err := s.AccessStatus(ctx, item.ID, 3, "rejected", "Демонстрационный отказ"); err != nil || result.Status != 409 {
		t.Fatal(result, err)
	}
	status := call(s, "GET", "/v1/access-requests/"+item.ID, "", "alina")
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"version":3`) || !strings.Contains(status.Body.String(), `"status":"approved"`) {
		t.Fatal(status)
	}
	grants := call(s, "GET", "/v1/grants?resource_id=monitoring", "", "alina")
	if grants.Code != 200 || !strings.Contains(grants.Body.String(), item.ID) {
		t.Fatal(grants)
	}
	replay := call(s, "POST", "/v1/access-requests", body, "alina")
	if replay.Body.String() != created.Body.String() {
		t.Fatal("approval changed historical key response")
	}
	reconciled := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "alina")
	if reconciled.Code != 200 || !strings.Contains(reconciled.Body.String(), `"status":"created"`) {
		t.Fatal("reconcile replaced original operation result", reconciled)
	}
	if w := call(s, "POST", "/v1/access-requests/"+item.ID+"/cancel", fmt.Sprintf(`{"operation_key":%q,"expected_version":3}`, key()), "alina"); w.Code != 409 || errorCode(t, w) != "INVALID_STATE" {
		t.Fatal("approved access revoked by cancel", w)
	}
}
func TestHiddenObjectIsRemovedFromReadsAndReplays(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	operationKey := key()
	body := createBody(operationKey, "monitoring", "read", "Для демонстрационных задач стажировки")
	created := call(s, "POST", "/v1/access-requests", body, "alina")
	if created.Code != 201 {
		t.Fatal(created)
	}
	var object AccessRequest
	_ = json.Unmarshal(created.Body.Bytes(), &object)
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE resources SET payload=jsonb_set(payload,'{kind}','"hidden"') WHERE id='monitoring'`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/access-requests/" + object.ID, "/v1/access-requests?resource_id=monitoring", "/v1/grants?resource_id=monitoring"} {
		if w := call(s, "GET", path, "", "alina"); w.Code != 404 {
			t.Fatal(path, w)
		}
	}
	if w := call(s, "GET", "/v1/access-requests", "", "alina"); w.Code != 200 || strings.Contains(w.Body.String(), object.ID) {
		t.Fatal(w)
	}
	if w := call(s, "POST", "/v1/access-requests", body, "alina"); w.Code != 404 {
		t.Fatal("hidden replay leaked", w)
	}
	if w := call(s, "POST", "/v1/operations/"+operationKey+"/reconcile", `{}`, "alina"); w.Code != 404 {
		t.Fatal("hidden reconcile leaked", w)
	}
}
func TestTransactionFailuresDoNotCreateFalseUnavailable(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if _, err := s.Store.Pool.Exec(ctx, `CREATE FUNCTION fail_operation_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER fail_operation_insert BEFORE INSERT ON operations FOR EACH ROW EXECUTE FUNCTION fail_operation_insert()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Store.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_operation_insert ON operations;DROP FUNCTION IF EXISTS fail_operation_insert()`)
	})
	operationKey := key()
	w := call(s, "POST", "/v1/access-requests", createBody(operationKey, "monitoring", "read", "Для демонстрационных задач стажировки"), "alina")
	if w.Code != 503 {
		t.Fatal(w)
	}
	var count int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM access_requests WHERE user_id='alina' AND resource_id='monitoring'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial creation before journal", count, err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `DROP TRIGGER fail_operation_insert ON operations;DROP FUNCTION fail_operation_insert();CREATE FUNCTION fail_operation_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic commit failure'; END $$;CREATE CONSTRAINT TRIGGER fail_operation_commit AFTER INSERT ON operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_operation_commit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Store.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_operation_commit ON operations;DROP FUNCTION IF EXISTS fail_operation_commit()`)
	})
	w = call(s, "POST", "/v1/access-requests", createBody(key(), "monitoring", "read", "Для демонстрационных задач стажировки"), "alina")
	if w.Code != 500 || errorCode(t, w) != "SERVICE_ERROR" {
		t.Fatal("ambiguous commit advertised as guaranteed unavailability", w)
	}
}
