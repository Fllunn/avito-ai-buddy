package catalog

import (
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/demo"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"time"
)

type KV interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
}
type Source interface {
	Profile(context.Context, string) (demo.Profile, error)
	Resources(context.Context, demo.Profile) ([]demo.Resource, error)
}
type Directory struct {
	Source Source
	Cache  KV
}
type envelope struct {
	UserID  string
	Version int64
	Role    string
	TeamID  string
	Items   []demo.Resource
}

var ErrSource = errors.New("DIRECTORY_UNAVAILABLE")

func (d Directory) Resources(ctx context.Context, user string) ([]demo.Resource, error) {
	p, err := d.Source.Profile(ctx, user)
	if err != nil || !p.Enabled || p.ID != user {
		return nil, ErrSource
	}
	scope := envelope{UserID: p.ID, Version: p.PolicyVersion, Role: p.Role}
	if p.TeamID != nil {
		scope.TeamID = *p.TeamID
	}
	raw, _ := json.Marshal(scope)
	sum := sha256.Sum256(raw)
	key := "buddy:directory:" + hex.EncodeToString(sum[:])
	if d.Cache != nil {
		data, err := d.Cache.Get(ctx, key)
		if err == nil {
			var saved envelope
			if json.Unmarshal(data, &saved) == nil && saved.UserID == scope.UserID && saved.Version == scope.Version && saved.Role == scope.Role && saved.TeamID == scope.TeamID && saved.Items != nil {
				current, e := d.Source.Profile(ctx, user)
				if e != nil || !authz.SameScope(p, current) {
					return nil, ErrSource
				}
				return visible(saved.Items), nil
			}
		}
	}
	resources, err := d.Source.Resources(ctx, p)
	if err != nil {
		return nil, ErrSource
	}
	current, e := d.Source.Profile(ctx, user)
	if e != nil || !authz.SameScope(p, current) {
		return nil, ErrSource
	}
	scope.Items = visible(resources)
	if d.Cache != nil {
		if data, err := json.Marshal(scope); err == nil {
			_ = d.Cache.Set(ctx, key, data, time.Minute)
		}
	}
	return scope.Items, nil
}
func visible(items []demo.Resource) []demo.Resource {
	result := []demo.Resource{}
	for _, r := range items {
		if r.Kind == "hidden" {
			continue
		}
		if r.Kind == "closed" {
			r = demo.Resource{ID: r.ID, Name: r.Name, Kind: r.Kind, Levels: []string{}, RecommendedTeams: []string{}, Synonyms: []string{}}
		}
		result = append(result, r)
	}
	return result
}

type Redis struct{ client *redis.Client }

func NewRedis(address string) *Redis {
	return &Redis{redis.NewClient(&redis.Options{Addr: address, MaxRetries: -1, DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond, PoolTimeout: 200 * time.Millisecond, ContextTimeoutEnabled: true})}
}
func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	return r.client.Get(ctx, key).Bytes()
}
func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}
func (r *Redis) Close() error { return r.client.Close() }
