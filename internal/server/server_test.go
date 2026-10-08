package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const snapshot = "20261008T120000Z"

func newServer(t *testing.T) (*Server, fstest.MapFS) {
	t.Helper()
	web := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>atlas</title>" + strings.Repeat("a", 2000))},
		"js/app.js":  {Data: []byte("export const v = 1; " + strings.Repeat("// padding\n", 200))},
	}
	dir := t.TempDir()
	snap := filepath.Join(dir, snapshot)
	if err := os.Mkdir(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"atlas.json": `{"id":"` + snapshot + `"}`, "cols.json": `{}`, "xy.f32": "0000"} {
		if err := os.WriteFile(filepath.Join(snap, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(snapshot, filepath.Join(dir, "latest")); err != nil {
		t.Fatal(err)
	}
	return &Server{Web: web, AtlasDir: dir}, web
}

func get(h http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStaticFilesRevalidateByContentHash(t *testing.T) {
	t.Parallel()
	s, web := newServer(t)
	h := s.Handler()

	first := get(h, "/js/app.js")
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" {
		t.Fatalf("first load: %d etag=%q", first.Code, etag)
	}
	if cc := first.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("the site's own files must revalidate on every load, got Cache-Control %q", cc)
	}

	// Unchanged: a conditional request is answered 304 with no body, even if the client takes gzip.
	again := get(h, "/js/app.js", "If-None-Match", etag, "Accept-Encoding", "gzip")
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("want an empty 304, got %d with %d body bytes", again.Code, again.Body.Len())
	}
	if again.Header().Get("Content-Encoding") != "" {
		t.Errorf("a 304 must not claim a gzip body, got Content-Encoding %q", again.Header().Get("Content-Encoding"))
	}

	// Deploying new code changes the ETag, so the old copy is not reused.
	web["js/app.js"] = &fstest.MapFile{Data: []byte("export const v = 2;")}
	changed := get(h, "/js/app.js", "If-None-Match", etag)
	if changed.Code != 200 || changed.Header().Get("ETag") == etag || !strings.Contains(changed.Body.String(), "v = 2") {
		t.Errorf("changed file must be served fresh: code=%d etag=%q", changed.Code, changed.Header().Get("ETag"))
	}
}

func TestIndexIsServedAtTheRoot(t *testing.T) {
	t.Parallel()
	s, _ := newServer(t)
	w := get(s.Handler(), "/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>atlas</title>") || w.Header().Get("ETag") == "" {
		t.Errorf("root: %d etag=%q", w.Code, w.Header().Get("ETag"))
	}
}

func TestGzipOnlyWhenAskedFor(t *testing.T) {
	t.Parallel()
	s, web := newServer(t)
	h := s.Handler()
	want := string(web["js/app.js"].Data)

	plain := get(h, "/js/app.js")
	if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != want {
		t.Errorf("without Accept-Encoding the body is sent as is")
	}

	zipped := get(h, "/js/app.js", "Accept-Encoding", "gzip")
	if zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip, got %q", zipped.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != want {
		t.Errorf("gzip body does not decompress to the file")
	}
	if !strings.Contains(zipped.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("a response that varies by encoding must say so, Vary=%q", zipped.Header().Get("Vary"))
	}
}

func TestSnapshotFilesAreImmutableAndValidated(t *testing.T) {
	t.Parallel()
	s, _ := newServer(t)
	h := s.Handler()

	ok := get(h, "/api/snap/"+snapshot+"/atlas.json")
	if ok.Code != 200 || !strings.Contains(ok.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("snapshot file: %d cache=%q", ok.Code, ok.Header().Get("Cache-Control"))
	}
	for _, bad := range []string{
		"/api/snap/not-an-id/atlas.json",
		"/api/snap/" + snapshot + "/delve.db",
		"/api/snap/" + snapshot + "/../../etc/passwd",
		"/api/snap/20261008T120000Z/latest",
	} {
		w := get(h, bad)
		// the mux cleans ".." by redirecting; what matters is that the cleaned URL serves nothing
		if w.Code >= 300 && w.Code < 400 {
			w = get(h, w.Header().Get("Location"))
		}
		if w.Code != 404 && w.Code != 400 {
			t.Errorf("%s should be refused, got %d", bad, w.Code)
		}
	}
}

func TestLatestPointsAtTheNewestSnapshot(t *testing.T) {
	t.Parallel()
	s, _ := newServer(t)
	w := get(s.Handler(), "/api/atlas")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/api/snap/"+snapshot+"/atlas.json" {
		t.Errorf("got %d -> %q", w.Code, w.Header().Get("Location"))
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("the pointer to the latest snapshot must not be cached, got %q", w.Header().Get("Cache-Control"))
	}
}

func TestPostEndpointRejectsNonURIs(t *testing.T) {
	t.Parallel()
	s, _ := newServer(t)
	for _, path := range []string{"/api/post?uri=x", "/api/post", "/api/thread?root=nope"} {
		if w := get(s.Handler(), path); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", path, w.Code)
		}
	}
}
