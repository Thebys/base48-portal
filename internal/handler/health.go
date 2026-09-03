package handler

import (
	"context"
	"net/http"
	"time"
)

// HealthHandler reports whether the process can still serve requests.
// Used by the container healthcheck and by any external monitoring.
//
// Deliberately unauthenticated and free of user data — it answers exactly
// one question: is the app up and is the database reachable.
func (h *Handler) HealthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	if err := h.db.PingContext(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"unhealthy","database":"unreachable"}`))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok","database":"ok"}`))
}
