// Package agentapi is the read-only JSON API for agents: everything the website shows, as
// stable, documented endpoints under /api/v1, plus the guide that explains them (/AGENTS.md).
package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/haileyok/delve-atlas/internal/store"
)

// Embedder turns text into vectors; it is only needed for meaning-based search.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// Config for the API.
type Config struct {
	DB       *store.DB
	AtlasDir string
	// Embedder enables /search. Nil disables it (the endpoint then answers 503).
	Embedder Embedder
	// ModelKey is the embeddings.model value the vectors were stored under.
	ModelKey string
	// Rate limits per client address: requests per second and burst; search is costlier.
	RPS, Burst             float64
	SearchRPS, SearchBurst float64
	// Activity computes live volume statistics for /api/v1/activity; nil omits that endpoint.
	Activity func(ctx context.Context, days int) (any, error)
	Log      *slog.Logger
	Now      func() time.Time
}

// API serves the agent endpoints.
type API struct {
	cfg      Config
	snaps    snapCache
	vecs     vecCache
	general  *limiter
	search   *limiter
	embedSem chan struct{} // at most a few query embeddings in flight
}

// New returns an API with defaults filled in.
func New(cfg Config) *API {
	if cfg.ModelKey == "" {
		cfg.ModelKey = "nomic-embed-text+thread1"
	}
	if cfg.RPS == 0 {
		cfg.RPS, cfg.Burst = 20, 60
	}
	if cfg.SearchRPS == 0 {
		cfg.SearchRPS, cfg.SearchBurst = 2, 8
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &API{
		cfg:      cfg,
		general:  newLimiter(cfg.RPS, int(cfg.Burst)),
		search:   newLimiter(cfg.SearchRPS, int(cfg.SearchBurst)),
		embedSem: make(chan struct{}, 4),
	}
}

// handlerFunc computes a response value or fails with an *apiError (anything else is a 500).
type handlerFunc func(r *http.Request) (any, error)

// route wraps fn with the API's conventions: CORS, per-client rate limiting, JSON output,
// the error envelope and a short shared-cache lifetime. costly routes share a tighter budget.
func (a *API) route(fn handlerFunc, costly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", "Retry-After")
		key := clientIP(r)
		lim := a.general
		if costly {
			lim = a.search
		}
		if !lim.allow(key) {
			h.Set("Retry-After", "1")
			writeError(w, &apiError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests; slow down and retry after a second"})
			return
		}
		v, err := fn(r)
		if err != nil {
			var ae *apiError
			if !errors.As(err, &ae) {
				a.cfg.Log.Error("agent api", "path", r.URL.Path, "err", err)
				ae = &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
			}
			writeError(w, ae)
			return
		}
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=60")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		if r.URL.Query().Get("pretty") != "" {
			enc.SetIndent("", "  ")
		}
		enc.Encode(v)
	})
}

// ---- errors

// apiError is the JSON error envelope: {"error": {"code", "message", "param"?}}.
type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func badParam(param, msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_parameter", Message: msg, Param: param}
}
func notFound(msg string) *apiError {
	return &apiError{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

func writeError(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// ---- rate limiting

type limiter struct {
	mu    sync.Mutex
	rps   rate.Limit
	burst int
	m     map[string]*limEntry
	clean time.Time
}

type limEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func newLimiter(rps float64, burst int) *limiter {
	return &limiter{rps: rate.Limit(rps), burst: burst, m: map[string]*limEntry{}, clean: time.Now()}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.clean) > 5*time.Minute { // forget clients we haven't seen for a while
		for k, e := range l.m {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.m, k)
			}
		}
		l.clean = now
	}
	e, ok := l.m[key]
	if !ok {
		e = &limEntry{l: rate.NewLimiter(l.rps, l.burst)}
		l.m[key] = e
	}
	e.seen = now
	return e.l.Allow()
}

// clientIP is the address to rate-limit by: Cloudflare's header when we are behind it,
// otherwise the first forwarded address, otherwise the socket's.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
