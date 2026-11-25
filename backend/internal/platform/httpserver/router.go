package httpserver

import (
	"net/http"

	"bettercontent/backend/internal/transport"
)

func NewHandler(database Pinger, auth transport.ServerInterface, developmentHandlers ...http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, handler := range developmentHandlers {
		if handler != nil {
			mux.Handle("/_dev/assets/", handler)
		}
	}
	transport.HandlerWithOptions(auth, transport.StdHTTPServerOptions{
		BaseRouter: mux,
		BaseURL:    "/api/v1",
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
		},
	})
	health := NewHealthHandler(database)
	mux.Handle("GET /health/live", health)
	mux.Handle("GET /health/ready", health)
	return WithRequestID(mux)
}
