package agentapi

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"
)

// vecIndex holds every embedded post's vector in memory so a query is one pass over a flat
// array (about 13k posts x 768 floats: a few milliseconds).
type vecIndex struct {
	builtAt time.Time
	dim     int
	uris    []string
	dids    []string
	created []int64
	reply   []bool
	vecs    []float32
	pos     map[string]int
}

type vecCache struct {
	mu  sync.Mutex
	idx *vecIndex
}

const vecIndexTTL = 5 * time.Minute

// vectors returns the in-memory index, rebuilding it when it is a few minutes old so newly
// embedded posts become searchable.
func (a *API) vectors(ctx context.Context) (*vecIndex, error) {
	c := &a.vecs
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx != nil && a.cfg.Now().Sub(c.idx.builtAt) < vecIndexTTL {
		return c.idx, nil
	}
	rows, err := a.cfg.DB.QueryContext(ctx, `
		SELECT e.uri, e.vec, p.did, p.created_at, p.reply_parent <> ''
		FROM embeddings e JOIN posts p ON p.uri = e.uri
		WHERE e.model = ? ORDER BY p.created_at`, a.cfg.ModelKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := &vecIndex{builtAt: a.cfg.Now(), pos: map[string]int{}}
	for rows.Next() {
		var uri, did string
		var blob []byte
		var created int64
		var reply bool
		if err := rows.Scan(&uri, &blob, &did, &created, &reply); err != nil {
			return nil, err
		}
		d := len(blob) / 4
		if idx.dim == 0 {
			idx.dim = d
		}
		if d != idx.dim || d == 0 {
			continue // a vector of another size can't be compared; skip it
		}
		idx.pos[uri] = len(idx.uris)
		idx.uris = append(idx.uris, uri)
		idx.dids = append(idx.dids, did)
		idx.created = append(idx.created, created)
		idx.reply = append(idx.reply, reply)
		for i := 0; i < d; i++ {
			idx.vecs = append(idx.vecs, math.Float32frombits(binary.LittleEndian.Uint32(blob[4*i:])))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c.idx = idx
	return idx, nil
}

func (x *vecIndex) vec(i int) []float32 { return x.vecs[i*x.dim : (i+1)*x.dim] }

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

type scored struct {
	i     int
	score float32
}

// rank scores every vector against q and returns those passing keep, best first.
func (x *vecIndex) rank(q []float32, keep func(i int) bool) []scored {
	out := make([]scored, 0, 512)
	for i := range x.uris {
		if keep != nil && !keep(i) {
			continue
		}
		out = append(out, scored{i, dot(q, x.vec(i))})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].score != out[b].score {
			return out[a].score > out[b].score
		}
		return out[a].i < out[b].i
	})
	return out
}

// searchDepth is how far down the ranking a client can page.
const searchDepth = 500

// ---- GET /api/v1/search

func (a *API) handleSearch(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	p := a.params(r)
	q := p.required("q")
	t, hasT := p.optInt("topic")
	rg, hasR := p.optInt("region")
	author := p.str("author")
	since, until := p.timeMS("since"), p.timeMS("until")
	reply := p.boolean("reply")
	pg := p.page(10, 100)
	if err := p.Err(); err != nil {
		return nil, err
	}
	if a.cfg.Embedder == nil {
		return nil, &apiError{Status: 503, Code: "semantic_unavailable", Message: "meaning-based search is not enabled on this server; use /api/v1/posts?q= for keyword search"}
	}
	if len([]rune(q)) > 1000 {
		return nil, badParam("q", "too long (at most 1000 characters)")
	}
	var did string
	if author != "" {
		if did, err = a.resolveAuthor(r.Context(), author); err != nil {
			return nil, err
		}
	}
	if hasT {
		if _, ok := snap.Topics[t]; !ok {
			return nil, notFound("no such topic in the current map")
		}
	}
	if hasR {
		if _, ok := snap.Regions[rg]; !ok {
			return nil, notFound("no such region in the current map")
		}
	}

	// Embed the query. A few at a time at most: each one is a model call.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	select {
	case a.embedSem <- struct{}{}:
		defer func() { <-a.embedSem }()
	case <-ctx.Done():
		return nil, &apiError{Status: 503, Code: "busy", Message: "search is busy; retry shortly"}
	}
	vs, err := a.cfg.Embedder.Embed(ctx, []string{q})
	if err != nil || len(vs) != 1 {
		a.cfg.Log.Error("embedding the query failed", "err", err)
		return nil, &apiError{Status: 503, Code: "semantic_unavailable", Message: "the embedding model is not reachable right now; use /api/v1/posts?q= for keyword search"}
	}
	idx, err := a.vectors(ctx)
	if err != nil {
		return nil, err
	}
	if idx.dim == 0 || len(vs[0]) != idx.dim {
		return nil, &apiError{Status: 503, Code: "semantic_unavailable", Message: "no compatible embeddings are available yet"}
	}

	keep := func(i int) bool {
		if did != "" && idx.dids[i] != did {
			return false
		}
		if since > 0 && idx.created[i] < since {
			return false
		}
		if until > 0 && idx.created[i] > until {
			return false
		}
		if reply != nil && idx.reply[i] != *reply {
			return false
		}
		if hasT || hasR {
			row, ok := snap.Idx[idx.uris[i]]
			if !ok {
				return false // newer than the map: it has no topic yet
			}
			if hasT && snap.C.Topic[row] != t {
				return false
			}
			if hasR && snap.C.Region[row] != rg {
				return false
			}
		}
		return true
	}
	ranked := idx.rank(vs[0], keep)
	if len(ranked) > searchDepth {
		ranked = ranked[:searchDepth]
	}
	total := len(ranked)
	lo := min(pg.offset, total)
	hi := min(lo+pg.limit, total)
	page := ranked[lo:hi]
	uris := make([]string, len(page))
	for i, s := range page {
		uris[i] = idx.uris[s.i]
	}
	rows, err := a.postsByURI(r.Context(), uris)
	if err != nil {
		return nil, err
	}
	views := snap.views(rows)
	byURI := make(map[string]float32, len(page))
	for _, s := range page {
		byURI[idx.uris[s.i]] = s.score
	}
	for i := range views {
		sc := float64(byURI[views[i].URI])
		views[i].Score = &sc
	}
	return newList(snap.ID, views, total, pg), nil
}

// similarTo returns the posts closest in meaning to the stored post uri (none if it has no
// embedding yet). It needs no model call: the post's own vector is the query.
func (a *API) similarTo(ctx context.Context, snap *Snapshot, uri string, k int) ([]PostView, error) {
	idx, err := a.vectors(ctx)
	if err != nil {
		return nil, err
	}
	at, ok := idx.pos[uri]
	if !ok {
		return []PostView{}, nil
	}
	ranked := idx.rank(idx.vec(at), func(i int) bool { return i != at })
	if len(ranked) > k {
		ranked = ranked[:k]
	}
	uris := make([]string, len(ranked))
	score := map[string]float32{}
	for i, s := range ranked {
		uris[i] = idx.uris[s.i]
		score[uris[i]] = s.score
	}
	rows, err := a.postsByURI(ctx, uris)
	if err != nil {
		return nil, err
	}
	views := snap.views(rows)
	for i := range views {
		sc := float64(score[views[i].URI])
		views[i].Score = &sc
	}
	return views, nil
}
