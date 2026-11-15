package service

// Контрольные проверки изоляции имитаций (output/security-review).
// Кладётся во временную копию: mocks/internal/service/secreview_mocks_test.go.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecReviewMocksIsolation(t *testing.T) {
	s := fixture(t)
	own := create(t, s, "alina", "monitoring")
	k := key()
	first := call(s, "POST", "/v1/access-requests", createBody(k, "space-payments", "read", "Для задач демонстрационной стажировки"), "alina")
	if first.Code != 201 {
		t.Fatal(first.Body.String())
	}
	checks := []struct {
		name, method, path, body, user string
		want                           int
	}{
		{"foreign-get", "GET", "/v1/access-requests/" + own.ID, "", "maxim", 404},
		{"foreign-cancel", "POST", "/v1/access-requests/" + own.ID + "/cancel", `{"operation_key":"` + key() + `","expected_version":1}`, "maxim", 404},
		{"foreign-key-replay", "POST", "/v1/access-requests", createBody(k, "space-payments", "read", "Для задач демонстрационной стажировки"), "maxim", 404},
		{"foreign-key-reconcile", "POST", "/v1/operations/" + k + "/reconcile", `{}`, "maxim", 404},
		{"buddy-create", "POST", "/v1/access-requests", createBody(key(), "monitoring", "read", "Для задач демонстрационной стажировки"), "igor", 403},
		{"hidden-create", "POST", "/v1/access-requests", createBody(key(), "salary-reports", "read", "Для задач демонстрационной стажировки"), "maxim", 404},
		{"hidden-list", "GET", "/v1/access-requests?resource_id=salary-reports", "", "alina", 404},
		{"no-secret", "GET", "/v1/resources", "", "", 401},
	}
	for _, c := range checks {
		w := call(s, c.method, c.path, c.body, c.user)
		t.Logf("%-22s -> %d %s", c.name, w.Code, strings.TrimSpace(w.Body.String()))
		if w.Code != c.want || strings.Contains(w.Body.String(), "alina") || strings.Contains(w.Body.String(), "Отчёты") {
			t.Fatalf("%s: got %d", c.name, w.Code)
		}
	}
	// Браузерный запрос с Origin не принимается даже с верным секретом.
	r := httptest.NewRequest("GET", "/v1/resources", nil)
	r.Header.Set("Authorization", "Bearer "+s.Config.Secret)
	r.Header.Set("X-Actor-ID", "alina")
	r.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	t.Logf("origin-with-secret     -> %d", w.Code)
	if w.Code != 401 {
		t.Fatal("browser-originated call accepted")
	}
	var list struct {
		Items []Resource `json:"items"`
	}
	_ = json.Unmarshal(call(s, "GET", "/v1/resources", "", "alina").Body.Bytes(), &list)
	for _, item := range list.Items {
		if item.ID == "salary-reports" {
			t.Fatal("hidden resource listed")
		}
	}
}
