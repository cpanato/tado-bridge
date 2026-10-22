// Package httpapi serves cached data only; it has no upstream client dependency.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/store"
)

type Options struct {
	MaxSnapshotAge time.Duration
	APIToken       string
	Now            func() time.Time
	MetricsHandler http.Handler
	History        history.Reader
}

func New(cache *store.Store, options Options) http.Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !get(w, r) {
			return
		}
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !get(w, r) {
			return
		}
		view, ok := cache.Load()
		if !ok {
			failure(w, http.StatusServiceUnavailable, "snapshot_unavailable", "No successful collection yet.")
			return
		}
		if !view.Usable {
			failure(w, http.StatusServiceUnavailable, "snapshot_unusable", "No climate measurements are available.")
			return
		}
		age := options.Now().Sub(view.CollectedAt)
		if age < 0 || age > options.MaxSnapshotAge {
			failure(w, http.StatusServiceUnavailable, "snapshot_stale", "The last successful collection is too old.")
			return
		}
		respond(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("/api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !get(w, r) {
			return
		}
		if !authenticate(w, r, options.APIToken) {
			return
		}
		view, ok := cache.Load()
		if !ok {
			failure(w, http.StatusServiceUnavailable, "snapshot_unavailable", "No successful collection yet.")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(view.JSON)
	})
	if options.MetricsHandler != nil {
		mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			if !get(w, r) || !authenticate(w, r, options.APIToken) {
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			options.MetricsHandler.ServeHTTP(w, r)
		})
	}
	if options.History != nil {
		mux.HandleFunc("/api/v1/history", func(w http.ResponseWriter, r *http.Request) {
			if !get(w, r) || !authenticate(w, r, options.APIToken) {
				return
			}
			query, err := historyQuery(r)
			if err != nil {
				failure(w, http.StatusBadRequest, "invalid_history_query", "Use RFC3339 from/to, a range of at most 31 days and limit between 1 and 100.")
				return
			}
			page, err := options.History.Query(r.Context(), query)
			if err != nil {
				failure(w, http.StatusServiceUnavailable, "history_unavailable", "Historical data is temporarily unavailable.")
				return
			}
			respond(w, http.StatusOK, page)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		failure(w, http.StatusNotFound, "not_found", "Unknown endpoint.")
	})
	return mux
}

func authenticate(w http.ResponseWriter, r *http.Request, token string) bool {
	if token == "" || authorized(r, token) {
		return true
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	failure(w, http.StatusUnauthorized, "unauthorized", "A valid bearer token is required.")
	return false
}

func authorized(r *http.Request, token string) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, value, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	actual, expected := sha256.Sum256([]byte(value)), sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func get(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	failure(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported.")
	return false
}

func failure(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
