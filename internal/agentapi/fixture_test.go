package agentapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haileyok/delve-atlas/internal/embed"
	"github.com/haileyok/delve-atlas/internal/store"
)

// A tiny network: three accounts, a four-post conversation, a few reactions, and one post that
// is newer than the map.
//
//	alice  alice.example   p1 (root) "Agents debating append-only provenance records"
//	bob    bob.example     p2 reply to p1   p5 "Ducks are swimming in the pond"
//	carol  (no handle)     p3 reply to p2   p6 "Random numbers from the CSPRNG ..."
//	alice                  p4 reply to p3 "o7"          p7 "Newer post, not in the map yet"
const (
	snapID   = "20261008T120000Z"
	modelKey = "test-model"
)

var (
	didAlice = "did:plc:alice"
	didBob   = "did:plc:bob"
	didCarol = "did:plc:carol"
)

func uri(did, rkey string) string { return "at://" + did + "/town.delve.feed.post/" + rkey }

var (
	p1 = uri("did:plc:alice", "p1")
	p2 = uri("did:plc:bob", "p2")
	p3 = uri("did:plc:carol", "p3")
	p4 = uri("did:plc:alice", "p4")
	p5 = uri("did:plc:bob", "p5")
	p6 = uri("did:plc:carol", "p6")
	p7 = uri("did:plc:alice", "p7")
)

// now is the fixed clock: shortly after the newest mapped post.
var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func at(h float64) int64 { return now.Add(-time.Duration(h * float64(time.Hour))).UnixMilli() }

// vocabulary of the toy embedder: a text's vector counts these words.
var vocab = []string{"provenance", "receipts", "ducks", "pond", "random", "numbers", "agents", "records"}

func toyVector(text string) []float32 {
	v := make([]float32, len(vocab)+1) // the last slot: "none of the words above"
	low := strings.ToLower(text)
	var norm float64
	for i, w := range vocab {
		c := strings.Count(low, w)
		v[i] = float32(c)
		norm += float64(c * c)
	}
	if norm == 0 {
		v[len(vocab)] = 1 // keep it a unit vector
		return v
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(norm))
	}
	return v
}

type toyEmbedder struct{ err error }

func (e toyEmbedder) Embed(_ context.Context, in []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(in))
	for i, s := range in {
		out[i] = toyVector(s)
	}
	return out, nil
}

type fixture struct {
	api *API
	h   http.Handler
	db  *store.DB
}

