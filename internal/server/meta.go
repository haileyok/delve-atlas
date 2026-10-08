package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The page's social tags (og:*, twitter:*, canonical) need absolute URLs and should describe the
// map as it is now, so index.html carries three placeholders that are filled in per request:
//
//	{{origin}}       https://host the page was reached on (or Server.PublicURL)
//	{{description}}  one sentence about the newest map
//	{{ogv}}          the newest map's id, so link-preview caches fetch a fresh card after a rebuild

const defaultDescription = "A living map of what Delve is talking about: the last week of posts, grouped into regions and topics. Zoom in and dig into the conversations."

// metaCache remembers what was read from the newest snapshot, which only changes on a rebuild.
type metaCache struct {
	id          string
	description string
}

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)

func firstValue(v string) string {
	v, _, _ = strings.Cut(v, ",")
	return strings.TrimSpace(v)
}

// origin is scheme://host for absolute URLs. Behind a proxy or tunnel it is the host the visitor
// used (X-Forwarded-Host, else Host) and the protocol the proxy saw. A value that is not a plain
// host name is dropped, which leaves the tags with relative URLs instead of attacker text.
func (s *Server) origin(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	host := firstValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if !hostRE.MatchString(host) {
		return ""
	}
	proto := strings.ToLower(firstValue(r.Header.Get("X-Forwarded-Proto")))
	if proto != "http" && proto != "https" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	return proto + "://" + host
}

func groupThousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// mapMeta returns the newest snapshot's id ("0" when there is none) and a one-sentence
// description of it.
func (s *Server) mapMeta() (id, description string) {
	id, err := s.latestID()
	if err != nil || !snapID.MatchString(id) {
		return "0", defaultDescription
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if s.metaCache.id == id {
		return id, s.metaCache.description
	}
	var a struct {
		NPosts     int     `json:"n_posts"`
		NAuthors   int     `json:"n_authors"`
		NTopics    int     `json:"n_topics"`
		WindowDays float64 `json:"window_days"`
		Regions    []struct {
			Title string `json:"title"`
			N     int    `json:"n"`
		} `json:"regions"`
	}
	b, err := os.ReadFile(filepath.Join(s.AtlasDir, id, "atlas.json"))
	if err != nil || json.Unmarshal(b, &a) != nil || a.NPosts == 0 {
		return id, defaultDescription
	}
	sort.SliceStable(a.Regions, func(i, j int) bool { return a.Regions[i].N > a.Regions[j].N })
	var names []string
	for _, r := range a.Regions {
		if t := strings.TrimSpace(r.Title); t != "" && len(names) < 3 {
			names = append(names, t)
		}
	}
	days := int(a.WindowDays + 0.5)
	if days < 1 {
		days = 7
	}
	d := fmt.Sprintf("%s posts from %s accounts in the last %d days, mapped into %s topics", groupThousands(a.NPosts),
		groupThousands(a.NAuthors), days, groupThousands(a.NTopics))
	if len(names) > 0 {
		d += " across areas like " + joinNames(names)
	}
	d += ". Zoom in and dig into the conversations."
	s.metaCache = metaCache{id: id, description: d}
	return id, d
}

// serveIndex answers / with index.html, its placeholders filled in. The ETag covers the filled-in
// page, so a new map (new description and image version) reaches browsers on the next load.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	tpl, err := fs.ReadFile(s.Web, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id, desc := s.mapMeta()
	page := strings.NewReplacer(
		"{{origin}}", html.EscapeString(s.origin(r)),
		"{{description}}", html.EscapeString(desc),
		"{{ogv}}", id,
	).Replace(string(tpl))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", `W/"`+hashOf([]byte(page))+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader([]byte(page)))
}

// snapshotsNewestFirst lists up to n snapshot ids, newest first, starting with the one `latest`
// points at (which is what the site is showing even if a newer directory is half-written).
func (s *Server) snapshotsNewestFirst(n int) []string {
	var ids []string
	if id, err := s.latestID(); err == nil && snapID.MatchString(id) {
		ids = append(ids, id)
	}
	entries, _ := os.ReadDir(s.AtlasDir)
	var rest []string
	for _, e := range entries {
		if e.IsDir() && snapID.MatchString(e.Name()) && (len(ids) == 0 || e.Name() != ids[0]) {
			rest = append(rest, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rest)))
	ids = append(ids, rest...)
	if len(ids) > n {
		ids = ids[:n]
	}
	return ids
}

// ogImage serves the link-preview card: the one rendered for the newest map (tools/og.mjs writes
// og.jpg into the snapshot), else the card bundled with the site so a preview always has an image.
func (s *Server) ogImage(w http.ResponseWriter, r *http.Request) {
	serve := func(name string, mod time.Time, body interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	}) {
		w.Header().Set("Content-Type", "image/jpeg")
		// The URL stays the same across maps (the page adds ?v=<map id> to refresh previews), so it
		// may be cached for a while but never forever.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeContent(w, r, name, mod, body)
	}
	for _, id := range s.snapshotsNewestFirst(16) {
		if f, err := os.Open(filepath.Join(s.AtlasDir, id, "og.jpg")); err == nil {
			defer f.Close()
			if st, err := f.Stat(); err == nil {
				serve("og.jpg", st.ModTime(), f)
				return
			}
		}
	}
	if b, err := fs.ReadFile(s.Web, "og/default.jpg"); err == nil {
		serve("og.jpg", time.Time{}, bytes.NewReader(b))
		return
	}
	http.NotFound(w, r)
}
