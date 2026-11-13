package app

import (
	"ai-buddy/backend/internal/api"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/platform/config"
	"context"
	"net/http"
)

func Factory(ctx context.Context, cfg config.Config) (http.Handler, func(), error) {
	store, err := storage.Open(ctx, cfg.DatabaseURL)
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
	return api.New(store, api.Config{Demo: cfg.DemoMode, Origin: cfg.AllowedOrigin, Clock: cfg.Clock, SessionTTL: cfg.SessionTTL, Location: cfg.Location}), store.Close, nil
}
