package execution

import "context"

type routeKey struct{}

func WithRoute(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, routeKey{}, id)
}
func RouteID(ctx context.Context) string { id, _ := ctx.Value(routeKey{}).(string); return id }
