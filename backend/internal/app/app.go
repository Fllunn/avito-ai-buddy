package app

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/api"
	"ai-buddy/backend/internal/catalog"
	"ai-buddy/backend/internal/integrations"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/internal/tools"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/config"
	"ai-buddy/backend/platform/demo"
	"context"
	"net/http"
	"sync"
	"time"
)

type directorySource struct {
	store  *storage.Store
	remote integrations.Client
}

func (s directorySource) Profile(ctx context.Context, user string) (demo.Profile, error) {
	return s.store.Profile(ctx, user)
}
func (s directorySource) Resources(ctx context.Context, p demo.Profile) ([]demo.Resource, error) {
	return s.remote.Directory(ctx, p)
}
func Factory(ctx context.Context, cfg config.Config) (http.Handler, func(), error) {
	store, err := storage.Open(ctx, cfg.DatabaseURL, cfg.Location)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		return nil, nil, err
	}
	if cfg.DemoMode {
		if err := store.Seed(ctx); err != nil {
			store.Close()
			return nil, nil, err
		}
	}
	cache := catalog.NewRedis(cfg.RedisAddr)
	remote := integrations.Client{BaseURL: cfg.MockURL, Secret: cfg.ServiceSecret, Timeout: cfg.ServiceTimeout}
	service := tools.Client{Authority: store, Remote: remote, Clock: cfg.Clock, Location: cfg.Location}
	var provider agent.Provider = agent.Fake{}
	if cfg.LLMProvider == "openrouter" {
		provider = agent.OpenRouter{Model: cfg.LLMModel, Key: cfg.OpenRouterKey}
	}
	provider = agent.Metered{Provider: provider, Record: store.RecordCall}
	directory := catalog.Directory{Source: directorySource{store, remote}, Cache: cache}
	service.Source = store
	service.Directory = directory
	business := &api.Business{Store: store, Tools: service, Builder: agent.Builder{Source: store, Directory: directory}, Provider: provider, LLMTimeout: cfg.LLMTimeout, CardTTL: cfg.CardTTL, StepTTL: cfg.StepConfirmationTTL}
	handler := api.New(store, api.Config{Demo: cfg.DemoMode, Origin: cfg.AllowedOrigin, Clock: cfg.Clock, SessionTTL: cfg.SessionTTL, Location: cfg.Location, Business: business})
	// Initialization's context is short lived. Runtime ends with the returned
	// cleanup, which waits for workers before releasing their connection pool.
	runtime, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	worker := actions.Worker{BusinessClock: cfg.Clock, Location: cfg.Location, Store: store, Client: service, Timeout: cfg.ServiceTimeout, Lease: cfg.ServiceTimeout + 10*time.Second, Delays: cfg.ReconcileDelays}
	wg.Go(func() { _ = worker.Run(runtime, clock.System{}, 100*time.Millisecond) })
	wg.Go(func() {
		for runtime.Err() == nil {
			_ = business.ProcessOnce(runtime, time.Now().UTC())
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-runtime.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	})
	var once sync.Once
	cleanup := func() { once.Do(func() { cancel(); wg.Wait(); cache.Close(); store.Close() }) }
	return handler, cleanup, nil
}
