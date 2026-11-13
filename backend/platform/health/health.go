package health

import (
	"encoding/json"
	"net/http"
)

// Handler exposes process liveness, never application readiness during bootstrap.
func Handler(service string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Service string `json:"service"`
			Status  string `json:"status"`
			Stage   string `json:"stage"`
		}{service, "alive", "bootstrap"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("{\"status\":\"not_ready\",\"code\":\"BOOTSTRAP_ONLY\"}\n"))
	})
	return mux
}
