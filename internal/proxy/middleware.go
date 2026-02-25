package egproxy

import (
	"context"
	"net/http"
)

// HandlerMiddleware is the base interface for all middlewares.
type HandlerMiddleware interface {
	WrapHandler(next http.Handler) http.Handler
	Equal(other any) bool
	String() string
}

// LifecycleMiddleware is an optional interface for middlewares that need
// startup/shutdown hooks (e.g. background goroutines, cleanup timers).
type LifecycleMiddleware interface {
	ServerStart(ctx context.Context)
	ServerShutdown()
}
