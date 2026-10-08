// Package server is the HTTP API and static site for the atlas.
package server

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/haileyok/delve-atlas/internal/agentapi"
	"github.com/haileyok/delve-atlas/internal/store"
)

// Server serves the atlas snapshots in AtlasDir and live data from DB.
type Server struct {
	DB       *store.DB
	AtlasDir string
	Web      fs.FS
	Log      *slog.Logger
	// Agent, when set, serves the documented JSON API for agents (/api/v1/*), /AGENTS.md and
	// /llms.txt.
	Agent *agentapi.API

	mu       sync.Mutex
	activity cached
}

type cached struct {
	at   time.Time
	body []byte
}

var (
	snapID   = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
	snapFile = map[string]string{
		"atlas.json": "application/json",
		"cols.json":  "application/json",
		"xy.f32":     "application/octet-stream",
	}
)

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/atlas", s.latest)
	mux.HandleFunc("GET /api/snap/{id}/{file}", s.snapshotFile)
	mux.HandleFunc("GET /api/post", s.post)
	mux.HandleFunc("GET /api/thread", s.thread)
	mux.HandleFunc("GET /api/activity", s.activityHandler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	if s.Agent != nil {
		s.Agent.Register(mux)
	}
	mux.Handle("/", s.static())
	return gzipMW(mux)
}

// static serves the site's own files. They are revalidated on every load with a content-hash
// ETag, so a deploy reaches browsers (and any CDN in front) on the next reload: the page's
// modules (main.js, gl.js, ...) must never be a mix of old and new. Unchanged files answer 304.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if b, err := fs.ReadFile(s.Web, name); err == nil {
			sum := sha256.Sum256(b)
			// weak, because the body may be sent gzip-compressed
			w.Header().Set("ETag", `W/"`+hex.EncodeToString(sum[:8])+`"`)
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func (s *Server) latestID() (string, error) {
	t, err := os.Readlink(filepath.Join(s.AtlasDir, "latest"))
	if err != nil {
		return "", err
	}
	return filepath.Base(t), nil
}

// latest redirects to the newest snapshot's atlas.json so the client can learn its id and
// then fetch the heavy files by immutable URL.
func (s *Server) latest(w http.ResponseWriter, r *http.Request) {
	id, err := s.latestID()
	if err != nil {
		http.Error(w, "no atlas has been built yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.Redirect(w, r, "/api/snap/"+id+"/atlas.json", http.StatusFound)
}

func (s *Server) snapshotFile(w http.ResponseWriter, r *http.Request) {
	id, file := r.PathValue("id"), r.PathValue("file")
	ct, ok := snapFile[file]
	if !ok || !snapID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.AtlasDir, id, file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, file, st.ModTime(), f)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(v)
}

// PostView is a post with its author and counts, as the detail panel shows it.
type PostView struct {
	URI         string `json:"uri"`
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Text        string `json:"text"`
	CreatedAt   int64  `json:"created_at"`
	ReplyParent string `json:"reply_parent"`
	ReplyRoot   string `json:"reply_root"`
	QuoteURI    string `json:"quote_uri"`
	EmbedKind   string `json:"embed_kind"`
	EmbedText   string `json:"embed_text"`
	LinkURL     string `json:"link_url"`
	Tags        string `json:"tags"`
	Langs       string `json:"langs"`
	NImages     int    `json:"n_images"`
	Likes       int    `json:"likes"`
	Reposts     int    `json:"reposts"`
	Replies     int    `json:"replies"`
}

const postSelect = `
	SELECT p.uri, p.did, COALESCE(a.handle,''), COALESCE(a.display_name,''), p.text, p.created_at,
	       p.reply_parent, p.reply_root, p.quote_uri, p.embed_kind, p.embed_text, p.link_url, p.tags, p.langs, p.n_images,
	       (SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind='like'),
	       (SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind='repost'),
	       (SELECT count(*) FROM posts c WHERE c.reply_parent = p.uri)
	FROM posts p LEFT JOIN actors a ON a.did = p.did `

type scanner interface{ Scan(...any) error }

func scanPost(r scanner) (PostView, error) {
	var p PostView
	err := r.Scan(&p.URI, &p.DID, &p.Handle, &p.Name, &p.Text, &p.CreatedAt, &p.ReplyParent, &p.ReplyRoot, &p.QuoteURI,
		&p.EmbedKind, &p.EmbedText, &p.LinkURL, &p.Tags, &p.Langs, &p.NImages, &p.Likes, &p.Reposts, &p.Replies)
	return p, err
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.Query().Get("uri")
	if !strings.HasPrefix(uri, "at://") {
		http.Error(w, "uri required", http.StatusBadRequest)
		return
	}
	p, err := scanPost(s.DB.QueryRowContext(r.Context(), postSelect+`WHERE p.uri = ?`, uri))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, p)
}

// thread returns every stored post whose thread root is the given URI (the root included),
// oldest first.
func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if !strings.HasPrefix(root, "at://") {
		http.Error(w, "root required", http.StatusBadRequest)
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), postSelect+`WHERE p.uri = ? OR p.reply_root = ? ORDER BY p.created_at LIMIT 800`, root, root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []PostView{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, p)
	}
	writeJSON(w, out)
}

// ---- gzip

// gzipWriter compresses a response once its status is known. Responses that must not have a
// body (304, 204, 1xx) and HEAD requests are passed through untouched.
type gzipWriter struct {
	http.ResponseWriter
	gz    *gzip.Writer
	wrote bool
	enc   bool
	head  bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.wrote {
		return
	}
	g.wrote = true
	bodyless := code < 200 || code == http.StatusNoContent || code == http.StatusNotModified || g.head
	if !bodyless && g.Header().Get("Content-Encoding") == "" {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length") // ServeContent sets the uncompressed size
		g.enc = true
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	if !g.enc {
		return g.ResponseWriter.Write(b)
	}
	if g.gz == nil {
		g.gz = gzip.NewWriter(g.ResponseWriter)
	}
	return g.gz.Write(b)
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		g.gz.Close()
	}
}

func gzipMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		// Range requests and clients without gzip pass through; so do formats that don't shrink.
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Header.Get("Range") != "" ||
			strings.HasSuffix(r.URL.Path, ".f32") || strings.HasSuffix(r.URL.Path, ".png") {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w, head: r.Method == http.MethodHead}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}
