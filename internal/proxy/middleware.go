package egproxy

import (
	"context"
	"net/http"
)

type HandlerMiddleware interface {
	ServerStart(ctx context.Context)
	ServerShutdown()
	WrapHandler(next http.Handler) http.Handler
}
