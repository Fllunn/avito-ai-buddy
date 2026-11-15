package catalog

// Тест-доказательство ревью изоляции (output/isolation-review), п.8: Redis-кэш
// каталога. Кладётся во временную копию: backend/internal/catalog/isoreview_catalog_test.go.

import (
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type isoKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (k *isoKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.data[key]
	if !ok {
		return nil, authz.ErrNotFound
	}
	return v, nil
}
func (k *isoKV) Set(_ context.Context, key string, v []byte, _ time.Duration) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.data[key] = v
	return nil
}

type isoSource struct{ calls map[string]int }

func (s isoSource) Profile(_ context.Context, id string) (demo.Profile, error) {
	for _, p := range demo.Profiles() {
		if p.ID == id {
			return p, nil
		}
	}
	return demo.Profile{}, authz.ErrNotFound
}
func (s isoSource) Resources(_ context.Context, p demo.Profile) ([]demo.Resource, error) {
	s.calls[p.ID]++
	items := demo.Resources()
	for i := range items {
		if items[i].ApproverID == "manager" && p.ManagerID != nil {
			items[i].ApproverID = *p.ManagerID
		}
	}
	return items, nil
}

func TestIsoCatalogCacheIsPerUser(t *testing.T) {
	kv := &isoKV{data: map[string][]byte{}}
	src := isoSource{calls: map[string]int{}}
	d := Directory{Source: src, Cache: kv}
	ctx := context.Background()
	alina, err := d.Resources(ctx, "alina")
	if err != nil {
		t.Fatal(err)
	}
	maxim, err := d.Resources(ctx, "maxim")
	if err != nil {
		t.Fatal(err)
	}
	if len(kv.data) != 2 {
		t.Fatalf("expected one key per user scope, got %d", len(kv.data))
	}
	for key, value := range kv.data {
		if strings.Contains(key, "alina") || strings.Contains(key, "maxim") {
			t.Errorf("cache key exposes user id: %s", key)
		}
		var e envelope
		_ = json.Unmarshal(value, &e)
		t.Logf("key=%s… user=%s team=%s items=%d", key[:30], e.UserID, e.TeamID, len(e.Items))
		for _, r := range e.Items {
			if r.Kind == "hidden" {
				t.Errorf("hidden resource cached")
			}
		}
	}
	approver := func(items []demo.Resource) string {
		for _, r := range items {
			if r.ID == "launch-plans" {
				return r.ApproverID
			}
		}
		return ""
	}
	if approver(alina) != "olga" || approver(maxim) != "anna" {
		t.Errorf("personal approver mixed: alina=%s maxim=%s", approver(alina), approver(maxim))
	}
	// Подмена: под ключом Алины лежит конверт Максима — не принимается.
	var alinaKey, maximKey string
	for key, value := range kv.data {
		var e envelope
		_ = json.Unmarshal(value, &e)
		if e.UserID == "alina" {
			alinaKey = key
		} else {
			maximKey = key
		}
	}
	kv.data[alinaKey] = kv.data[maximKey]
	before := src.calls["alina"]
	again, err := d.Resources(ctx, "alina")
	if err != nil || approver(again) != "olga" || src.calls["alina"] != before+1 {
		t.Errorf("foreign envelope accepted from alina's key: approver=%s refetch=%d", approver(again), src.calls["alina"]-before)
	}
}
