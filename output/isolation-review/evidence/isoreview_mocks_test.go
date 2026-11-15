package service

// Тесты-доказательства ревью изоляции (output/isolation-review), п.7: имитации.
// Кладутся во временную копию: mocks/internal/service/isoreview_mocks_test.go.
// Дополняют TestSecReviewMocksIsolation: здесь списки, X-Actor-ID, отгулы,
// персональный каталог и ключи операций двух стажёров одновременно.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestIsoMocksPerActorProjections(t *testing.T) {
	s := fixture(t)
	t.Cleanup(func() { _, _ = s.Store.Pool.Exec(context.Background(), `DELETE FROM time_off_requests`) })
	// Оба стажёра создают заявки одновременно.
	var wg sync.WaitGroup
	results := map[string][]string{}
	var mu sync.Mutex
	for _, c := range []struct{ user, resource, day string }{{"alina", "space-payments", "2026-11-16"}, {"maxim", "tracker-srch", "2026-11-17"}, {"alina", "monitoring", ""}, {"maxim", "monitoring", ""}} {
		wg.Add(1)
		go func(user, resource, day string) {
			defer wg.Done()
			w := call(s, "POST", "/v1/access-requests", createBody(key(), resource, "read", "Для задач демонстрационной стажировки"), user)
			var a AccessRequest
			_ = json.Unmarshal(w.Body.Bytes(), &a)
			if w.Code != 201 {
				t.Errorf("%s access %s: %d %s", user, resource, w.Code, w.Body.String())
			}
			ids := []string{a.ID}
			if day != "" {
				w := call(s, "POST", "/v1/time-off-requests", absenceBody(key(), day, day), user)
				var r TimeOffRequest
				_ = json.Unmarshal(w.Body.Bytes(), &r)
				if w.Code != 201 {
					t.Errorf("%s absence %d %s", user, w.Code, w.Body.String())
				}
				ids = append(ids, r.ID)
			}
			mu.Lock()
			results[user] = append(results[user], ids...)
			mu.Unlock()
		}(c.user, c.resource, c.day)
	}
	wg.Wait()
	t.Logf("created: %v", results)
	for _, pair := range [][2]string{{"alina", "maxim"}, {"maxim", "alina"}} {
		me, other := pair[0], pair[1]
		for _, path := range []string{"/v1/access-requests", "/v1/time-off-requests", "/v1/grants", "/v1/access-requests?resource_id=monitoring"} {
			body := call(s, "GET", path, "", me).Body.String()
			if strings.Contains(body, `"user_id":"`+other+`"`) {
				t.Errorf("%s list %s contains %s", me, path, other)
			}
			for _, id := range results[other] {
				if strings.Contains(body, `"`+id+`"`) {
					t.Errorf("%s list %s contains %s's %s", me, path, other, id)
				}
			}
		}
		for _, id := range results[other] {
			path := "/v1/access-requests/" + id
			if strings.HasPrefix(id, "ABS-") {
				path = "/v1/time-off-requests/" + id
			}
			missing := call(s, "GET", strings.Replace(path, id, id[:4]+"999999", 1), "", me)
			w := call(s, "GET", path, "", me)
			if w.Code != 404 || errorCode(t, w) != errorCode(t, missing) {
				t.Errorf("%s reads %s's %s: %d", me, other, id, w.Code)
			}
			cancel := call(s, "POST", path+"/cancel", `{"operation_key":"`+key()+`","expected_version":1}`, me)
			if cancel.Code != 404 {
				t.Errorf("%s cancels %s's %s: %d", me, other, id, cancel.Code)
			}
		}
	}
	// Грант Максима из сида не виден Алине.
	if strings.Contains(call(s, "GET", "/v1/grants", "", "alina").Body.String(), "ACC-1001") {
		t.Error("alina sees maxim's seeded grant")
	}
	// Персональный каталог: согласующий «manager» подставляется для каждого свой.
	approver := func(user string) string {
		var list struct {
			Items []Resource `json:"items"`
		}
		_ = json.Unmarshal(call(s, "GET", "/v1/resources", "", user).Body.Bytes(), &list)
		for _, r := range list.Items {
			if r.ID == "launch-plans" && r.ApproverID != nil {
				return *r.ApproverID
			}
		}
		return ""
	}
	if approver("alina") != "olga" || approver("maxim") != "anna" {
		t.Errorf("approver projection mixed: alina=%s maxim=%s", approver("alina"), approver("maxim"))
	}
}

func TestIsoMocksActorHeaderIsExact(t *testing.T) {
	s := fixture(t)
	for _, actor := range []string{"", "Alina", " alina", "alina ", "alina,maxim", "ALINA", "alina\x00", "unknown"} {
		r := httptest.NewRequest("GET", "/v1/access-requests", nil)
		r.Header.Set("Authorization", "Bearer "+s.Config.Secret)
		r.Header.Set("X-Actor-ID", actor)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		t.Logf("X-Actor-ID %q -> %d", actor, w.Code)
		if w.Code < 400 || strings.Contains(w.Body.String(), "ACC-") {
			t.Errorf("X-Actor-ID %q accepted: %d %s", actor, w.Code, w.Body.String())
		}
	}
	// Два заголовка X-Actor-ID: используется только первый, второй не расширяет права.
	create(t, s, "maxim", "monitoring")
	r := httptest.NewRequest("GET", "/v1/access-requests", nil)
	r.Header.Set("Authorization", "Bearer "+s.Config.Secret)
	r.Header.Add("X-Actor-ID", "alina")
	r.Header.Add("X-Actor-ID", "maxim")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), `"user_id":"maxim"`) {
		t.Errorf("second X-Actor-ID header widened scope: %s", w.Body.String())
	}
}

// Ключи операций: чужой ключ не раскрывает результат. Неизвестный ключ при сверке
// «застолбивается» за вызывающим — наблюдение для отчёта (P3), а не утечка:
// ключи генерирует только API (UUID v4) и никому, кроме владельца, не показывает.
func TestIsoMocksOperationKeysPerActor(t *testing.T) {
	s := fixture(t)
	k := key()
	if w := call(s, "POST", "/v1/access-requests", createBody(k, "space-payments", "read", "Для задач демонстрационной стажировки"), "alina"); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	foreign := call(s, "POST", "/v1/operations/"+k+"/reconcile", `{}`, "maxim")
	unknown := call(s, "POST", "/v1/operations/"+key()+"/reconcile", `{}`, "maxim")
	t.Logf("reconcile foreign key -> %d %s; unknown key -> %d %s", foreign.Code, strings.TrimSpace(foreign.Body.String()), unknown.Code, strings.TrimSpace(unknown.Body.String()))
	if foreign.Code != 404 || strings.Contains(foreign.Body.String(), "space-payments") || strings.Contains(foreign.Body.String(), "alina") {
		t.Errorf("foreign key reveals result: %d", foreign.Code)
	}
	// Ключ, «закрытый» Максимом, не позволяет ему увидеть будущую заявку Алины.
	claimed := key()
	call(s, "POST", "/v1/operations/"+claimed+"/reconcile", `{}`, "maxim")
	late := call(s, "POST", "/v1/access-requests", createBody(claimed, "monitoring", "read", "Для задач демонстрационной стажировки"), "alina")
	t.Logf("alina create with key closed by maxim -> %d %s", late.Code, strings.TrimSpace(late.Body.String()))
	if late.Code == 201 {
		if strings.Contains(call(s, "GET", "/v1/access-requests", "", "maxim").Body.String(), "monitoring") {
			t.Error("maxim sees alina's request through a claimed key")
		}
	}
}