func newFixture(t *testing.T, mutate ...func(*Config)) *fixture {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	post := func(u, did, text string, hoursAgo float64, parent, root string) store.Post {
		rk := u[strings.LastIndex(u, "/")+1:]
		return store.Post{URI: u, DID: did, Rkey: rk, Text: text, CreatedAt: at(hoursAgo), IndexedAt: at(hoursAgo), ReplyParent: parent, ReplyRoot: root}
	}
	var b store.Batch
	b.Posts = []store.Post{
		post(p1, didAlice, "Agents debating append-only provenance records", 30, "", ""),
		post(p2, didBob, "Provenance needs receipts", 29, p1, p1),
		post(p3, didCarol, "I agree, receipts all the way down", 28, p2, p1),
		post(p4, didAlice, "o7", 27, p3, p1),
		post(p5, didBob, "Ducks are swimming in the pond", 20, "", ""),
		post(p6, didCarol, "Random numbers from the CSPRNG: 0.5 0.3", 10, "", ""),
		post(p7, didAlice, "Newer post, not in the map yet: provenance again", 1, "", ""),
	}
	like := func(who, subject string, n int) store.Interaction {
		return store.Interaction{URI: "at://" + who + "/town.delve.feed.like/" + itoa(n), Kind: "like", DID: who, Subject: subject, CreatedAt: at(5)}
	}
	b.Interactions = []store.Interaction{
		like(didBob, p1, 1), like(didCarol, p1, 2), like(didAlice, p5, 3), like(didBob, p3, 4),
		{URI: "at://" + didCarol + "/town.delve.feed.repost/1", Kind: "repost", DID: didCarol, Subject: p1, CreatedAt: at(5)},
		{URI: "at://" + didBob + "/town.delve.graph.follow/1", Kind: "follow", DID: didBob, Subject: didAlice, CreatedAt: at(40)},
		{URI: "at://" + didAlice + "/town.delve.graph.follow/1", Kind: "follow", DID: didAlice, Subject: didBob, CreatedAt: at(40)},
		{URI: "at://" + didCarol + "/town.delve.graph.follow/1", Kind: "follow", DID: didCarol, Subject: didAlice, CreatedAt: at(40)},
	}
	b.Actors = []store.ActorUpdate{
		{DID: didAlice, Handle: "alice.example", DisplayName: "Alice", Description: "Studies provenance.", SeenAt: 1},
		{DID: didBob, Handle: "bob.example", DisplayName: "Bob", Description: "Keeps ducks.", SeenAt: 1},
		{DID: didCarol, DisplayName: "Carol", Description: "Counts things.", SeenAt: 1},
	}
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	// embeddings for every post, as the embed service would have stored them
	var uris []string
	var vecs [][]byte
	for _, p := range b.Posts {
		uris = append(uris, p.URI)
		vecs = append(vecs, embed.Pack(toyVector(p.Text)))
	}
	if err := db.PutEmbeddings(ctx, modelKey, len(vocab)+1, uris, vecs); err != nil {
		t.Fatal(err)
	}

	writeSnapshot(t, dir)

	cfg := Config{
		DB: db, AtlasDir: dir, Embedder: toyEmbedder{}, ModelKey: modelKey,
		RPS: 1000, Burst: 1000, SearchRPS: 1000, SearchBurst: 1000,
		Now: func() time.Time { return now },
		Activity: func(_ context.Context, days int) (any, error) {
			return map[string]any{"days": days, "totals": map[string]int{"posts": 7}}, nil
		},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	api := New(cfg)
	mux := http.NewServeMux()
	api.Register(mux)
	return &fixture{api: api, h: mux, db: db}
}

// writeSnapshot writes the map for posts p1..p6 (p7 is newer than the map).
func writeSnapshot(t *testing.T, dir string) {
	t.Helper()
	sec := func(h float64) int64 { return at(h) / 1000 }
	order := []string{p1, p2, p3, p4, p5, p6}
	a := atlasFile{
		ID: snapID, BuiltAt: now.Add(-30 * time.Minute).Unix(), WindowDays: 7, NPosts: 6, NAuthors: 3, NFollows: 3, NTopics: 3,
		TsRange: [2]int64{sec(30), sec(10)},
		Regions: []regionRec{
			{ID: 1, N: 4, Topics: []int{10}, Title: "Records and Receipts", Summary: "Agents on provenance."},
			{ID: 2, N: 2, Topics: []int{11, 12}, Title: "Odds and Ends", Summary: "Ducks and numbers."},
		},
		Topics: []topicRec{
			{ID: 10, Region: 1, N: 4, X: 0.1, Y: 0.1, Keywords: []string{"provenance", "receipts"}, Authors: 3, TopAuthors: []int{0, 1, 2}, First: sec(30), Last: sec(27), Rep: []int{0, 1}, Title: "Provenance and Receipts", Summary: "Agents argue that records need receipts."},
			{ID: 11, Region: 2, N: 1, X: -0.5, Y: 0.4, Keywords: []string{"ducks", "pond"}, Authors: 1, TopAuthors: []int{1}, First: sec(20), Last: sec(20), Rep: []int{4}, Title: "Ducks on the Pond", Summary: "A duck post."},
			{ID: 12, Region: 2, N: 1, X: 0.6, Y: -0.4, Keywords: []string{"random", "numbers"}, Authors: 1, TopAuthors: []int{2}, First: sec(10), Last: sec(10), Rep: []int{5}, Title: "Random Number Samples", Summary: "Generated numbers."},
		},
		Threads: []threadRec{{Root: p1, N: 4, Topic: 10, Authors: 3, First: sec(30), Last: sec(27), Title: "Agents debating append-only provenance records"}},
		Authors: []authorRec{
			{DID: didAlice, Handle: "alice.example", Name: "Alice", N: 2, Topics: []int{10}},
			{DID: didBob, Handle: "bob.example", Name: "Bob", N: 2, Topics: []int{10, 11}},
			{DID: didCarol, Name: "Carol", N: 2, Topics: []int{10, 12}},
		},
		Model: modelKey,
	}
	author := map[string]int{didAlice: 0, didBob: 1, didCarol: 2}
	text := map[string]string{p1: "Agents debating append-only provenance records", p2: "Provenance needs receipts", p3: "I agree, receipts all the way down", p4: "o7", p5: "Ducks are swimming in the pond", p6: "Random numbers from the CSPRNG: 0.5 0.3"}
	hours := map[string]float64{p1: 30, p2: 29, p3: 28, p4: 27, p5: 20, p6: 10}
	topic := map[string]int{p1: 10, p2: 10, p3: 10, p4: 10, p5: 11, p6: 12}
	region := map[string]int{p1: 1, p2: 1, p3: 1, p4: 1, p5: 2, p6: 2}
	parent := map[string]int{p2: 0, p3: 1, p4: 2}
	likes := map[string]int{p1: 2, p3: 1, p5: 1}
	var c colsFile
	for _, u := range order {
		did := "did:plc:" + strings.Split(strings.TrimPrefix(u, "at://did:plc:"), "/")[0]
		c.URI = append(c.URI, u)
		c.Text = append(c.Text, text[u])
		c.Author = append(c.Author, author[did])
		c.Ts = append(c.Ts, sec(hours[u]))
		c.Topic = append(c.Topic, topic[u])
		c.Region = append(c.Region, region[u])
		th := -1
		if topic[u] == 10 {
			th = 0
		}
		c.Thread = append(c.Thread, th)
		pi, ok := parent[u]
		if !ok {
			pi = -1
		}
		c.Parent = append(c.Parent, pi)
		r := 0
		if ok {
			r = 1
		}
		c.Reply = append(c.Reply, r)
		c.Likes = append(c.Likes, likes[u])
		c.Replies = append(c.Replies, 0)
		c.Reposts = append(c.Reposts, 0)
		c.Kind = append(c.Kind, "")
	}
	snap := filepath.Join(dir, snapID)
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"atlas.json": a, "cols.json": c} {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(snap, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(snapID, filepath.Join(dir, "latest")); err != nil {
		t.Fatal(err)
	}
}

// ---- request helpers

func (f *fixture) do(path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "atlas.test"
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// get requests path, requires the status, and decodes the JSON body into v (if non-nil).
func (f *fixture) get(t *testing.T, path string, wantStatus int, v any) *httptest.ResponseRecorder {
	t.Helper()
	w := f.do(path)
	if w.Code != wantStatus {
		t.Fatalf("GET %s: status %d, want %d; body: %s", path, w.Code, wantStatus, w.Body.String())
	}
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("GET %s: bad JSON: %v\n%s", path, err, w.Body.String())
		}
	}
	return w
}

// wantError requests path and checks the error envelope.
func (f *fixture) wantError(t *testing.T, path string, status int, code, param string) {
	t.Helper()
	var e struct {
		Error apiError `json:"error"`
	}
	f.get(t, path, status, &e)
	if e.Error.Code != code || e.Error.Param != param || e.Error.Message == "" {
		t.Errorf("GET %s: error %+v, want code=%q param=%q with a message", path, e.Error, code, param)
	}
}

func uris(vs []PostView) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.URI
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- small file helpers for tests

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRelink(t *testing.T, link, target string) {
	t.Helper()
	os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
