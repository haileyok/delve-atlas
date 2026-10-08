package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const metaIndex = `<!doctype html><head>
<meta name="description" content="{{description}}">
<link rel="canonical" href="{{origin}}/">
<meta property="og:image" content="{{origin}}/og.jpg?v={{ogv}}">
</head>`

// newMetaServer is a server whose index page has the placeholders and whose newest snapshot
// describes itself, as a real one does.
func newMetaServer(t *testing.T, atlasJSON string) *Server {
	t.Helper()
	s, web := newServer(t)
	web["index.html"] = &fstest.MapFile{Data: []byte(metaIndex)}
	if atlasJSON != "" {
		if err := os.WriteFile(filepath.Join(s.AtlasDir, snapshot, "atlas.json"), []byte(atlasJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

const atlasWithNumbers = `{"id":"` + snapshot + `","n_posts":13241,"n_authors":218,"n_topics":84,"window_days":7,
"regions":[{"title":"Agent Social Life","n":3152},{"title":"Delvetown Agent Commons","n":2975},{"title":"Playful Provenance","n":1547},{"title":"Small One","n":3}]}`

func TestPageMetaTakesItsOriginFromTheRequest(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	w := get(s.Handler(), "/", "X-Forwarded-Host", "atlas.example.com", "X-Forwarded-Proto", "https")
	body := w.Body.String()
	for _, want := range []string{
		`href="https://atlas.example.com/"`,
		`content="https://atlas.example.com/og.jpg?v=` + snapshot + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page should contain %s, got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "{{") {
		t.Errorf("no placeholder may be left behind:\n%s", body)
	}
}

func TestPageMetaDescribesTheCurrentMap(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	body := get(s.Handler(), "/").Body.String()
	for _, want := range []string{"13,241 posts", "218 accounts", "84 topics", "Agent Social Life", "Delvetown Agent Commons", "Playful Provenance"} {
		if !strings.Contains(body, want) {
			t.Errorf("description should mention %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Small One") {
		t.Errorf("only the biggest regions are named:\n%s", body)
	}
}

func TestPageMetaEscapesWhatItInserts(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, `{"id":"`+snapshot+`","n_posts":10,"n_authors":2,"n_topics":3,"window_days":7,
"regions":[{"title":"Quotes \" and <b>tags</b>","n":9}]}`)
	body := get(s.Handler(), "/").Body.String()
	if strings.Contains(body, `<b>`) || strings.Contains(body, `" and`) {
		t.Errorf("a region title must not break out of the attribute:\n%s", body)
	}
	if !strings.Contains(body, "&lt;b&gt;") {
		t.Errorf("the title should still be there, escaped:\n%s", body)
	}
}

func TestPageMetaIgnoresAHostileHost(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	for _, host := range []string{`evil.com"><script>`, "a b", "x/y", ""} {
		body := get(s.Handler(), "/", "X-Forwarded-Host", host).Body.String()
		if strings.Contains(body, "<script>") || strings.Contains(body, "evil") {
			t.Errorf("host %q leaked into the page:\n%s", host, body)
		}
	}
	// A bad forwarded protocol is ignored too.
	body := get(s.Handler(), "/", "X-Forwarded-Host", "ok.example", "X-Forwarded-Proto", `javascript:"`).Body.String()
	if !strings.Contains(body, `href="http://ok.example/"`) {
		t.Errorf("an unknown protocol should fall back to http:\n%s", body)
	}
}

func TestPublicURLWinsOverTheRequest(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	s.PublicURL = "https://atlas.delve.town/"
	body := get(s.Handler(), "/", "X-Forwarded-Host", "other.example").Body.String()
	if !strings.Contains(body, `href="https://atlas.delve.town/"`) || strings.Contains(body, "other.example") {
		t.Errorf("configured public URL should be used:\n%s", body)
	}
}

func TestPageMetaStillWorksWithoutASnapshotOrNumbers(t *testing.T) {
	t.Parallel()
	s, web := newServer(t)
	web["index.html"] = &fstest.MapFile{Data: []byte(metaIndex)}
	s.AtlasDir = t.TempDir() // nothing built yet
	w := get(s.Handler(), "/")
	if w.Code != 200 || strings.Contains(w.Body.String(), "{{") {
		t.Fatalf("code %d:\n%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "og.jpg?v=0") {
		t.Errorf("without a snapshot the image version is a constant:\n%s", w.Body.String())
	}
}

func TestRenderedIndexKeepsRevalidating(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	h := s.Handler()
	first := get(h, "/", "Host", "x")
	etag := first.Header().Get("ETag")
	if etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("etag=%q cache-control=%q", etag, first.Header().Get("Cache-Control"))
	}
	if again := get(h, "/", "If-None-Match", etag); again.Code != 304 {
		t.Errorf("unchanged page should answer 304, got %d", again.Code)
	}
	// A new map changes the description and the image version, so the ETag must change.
	if err := os.WriteFile(filepath.Join(s.AtlasDir, snapshot, "atlas.json"),
		[]byte(strings.Replace(atlasWithNumbers, "13241", "14000", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	s.metaCache = metaCache{} // as if a new snapshot had appeared
	if changed := get(h, "/", "If-None-Match", etag); changed.Code != 200 {
		t.Errorf("a changed page must not be answered 304, got %d", changed.Code)
	}
}

func TestOGImageComesFromTheNewestSnapshotElseTheBundledOne(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	h := s.Handler()

	// Nothing rendered yet and nothing bundled.
	if w := get(h, "/og.jpg"); w.Code != 404 {
		t.Errorf("no image anywhere should be a 404, got %d", w.Code)
	}

	web := s.Web.(fstest.MapFS)
	web["og/default.jpg"] = &fstest.MapFile{Data: []byte("bundled")}
	if w := get(h, "/og.jpg"); w.Code != 200 || w.Body.String() != "bundled" {
		t.Errorf("fallback: %d %q", w.Code, w.Body.String())
	}

	if err := os.WriteFile(filepath.Join(s.AtlasDir, snapshot, "og.jpg"), []byte("rendered"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := get(h, "/og.jpg")
	if w.Code != 200 || w.Body.String() != "rendered" {
		t.Errorf("the snapshot's card should win: %d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "public") || strings.Contains(cc, "immutable") {
		t.Errorf("the image URL is reused across maps, so it may be cached but not forever: %q", cc)
	}
}

func TestOGImageFallsBackToAnOlderMapsCard(t *testing.T) {
	t.Parallel()
	s := newMetaServer(t, atlasWithNumbers)
	// An older snapshot has a card; the newest one's render failed.
	older := filepath.Join(s.AtlasDir, "20261007T120000Z")
	if err := os.Mkdir(older, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(older, "og.jpg"), []byte("older card"), 0o644); err != nil {
		t.Fatal(err)
	}
	web := s.Web.(fstest.MapFS)
	web["og/default.jpg"] = &fstest.MapFile{Data: []byte("bundled")}
	if w := get(s.Handler(), "/og.jpg"); w.Body.String() != "older card" {
		t.Errorf("the last rendered card should beat the bundled one, got %q", w.Body.String())
	}
}
