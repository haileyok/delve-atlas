package agentapi

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// params reads and validates query parameters, remembering the first problem so a handler can
// parse everything and then check once.
type params struct {
	q   url.Values
	now time.Time
	err *apiError
}

func (a *API) params(r *http.Request) *params {
	return &params{q: r.URL.Query(), now: a.cfg.Now()}
}

func (p *params) fail(name, msg string) {
	if p.err == nil {
		p.err = badParam(name, msg)
	}
}

// Err is the first validation error, or nil.
func (p *params) Err() error {
	if p.err == nil {
		return nil
	}
	return p.err
}

func (p *params) str(name string) string { return strings.TrimSpace(p.q.Get(name)) }

func (p *params) required(name string) string {
	v := p.str(name)
	if v == "" {
		p.fail(name, "required")
	}
	return v
}

func (p *params) integer(name string, def, lo, hi int) int {
	s := p.str(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		p.fail(name, "must be an integer between "+strconv.Itoa(lo)+" and "+strconv.Itoa(hi))
		return def
	}
	return n
}

// optInt is an integer parameter that may be absent (ok=false).
func (p *params) optInt(name string) (int, bool) {
	s := p.str(name)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		p.fail(name, "must be an integer")
		return 0, false
	}
	return n, true
}

func (p *params) enum(name, def string, allowed ...string) string {
	s := p.str(name)
	if s == "" {
		return def
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	p.fail(name, "must be one of: "+strings.Join(allowed, ", "))
	return def
}

func (p *params) boolean(name string) *bool {
	switch strings.ToLower(p.str(name)) {
	case "":
		return nil
	case "true", "1", "yes":
		t := true
		return &t
	case "false", "0", "no":
		f := false
		return &f
	}
	p.fail(name, "must be true or false")
	return nil
}

// timeMS parses a time bound into Unix milliseconds (0 when absent). It accepts RFC 3339
// ("2026-10-05T12:00:00Z"), Unix seconds, or a relative age like "90m", "6h" or "3d" meaning
// that long before now.
func (p *params) timeMS(name string) int64 {
	s := p.str(name)
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1_000_000_000 {
		return n * 1000
	}
	if d, ok := parseAge(s); ok {
		return p.now.Add(-d).UnixMilli()
	}
	p.fail(name, `must be RFC 3339 (2026-10-05T12:00:00Z), Unix seconds, or a relative age such as "6h" or "3d"`)
	return 0
}

func parseAge(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || n < 0 {
		return 0, false
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(n * float64(time.Minute)), true
	case 'h':
		return time.Duration(n * float64(time.Hour)), true
	case 'd':
		return time.Duration(n * 24 * float64(time.Hour)), true
	}
	return 0, false
}

// ---- pagination

// cursor is opaque to clients: an offset into the result set.
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o" + strconv.Itoa(offset)))
}

func (p *params) cursor() int {
	s := p.str("cursor")
	if s == "" {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) < 2 || b[0] != 'o' {
		p.fail("cursor", "not a cursor this API issued; pass next_cursor from a previous response unchanged")
		return 0
	}
	n, err := strconv.Atoi(string(b[1:]))
	if err != nil || n < 0 {
		p.fail("cursor", "not a cursor this API issued; pass next_cursor from a previous response unchanged")
		return 0
	}
	return n
}

// page describes one page of a list response.
type page struct {
	limit, offset int
}

func (p *params) page(defLimit, maxLimit int) page {
	return page{limit: p.integer("limit", defLimit, 1, maxLimit), offset: p.cursor()}
}

// next is the cursor for the page after this one, or nil when the results end.
func (pg page) next(returned int, total int) *string {
	if total >= 0 && pg.offset+returned >= total {
		return nil
	}
	if returned < pg.limit {
		return nil
	}
	c := encodeCursor(pg.offset + returned)
	return &c
}

// listResponse is the shape of every paginated endpoint.
type listResponse[T any] struct {
	Snapshot   string  `json:"snapshot"`
	Total      *int    `json:"total,omitempty"`
	Results    []T     `json:"results"`
	NextCursor *string `json:"next_cursor"`
}

func newList[T any](snap string, results []T, total int, pg page) listResponse[T] {
	if results == nil {
		results = []T{}
	}
	l := listResponse[T]{Snapshot: snap, Results: results, NextCursor: pg.next(len(results), total)}
	if total >= 0 {
		l.Total = &total
	}
	return l
}
