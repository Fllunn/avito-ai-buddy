package app

import (
	"ai-buddy/backend/platform/config"
	"ai-buddy/mocks/internal/service"
	"context"
	"fmt"
	"net/http"
)

func Factory(ctx context.Context, cfg config.Config) (http.Handler, func(), error) {
	if cfg.DemoMode && len(cfg.DemoAdminSecret) < 32 {
		return nil, nil, fmt.Errorf("DEMO_ADMIN_SECRET is required for the demo presenter")
	}
	store, err := service.Open(ctx, cfg.DatabaseURL)
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
	server := &service.Server{Store: store, Config: service.Config{Secret: cfg.ServiceSecret, Clock: cfg.Clock, Location: cfg.Location, Demo: cfg.DemoMode, PanelSecret: cfg.DemoAdminSecret, PanelOrigin: cfg.DemoPanelOrigin}}
	return server.Handler(), store.Close, nil
}
