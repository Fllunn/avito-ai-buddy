package agent

import (
	"context"
	"time"
)

type CallRecorder func(context.Context, string, string, string, int64)
type Metered struct {
	Provider Provider
	Record   CallRecorder
}

func (p Metered) Mode() string { return p.Provider.Mode() }
func (p Metered) Recognize(ctx context.Context, frame Context, query string) ([]byte, error) {
	return p.call(ctx, frame, "recognize", func() ([]byte, error) { return p.Provider.Recognize(ctx, frame, query) })
}
func (p Metered) SelectFragments(ctx context.Context, frame Context, query string) ([]byte, error) {
	return p.call(ctx, frame, "fragments", func() ([]byte, error) { return p.Provider.SelectFragments(ctx, frame, query) })
}
func (p Metered) call(ctx context.Context, frame Context, operation string, call func() ([]byte, error)) ([]byte, error) {
	start := time.Now()
	data, e := call()
	code := "ok"
	if e != nil {
		code = "error"
	}
	if ctx.Err() != nil {
		code = "timeout"
	}
	if p.Record != nil {
		p.Record(ctx, frame.Profile.ID, p.Mode()+":"+operation, code, time.Since(start).Milliseconds())
	}
	return data, e
}
