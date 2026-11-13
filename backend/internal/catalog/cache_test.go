package catalog

import (
	"ai-buddy/backend/platform/demo"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

type fakeSource struct {
	profiles     map[string]demo.Profile
	reads        int
	fail, hidden bool
}

func source() *fakeSource {
	s := &fakeSource{profiles: map[string]demo.Profile{}}
	for _, p := range demo.Profiles() {
		s.profiles[p.ID] = p
	}
	return s
}
func (s *fakeSource) Profile(_ context.Context, id string) (demo.Profile, error) {
	p, ok := s.profiles[id]
	if !ok {
		return p, errors.New("missing")
	}
	return p, nil
}
func (s *fakeSource) Resources(context.Context, demo.Profile) ([]demo.Resource, error) {
	s.reads++
	if s.fail {
		return nil, errors.New("private source failure")
	}
	r := demo.Resources()
	if s.hidden {
		r[0].Kind = "hidden"
	}
	return r, nil
}

type fakeKV struct {
	items map[string][]byte
	fail  bool
	last  string
}

func (k *fakeKV) Get(_ context.Context, key string) ([]byte, error) {
	k.last = key
	if k.fail {
		return nil, errors.New("unavailable")
	}
	value, ok := k.items[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return value, nil
}
func (k *fakeKV) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	if k.fail {
		return errors.New("unavailable")
	}
	k.items[key] = append([]byte(nil), value...)
	return nil
}
func TestIdentityPolicyAndFallback(t *testing.T) {
	ctx := context.Background()
	s := source()
	kv := &fakeKV{items: map[string][]byte{}}
	d := Directory{s, kv}
	a, err := d.Resources(ctx, "alina")
	if err != nil || len(a) != 10 || s.reads != 1 {
		t.Fatal(a, err)
	}
	keyA := kv.last
	if _, err := d.Resources(ctx, "alina"); err != nil || s.reads != 1 {
		t.Fatal("cache miss", err)
	}
	if _, err := d.Resources(ctx, "maxim"); err != nil || s.reads != 2 || keyA == kv.last {
		t.Fatal("identity scope shared", err)
	}
	// Even an accidental cross-key copy cannot disclose the other scope.
	kv.items[keyA] = kv.items[kv.last]
	if _, err := d.Resources(ctx, "alina"); err != nil || s.reads != 3 {
		t.Fatal("foreign envelope accepted", err)
	}
	p := s.profiles["alina"]
	p.PolicyVersion++
	s.profiles["alina"] = p
	s.hidden = true
	a, err = d.Resources(ctx, "alina")
	if err != nil || len(a) != 9 || s.reads != 4 {
		t.Fatal("policy invalidation missed", a, err)
	}
	for _, r := range a {
		if r.Kind == "hidden" || r.Kind == "closed" && (r.ApproverID != "" || len(r.Levels) != 0) {
			t.Fatal("restricted fields leaked", r)
		}
	}
	kv.fail = true
	if _, err := d.Resources(ctx, "alina"); err != nil || s.reads != 5 {
		t.Fatal("Redis failure prevented source read", err)
	}
	s.fail = true
	if _, err := d.Resources(ctx, "alina"); err != ErrSource {
		t.Fatal("source diagnostic leaked", err)
	}
	p.Enabled = false
	s.profiles["alina"] = p
	if _, err := d.Resources(ctx, "alina"); err != ErrSource {
		t.Fatal("disabled identity read cache")
	}
	if _, err := d.Resources(ctx, "missing"); err != ErrSource {
		t.Fatal(err)
	}
}
func TestCorruptCacheAndNoCache(t *testing.T) {
	ctx := context.Background()
	s := source()
	kv := &fakeKV{items: map[string][]byte{}}
	d := Directory{s, kv}
	if _, err := d.Resources(ctx, "alina"); err != nil {
		t.Fatal(err)
	}
	kv.items[kv.last] = []byte(`invalid`)
	if _, err := d.Resources(ctx, "alina"); err != nil || s.reads != 2 {
		t.Fatal(err)
	}
	if _, err := (Directory{Source: s}).Resources(ctx, "alina"); err != nil || s.reads != 3 {
		t.Fatal(err)
	}
}
func TestRedisAdapter(t *testing.T) {
	address := os.Getenv("TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("make coverage starts Redis")
	}
	r := NewRedis(address)
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := r.Get(ctx, "missing"); err == nil {
		t.Fatal("missing key")
	}
	if err := r.Set(ctx, "fixture", []byte("allowed"), time.Second); err != nil {
		t.Fatal(err)
	}
	value, err := r.Get(ctx, "fixture")
	if err != nil || string(value) != "allowed" {
		t.Fatal(string(value), err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "fixture"); err == nil {
		t.Fatal("closed Redis")
	}
	if err := r.Set(ctx, "fixture", nil, time.Second); err == nil {
		t.Fatal("closed Redis")
	}
}
