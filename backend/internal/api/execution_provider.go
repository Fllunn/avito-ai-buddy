package api

import (
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/storage"
	"context"
	"strconv"
	"sync/atomic"
	"time"
)

type executionProvider struct {
	agent.Provider
	store *storage.Store
	work  storage.MessageWork
	calls atomic.Int64
}

func (p *executionProvider) Recognize(ctx context.Context, f agent.Context, q string) ([]byte, error) {
	return p.call(ctx, "recognize", func() ([]byte, error) { return p.Provider.Recognize(ctx, f, q) })
}
func (p *executionProvider) SelectFragments(ctx context.Context, f agent.Context, q string) ([]byte, error) {
	return p.call(ctx, "fragments", func() ([]byte, error) { return p.Provider.SelectFragments(ctx, f, q) })
}
func (p *executionProvider) call(ctx context.Context, operation string, call func() ([]byte, error)) ([]byte, error) {
	key := operation + ":" + strconv.FormatInt(p.calls.Add(1), 10)
	_ = p.store.ObserveMessage(ctx, p.work, "model", "running", key)
	data, e := call()
	state := "succeeded"
	if e != nil {
		state = "failed"
	}
	// A timed-out/late callback must still pass the worker lease guard.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	_ = p.store.ObserveMessage(finishCtx, p.work, "model", state, key)
	return data, e
}
