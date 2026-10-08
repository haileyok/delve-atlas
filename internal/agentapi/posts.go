package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// ---- the shape of a post

// AuthorRef identifies an account.
type AuthorRef struct {
	DID    string `json:"did"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

// Counts are the reactions a post has received.
type Counts struct {
	Likes   int `json:"likes"`
	Reposts int `json:"reposts"`
	Replies int `json:"replies"`
}

// EmbedView is what a post attached: a link card, images, a quoted post.
type EmbedView struct {
	Kind    string `json:"kind"`
	LinkURL string `json:"link_url,omitempty"`
	Text    string `json:"text,omitempty"` // link title/description and image alt text
	Images  int    `json:"images,omitempty"`
}

// ThreadRef places a post in a conversation of three or more posts.
type ThreadRef struct {
	Root  string `json:"root"`
	Posts int    `json:"posts"`
}

// PostView is a post as every endpoint returns it. Topic and Region are null for posts newer
// than the latest map (they get one at the next rebuild).
type PostView struct {
	URI        string     `json:"uri"`
	URL        string     `json:"url"`
	Author     AuthorRef  `json:"author"`
	CreatedAt  string     `json:"created_at"`
	Text       string     `json:"text"`
	ReplyTo    *string    `json:"reply_to"`
	ThreadRoot *string    `json:"thread_root"`
	Quotes     *string    `json:"quotes"`
	Embed      *EmbedView `json:"embed"`
	Langs      []string   `json:"langs,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Counts     Counts     `json:"counts"`
	Topic      *Ref       `json:"topic"`
	Region     *Ref       `json:"region"`
	Thread     *ThreadRef `json:"thread,omitempty"`
	Score      *float64   `json:"score,omitempty"` // search relevance / similarity, when applicable
}

// postRow is one row of the shared post query.
type postRow struct {
	URI, DID, Handle, Name, Text                           string
	CreatedAt                                              int64
	ReplyParent, ReplyRoot, QuoteURI, EmbedKind, EmbedText string
	LinkURL, Tags, Langs                                   string
	NImages, Likes, Reposts, Replies                       int
	Rank                                                   float64
}

func postURL(uri string) string {
	rest := strings.TrimPrefix(uri, "at://")
	parts := strings.Split(rest, "/")
	if len(parts) == 3 {
		return "https://delve.town/profile/" + parts[0] + "/post/" + parts[2]
	}
	return ""
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func (s *Snapshot) view(r postRow) PostView {
	v := PostView{
		URI:       r.URI,
		URL:       postURL(r.URI),
		Author:    AuthorRef{DID: r.DID, Handle: r.Handle, Name: r.Name},
		CreatedAt: rfc3339(r.CreatedAt),
		Text:      r.Text,
		ReplyTo:   optStr(r.ReplyParent),
		Quotes:    optStr(r.QuoteURI),
		Langs:     splitCSV(r.Langs),
		Tags:      splitCSV(r.Tags),
		Counts:    Counts{Likes: r.Likes, Reposts: r.Reposts, Replies: r.Replies},
	}
	if r.ReplyRoot != "" {
		v.ThreadRoot = &r.ReplyRoot
	}
	if r.EmbedKind != "" || r.LinkURL != "" || r.EmbedText != "" {
		v.Embed = &EmbedView{Kind: r.EmbedKind, LinkURL: r.LinkURL, Text: r.EmbedText, Images: r.NImages}
	}
	if s != nil {
		if i, ok := s.Idx[r.URI]; ok {
			v.Topic = s.topicRef(s.C.Topic[i])
			v.Region = s.regionRef(s.C.Region[i])
			if k := s.C.Thread[i]; k >= 0 && k < len(s.A.Threads) {
				v.Thread = &ThreadRef{Root: s.A.Threads[k].Root, Posts: s.A.Threads[k].N}
			}
		}
	}
	return v
}

func (s *Snapshot) views(rows []postRow) []PostView {
	out := make([]PostView, len(rows))
	for i, r := range rows {
		out[i] = s.view(r)
	}
	return out
}

// ---- the shared post query

type postQuery struct {
	fts      string // sanitised FTS5 expression
	restrict bool   // limit to the URIs below (an empty list then matches nothing)
	uris     []string
	did      string
	sinceMS  int64
	untilMS  int64
	reply    *bool
	thread   string // a thread root: the root and everything replying within it
	parent   string // direct replies to this post
	sort     string // recent | oldest | engaged | relevance
	limit    int
	offset   int
}

const postColumns = `p.uri, p.did, COALESCE(a.handle,''), COALESCE(a.display_name,''), p.text, p.created_at,
	p.reply_parent, p.reply_root, p.quote_uri, p.embed_kind, p.embed_text, p.link_url, p.tags, p.langs, p.n_images,
	(SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind = 'like')   AS likes,
	(SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind = 'repost') AS reposts,
	(SELECT count(*) FROM posts c WHERE c.reply_parent = p.uri)                         AS replies`

// queryPosts runs the one post query behind every listing endpoint and also returns how many
// posts match in total (ignoring limit and offset).
func (a *API) queryPosts(ctx context.Context, pq postQuery) ([]postRow, int, error) {
	if pq.restrict && len(pq.uris) == 0 {
		return nil, 0, nil
	}
	from := "FROM posts p LEFT JOIN actors a ON a.did = p.did"
	rank := "0.0"
	var where []string
	var args []any
	if pq.fts != "" {
		from = "FROM posts p JOIN posts_fts ON posts_fts.rowid = p.rowid LEFT JOIN actors a ON a.did = p.did"
		rank = "bm25(posts_fts)"
		where = append(where, "posts_fts MATCH ?")
		args = append(args, pq.fts)
	}
	if pq.restrict {
		j, _ := json.Marshal(pq.uris)
		where = append(where, "p.uri IN (SELECT value FROM json_each(?))")
		args = append(args, string(j))
	}
	if pq.did != "" {
		where = append(where, "p.did = ?")
		args = append(args, pq.did)
	}
	if pq.sinceMS > 0 {
		where = append(where, "p.created_at >= ?")
		args = append(args, pq.sinceMS)
	}
	if pq.untilMS > 0 {
		where = append(where, "p.created_at <= ?")
		args = append(args, pq.untilMS)
	}
	if pq.reply != nil {
		if *pq.reply {
			where = append(where, "p.reply_parent <> ''")
		} else {
			where = append(where, "p.reply_parent = ''")
		}
	}
	if pq.thread != "" {
		where = append(where, "(p.uri = ? OR p.reply_root = ?)")
		args = append(args, pq.thread, pq.thread)
	}
	if pq.parent != "" {
		where = append(where, "p.reply_parent = ?")
		args = append(args, pq.parent)
	}
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := a.cfg.DB.QueryRowContext(ctx, "SELECT count(*) "+from+" "+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "p.created_at DESC, p.uri"
	switch pq.sort {
	case "oldest":
		order = "p.created_at ASC, p.uri"
	case "engaged":
		order = "(likes + 1.5 * replies + 2 * reposts) DESC, p.created_at DESC, p.uri"
	case "relevance":
		order = "rank ASC, p.created_at DESC, p.uri"
	}
	q := "SELECT " + postColumns + ", " + rank + " AS rank " + from + " " + w + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	rows, err := a.cfg.DB.QueryContext(ctx, q, append(args, pq.limit, pq.offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []postRow
	for rows.Next() {
		var r postRow
		if err := rows.Scan(&r.URI, &r.DID, &r.Handle, &r.Name, &r.Text, &r.CreatedAt, &r.ReplyParent, &r.ReplyRoot, &r.QuoteURI,
			&r.EmbedKind, &r.EmbedText, &r.LinkURL, &r.Tags, &r.Langs, &r.NImages, &r.Likes, &r.Reposts, &r.Replies, &r.Rank); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// postsByURI loads specific posts, in the order requested (missing ones are skipped).
func (a *API) postsByURI(ctx context.Context, uris []string) ([]postRow, error) {
	if len(uris) == 0 {
		return nil, nil
	}
	rows, _, err := a.queryPosts(ctx, postQuery{restrict: true, uris: uris, limit: len(uris), sort: "recent"})
	if err != nil {
		return nil, err
	}
	by := make(map[string]postRow, len(rows))
	for _, r := range rows {
		by[r.URI] = r
	}
	out := make([]postRow, 0, len(uris))
	for _, u := range uris {
		if r, ok := by[u]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---- keyword queries

var ftsToken = regexp.MustCompile(`"([^"]+)"|([\p{L}\p{N}_]+\*?)`)
var wordsOnly = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// ftsQuery turns what an agent typed into a safe FTS5 expression. Every term must match
// (any order), "double quotes" make an exact phrase, a trailing * makes a prefix. Anything else
// is dropped, so nothing typed can be read as FTS syntax.
func ftsQuery(q string) (string, bool) {
	var parts []string
	for _, m := range ftsToken.FindAllStringSubmatch(q, -1) {
		if m[1] != "" {
			words := wordsOnly.FindAllString(m[1], -1)
			if len(words) > 0 {
				parts = append(parts, `"`+strings.Join(words, " ")+`"`)
			}
			continue
		}
		tok := m[2]
		if strings.HasSuffix(tok, "*") {
			parts = append(parts, `"`+strings.TrimSuffix(tok, "*")+`"*`)
		} else {
			parts = append(parts, `"`+tok+`"`)
		}
	}
	return strings.Join(parts, " "), len(parts) > 0
}

// ---- author references

// resolveAuthor accepts a DID or a handle (with or without a leading @).
func (a *API) resolveAuthor(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "@")
	if strings.HasPrefix(ref, "did:") {
		return ref, nil
	}
	var did string
	err := a.cfg.DB.QueryRowContext(ctx, `SELECT did FROM actors WHERE handle = ? COLLATE NOCASE LIMIT 1`, ref).Scan(&did)
	if err != nil {
		return "", notFound(fmt.Sprintf("no account with handle %q", ref))
	}
	return did, nil
}

// ---- GET /api/v1/posts

// topicSet resolves topic/region filters to the URIs of the posts in them.
func (s *Snapshot) restrictTo(topic, region *int) (uris []string, restrict bool, err error) {
	var rows []int
	switch {
	case topic != nil:
		if _, ok := s.Topics[*topic]; !ok {
			return nil, false, notFound(fmt.Sprintf("no topic %d in map %s", *topic, s.ID))
		}
		rows = s.ByTopic[*topic]
		if region != nil && s.Topics[*topic].Region != *region {
			rows = nil
		}
	case region != nil:
		if _, ok := s.Regions[*region]; !ok {
			return nil, false, notFound(fmt.Sprintf("no region %d in map %s", *region, s.ID))
		}
		rows = s.ByRegion[*region]
	default:
		return nil, false, nil
	}
	uris = make([]string, len(rows))
	for i, r := range rows {
		uris[i] = s.C.URI[r]
	}
	return uris, true, nil
}

func intPtr(n int, ok bool) *int {
	if !ok {
		return nil
	}
	return &n
}

// postFilters are the parameters shared by /posts, /topics/{id}/posts and friends.
func (a *API) postFilters(ctx context.Context, p *params, snap *Snapshot, fixedTopic *int) (postQuery, page, error) {
	var pq postQuery
	q := p.str("q")
	t, hasT := p.optInt("topic")
	rg, hasR := p.optInt("region")
	topic, region := intPtr(t, hasT), intPtr(rg, hasR)
	if fixedTopic != nil {
		topic = fixedTopic
	}
	author := p.str("author")
	pq.sinceMS, pq.untilMS = p.timeMS("since"), p.timeMS("until")
	pq.reply = p.boolean("reply")
	defSort := "recent"
	if q != "" {
		defSort = "relevance"
	}
	pq.sort = p.enum("sort", defSort, "recent", "oldest", "engaged", "relevance")
	pg := p.page(25, 200)
	pq.limit, pq.offset = pg.limit, pg.offset
	if err := p.Err(); err != nil {
		return pq, pg, err
	}
	if q != "" {
		expr, ok := ftsQuery(q)
		if !ok {
			return pq, pg, badParam("q", "contains no searchable words")
		}
		pq.fts = expr
	} else if pq.sort == "relevance" {
		return pq, pg, badParam("sort", "relevance needs a q parameter")
	}
	var err error
	if pq.uris, pq.restrict, err = snap.restrictTo(topic, region); err != nil {
		return pq, pg, err
	}
	if author != "" {
		if pq.did, err = a.resolveAuthor(ctx, author); err != nil {
			return pq, pg, err
		}
	}
	return pq, pg, nil
}

func (a *API) listPosts(ctx context.Context, snap *Snapshot, pq postQuery, pg page) (any, error) {
	rows, total, err := a.queryPosts(ctx, pq)
	if err != nil {
		return nil, err
	}
	views := snap.views(rows)
	if pq.fts != "" {
		for i := range views {
			// bm25 is lower for better matches; report it as a positive score where bigger is better
			sc := -rows[i].Rank
			views[i].Score = &sc
		}
	}
	return newList(snap.ID, views, total, pg), nil
}

func (a *API) handlePosts(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	pq, pg, err := a.postFilters(r.Context(), a.params(r), snap, nil)
	if err != nil {
		return nil, err
	}
	return a.listPosts(r.Context(), snap, pq, pg)
}

func (a *API) handleTopicPosts(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	id, err := pathInt(r, "id")
	if err != nil {
		return nil, err
	}
	pq, pg, err := a.postFilters(r.Context(), a.params(r), snap, &id)
	if err != nil {
		return nil, err
	}
	return a.listPosts(r.Context(), snap, pq, pg)
}

func pathInt(r *http.Request, name string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(r.PathValue(name), "%d", &n); err != nil {
		return 0, badParam(name, "must be an integer")
	}
	return n, nil
}

// ---- GET /api/v1/post

// PostDetail is a post with its surroundings.
type PostDetail struct {
	Snapshot      string     `json:"snapshot"`
	Post          PostView   `json:"post"`
	Parent        *PostView  `json:"parent"`
	Children      []PostView `json:"children"`
	ChildrenTotal int        `json:"children_total"`
	Similar       []PostView `json:"similar,omitempty"`
}

func (a *API) handlePost(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	p := a.params(r)
	uri := p.required("uri")
	include := map[string]bool{"parent": true, "children": true}
	if s := p.str("include"); s != "" {
		include = map[string]bool{}
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part != "parent" && part != "children" && part != "similar" && part != "" {
				p.fail("include", "must be a comma-separated list of: parent, children, similar")
			}
			include[part] = true
		}
	}
	if err := p.Err(); err != nil {
		return nil, err
	}
	rows, err := a.postsByURI(r.Context(), []string{uri})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, notFound("no stored post with that uri")
	}
	out := PostDetail{Snapshot: snap.ID, Post: snap.view(rows[0]), Children: []PostView{}}
	if include["parent"] && rows[0].ReplyParent != "" {
		if pr, err := a.postsByURI(r.Context(), []string{rows[0].ReplyParent}); err == nil && len(pr) > 0 {
			v := snap.view(pr[0])
			out.Parent = &v
		}
	}
	if include["children"] {
		kids, total, err := a.queryPosts(r.Context(), postQuery{parent: uri, sort: "oldest", limit: 50})
		if err != nil {
			return nil, err
		}
		out.Children, out.ChildrenTotal = snap.views(kids), total
		if out.Children == nil {
			out.Children = []PostView{}
		}
	}
	if include["similar"] {
		sim, err := a.similarTo(r.Context(), snap, uri, 8)
		if err != nil {
			return nil, err
		}
		out.Similar = sim
	}
	return out, nil
}

// ---- GET /api/v1/thread

// ThreadPost is a post within a conversation, with how deep in the reply tree it sits.
type ThreadPost struct {
	PostView
	Depth int `json:"depth"`
}

// ThreadResponse is a whole conversation, oldest first.
type ThreadResponse struct {
	Snapshot     string        `json:"snapshot"`
	Root         string        `json:"root"`
	RootStored   bool          `json:"root_stored"`
	Topic        *Ref          `json:"topic"`
	Posts        int           `json:"posts"`
	Truncated    bool          `json:"truncated"`
	Participants []Participant `json:"participants"`
	Results      []ThreadPost  `json:"results"`
	NextCursor   *string       `json:"next_cursor"`
}

// Participant is an account in a conversation and how many posts it made there.
type Participant struct {
	AuthorRef
	Posts int `json:"posts"`
}

const threadReadCap = 3000

func (a *API) handleThread(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	p := a.params(r)
	root, uri := p.str("root"), p.str("uri")
	pg := p.page(200, 1000)
	if root == "" && uri == "" {
		p.fail("root", "give root (the conversation's first post) or uri (any post in it)")
	}
	if err := p.Err(); err != nil {
		return nil, err
	}
	if root == "" {
		rows, err := a.postsByURI(r.Context(), []string{uri})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, notFound("no stored post with that uri")
		}
		root = rows[0].URI
		if rows[0].ReplyRoot != "" {
			root = rows[0].ReplyRoot
		}
	}
	rows, total, err := a.queryPosts(r.Context(), postQuery{thread: root, sort: "oldest", limit: threadReadCap})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, notFound("no stored posts in that conversation")
	}

	// depth in the reply tree, over everything we read (pages are slices of this list)
	depth := make(map[string]int, len(rows))
	for _, r := range rows {
		d := 0
		if pd, ok := depth[r.ReplyParent]; ok {
			d = pd + 1
		}
		depth[r.URI] = d
	}
	resp := ThreadResponse{Snapshot: snap.ID, Root: root, Posts: total, Truncated: total > len(rows)}
	for _, r := range rows {
		if r.URI == root {
			resp.RootStored = true
		}
	}
	counts := map[string]*Participant{}
	for _, r := range rows {
		pt, ok := counts[r.DID]
		if !ok {
			pt = &Participant{AuthorRef: AuthorRef{DID: r.DID, Handle: r.Handle, Name: r.Name}}
			counts[r.DID] = pt
		}
		pt.Posts++
	}
	for _, pt := range counts {
		resp.Participants = append(resp.Participants, *pt)
	}
	sort.Slice(resp.Participants, func(i, j int) bool {
		if resp.Participants[i].Posts != resp.Participants[j].Posts {
			return resp.Participants[i].Posts > resp.Participants[j].Posts
		}
		return resp.Participants[i].DID < resp.Participants[j].DID
	})
	if len(resp.Participants) > 25 {
		resp.Participants = resp.Participants[:25]
	}
	if k, ok := snap.ThreadByRoot[root]; ok {
		resp.Topic = snap.topicRef(snap.A.Threads[k].Topic)
	}
	lo := min(pg.offset, len(rows))
	hi := min(lo+pg.limit, len(rows))
	for _, r := range rows[lo:hi] {
		resp.Results = append(resp.Results, ThreadPost{PostView: snap.view(r), Depth: depth[r.URI]})
	}
	if resp.Results == nil {
		resp.Results = []ThreadPost{}
	}
	resp.NextCursor = pg.next(len(resp.Results), len(rows))
	return resp, nil
}

// ---- GET /api/v1/threads

// ThreadSummary is a conversation in a list.
type ThreadSummary struct {
	Root         string `json:"root"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Posts        int    `json:"posts"`
	Participants int    `json:"participants"`
	Topic        *Ref   `json:"topic"`
	First        string `json:"first_post_at"`
	Last         string `json:"last_post_at"`
}

func (a *API) handleThreads(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	p := a.params(r)
	topic, hasTopic := p.optInt("topic")
	minPosts := p.integer("min_posts", 3, 1, 100000)
	sortBy := p.enum("sort", "size", "size", "recent")
	pg := p.page(25, 200)
	if err := p.Err(); err != nil {
		return nil, err
	}
	if hasTopic {
		if _, ok := snap.Topics[topic]; !ok {
			return nil, notFound(fmt.Sprintf("no topic %d in map %s", topic, snap.ID))
		}
	}
	var picked []threadRec
	for _, t := range snap.A.Threads {
		if t.N < minPosts || (hasTopic && t.Topic != topic) {
			continue
		}
		picked = append(picked, t)
	}
	sort.SliceStable(picked, func(i, j int) bool {
		if sortBy == "recent" {
			return picked[i].Last > picked[j].Last
		}
		return picked[i].N > picked[j].N
	})
	total := len(picked)
	lo := min(pg.offset, total)
	hi := min(lo+pg.limit, total)
	out := make([]ThreadSummary, 0, hi-lo)
	for _, t := range picked[lo:hi] {
		out = append(out, ThreadSummary{
			Root: t.Root, URL: postURL(t.Root), Title: t.Title, Posts: t.N, Participants: t.Authors,
			Topic: snap.topicRef(t.Topic), First: rfc3339s(t.First), Last: rfc3339s(t.Last),
		})
	}
	return newList(snap.ID, out, total, pg), nil
}
