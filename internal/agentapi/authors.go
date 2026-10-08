package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// AuthorSummary is an account that posted, with what it did in the window asked about.
type AuthorSummary struct {
	AuthorRef
	Bio           string `json:"bio"`
	Posts         int    `json:"posts"`
	Replies       int    `json:"replies"`
	LikesReceived int    `json:"likes_received"`
	FirstPostAt   string `json:"first_post_at"`
	LastPostAt    string `json:"last_post_at"`
}

// authorsQuery is the grouped query behind the account list and one account's totals.
const authorsSelect = `
	WITH lr AS (
		SELECT q.did AS did, count(*) AS n FROM interactions i JOIN posts q ON q.uri = i.subject
		WHERE i.kind = 'like' GROUP BY q.did
	)
	SELECT p.did, COALESCE(a.handle,''), COALESCE(a.display_name,''), COALESCE(a.description,''),
	       count(*), COALESCE(sum(p.reply_parent <> ''),0), min(p.created_at), max(p.created_at), COALESCE(lr.n,0)
	FROM posts p LEFT JOIN actors a ON a.did = p.did LEFT JOIN lr ON lr.did = p.did`

func scanAuthor(sc interface{ Scan(...any) error }) (AuthorSummary, error) {
	var s AuthorSummary
	var first, last int64
	err := sc.Scan(&s.DID, &s.Handle, &s.Name, &s.Bio, &s.Posts, &s.Replies, &first, &last, &s.LikesReceived)
	s.FirstPostAt, s.LastPostAt = rfc3339(first), rfc3339(last)
	return s, err
}

func (a *API) handleAuthors(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	p := a.params(r)
	q := p.str("q")
	since := p.timeMS("since")
	sortBy := p.enum("sort", "posts", "posts", "likes_received", "replies", "recent")
	pg := p.page(25, 200)
	if err := p.Err(); err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if since > 0 {
		where = append(where, "p.created_at >= ?")
		args = append(args, since)
	}
	if q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		where = append(where, `(a.handle LIKE ? ESCAPE '\' OR a.display_name LIKE ? ESCAPE '\' OR a.description LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	w := ""
	if len(where) > 0 {
		w = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := a.cfg.DB.QueryRowContext(r.Context(),
		`SELECT count(*) FROM (SELECT p.did FROM posts p LEFT JOIN actors a ON a.did = p.did`+w+` GROUP BY p.did)`, args...).Scan(&total); err != nil {
		return nil, err
	}
	order := map[string]string{
		"posts": "count(*) DESC", "replies": "sum(p.reply_parent <> '') DESC",
		"likes_received": "COALESCE(lr.n,0) DESC", "recent": "max(p.created_at) DESC",
	}[sortBy]
	rows, err := a.cfg.DB.QueryContext(r.Context(),
		authorsSelect+w+" GROUP BY p.did ORDER BY "+order+", p.did LIMIT ? OFFSET ?", append(args, pg.limit, pg.offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthorSummary{}
	for rows.Next() {
		s, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return newList(snap.ID, out, total, pg), rows.Err()
}

// TopicShare is how many of an account's posts fall in a topic.
type TopicShare struct {
	Ref
	Posts int `json:"posts"`
}

// AuthorDetail is one account in full.
type AuthorDetail struct {
	AuthorSummary
	Snapshot    string       `json:"snapshot"`
	Followers   int          `json:"followers"`
	Following   int          `json:"following"`
	TopTopics   []TopicShare `json:"top_topics"`
	RecentPosts []PostView   `json:"recent_posts"`
	TopPosts    []PostView   `json:"top_posts"`
}

func (a *API) handleAuthor(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	did, err := a.resolveAuthor(ctx, r.PathValue("ref"))
	if err != nil {
		return nil, err
	}
	sum, err := scanAuthor(a.cfg.DB.QueryRowContext(ctx, authorsSelect+" WHERE p.did = ? GROUP BY p.did", did))
	if err != nil {
		return nil, notFound("that account has no posts in the data")
	}
	out := AuthorDetail{AuthorSummary: sum, Snapshot: snap.ID, TopTopics: []TopicShare{}}
	if err := a.cfg.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM interactions WHERE kind='follow' AND subject = ?),
		(SELECT count(*) FROM interactions WHERE kind='follow' AND did = ?)`, did, did).Scan(&out.Followers, &out.Following); err != nil {
		return nil, err
	}
	if ai, ok := snap.AuthorByDID[did]; ok {
		counts := map[int]int{}
		for _, i := range snap.ByAuthor[ai] {
			counts[snap.C.Topic[i]]++
		}
		for tid, n := range counts {
			if ref := snap.topicRef(tid); ref != nil {
				out.TopTopics = append(out.TopTopics, TopicShare{Ref: *ref, Posts: n})
			}
		}
		sortShares(out.TopTopics)
		if len(out.TopTopics) > 8 {
			out.TopTopics = out.TopTopics[:8]
		}
	}
	recent, _, err := a.queryPosts(ctx, postQuery{did: did, sort: "recent", limit: 8})
	if err != nil {
		return nil, err
	}
	top, _, err := a.queryPosts(ctx, postQuery{did: did, sort: "engaged", limit: 5})
	if err != nil {
		return nil, err
	}
	out.RecentPosts, out.TopPosts = snap.views(recent), snap.views(top)
	return out, nil
}

func sortShares(s []TopicShare) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && (s[j].Posts > s[j-1].Posts || (s[j].Posts == s[j-1].Posts && s[j].ID < s[j-1].ID)); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---- relationships

// Edge is an account on the other end of some relationship, with how many times it happened.
type Edge struct {
	AuthorRef
	Count int `json:"count"`
}

// Network is who an account follows, replies to and likes, and who does the same to it.
type Network struct {
	Snapshot        string      `json:"snapshot"`
	Account         AuthorRef   `json:"account"`
	Follows         []AuthorRef `json:"follows"`
	FollowedBy      []AuthorRef `json:"followed_by"`
	FollowsTotal    int         `json:"follows_total"`
	FollowedByTotal int         `json:"followed_by_total"`
	RepliesTo       []Edge      `json:"replies_to"`
	RepliedBy       []Edge      `json:"replied_by"`
	LikesTo         []Edge      `json:"likes_to"`
	LikedBy         []Edge      `json:"liked_by"`
}

// authorRefs looks up handles and names for a set of DIDs.
func (a *API) authorRefs(ctx context.Context, dids []string) (map[string]AuthorRef, error) {
	out := make(map[string]AuthorRef, len(dids))
	for _, d := range dids {
		out[d] = AuthorRef{DID: d}
	}
	if len(dids) == 0 {
		return out, nil
	}
	j, _ := json.Marshal(dids)
	rows, err := a.cfg.DB.QueryContext(ctx, `SELECT did, handle, display_name FROM actors WHERE did IN (SELECT value FROM json_each(?))`, string(j))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AuthorRef
		if err := rows.Scan(&r.DID, &r.Handle, &r.Name); err != nil {
			return nil, err
		}
		out[r.DID] = r
	}
	return out, rows.Err()
}

// edgeQuery runs a query returning (did, count) rows and attaches names.
func (a *API) edges(ctx context.Context, q string, args ...any) ([]Edge, error) {
	rows, err := a.cfg.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dids []string
	var counts []int
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		dids, counts = append(dids, d), append(counts, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs, err := a.authorRefs(ctx, dids)
	if err != nil {
		return nil, err
	}
	out := make([]Edge, len(dids))
	for i, d := range dids {
		out[i] = Edge{AuthorRef: refs[d], Count: counts[i]}
	}
	return out, nil
}

const parentDID = `substr(c.reply_parent, 6, instr(substr(c.reply_parent, 6), '/') - 1)`

func (a *API) handleNetwork(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	did, err := a.resolveAuthor(ctx, r.PathValue("ref"))
	if err != nil {
		return nil, err
	}
	refs, err := a.authorRefs(ctx, []string{did})
	if err != nil {
		return nil, err
	}
	n := Network{Snapshot: snap.ID, Account: refs[did]}

	listDIDs := func(q string, total *int) ([]AuthorRef, error) {
		rows, err := a.cfg.DB.QueryContext(ctx, q, did)
		if err != nil {
			return nil, err
		}
		var dids []string
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				return nil, err
			}
			dids = append(dids, d)
		}
		rows.Close()
		*total = len(dids)
		if len(dids) > 50 {
			dids = dids[:50]
		}
		rs, err := a.authorRefs(ctx, dids)
		if err != nil {
			return nil, err
		}
		out := make([]AuthorRef, len(dids))
		for i, d := range dids {
			out[i] = rs[d]
		}
		return out, nil
	}
	if n.Follows, err = listDIDs(`SELECT subject FROM interactions WHERE kind='follow' AND did = ? ORDER BY created_at DESC`, &n.FollowsTotal); err != nil {
		return nil, err
	}
	if n.FollowedBy, err = listDIDs(`SELECT did FROM interactions WHERE kind='follow' AND subject = ? ORDER BY created_at DESC`, &n.FollowedByTotal); err != nil {
		return nil, err
	}
	if n.RepliesTo, err = a.edges(ctx, `SELECT `+parentDID+` AS other, count(*) FROM posts c WHERE c.did = ? AND c.reply_parent <> '' AND other <> c.did GROUP BY other ORDER BY 2 DESC, other LIMIT 25`, did); err != nil {
		return nil, err
	}
	if n.RepliedBy, err = a.edges(ctx, `SELECT c.did, count(*) FROM posts c WHERE c.reply_parent LIKE ? AND c.did <> ? GROUP BY c.did ORDER BY 2 DESC, c.did LIMIT 25`, "at://"+did+"/%", did); err != nil {
		return nil, err
	}
	if n.LikesTo, err = a.edges(ctx, `SELECT q.did, count(*) FROM interactions i JOIN posts q ON q.uri = i.subject WHERE i.kind='like' AND i.did = ? AND q.did <> i.did GROUP BY q.did ORDER BY 2 DESC, q.did LIMIT 25`, did); err != nil {
		return nil, err
	}
	if n.LikedBy, err = a.edges(ctx, `SELECT i.did, count(*) FROM interactions i JOIN posts q ON q.uri = i.subject WHERE i.kind='like' AND q.did = ? AND i.did <> q.did GROUP BY i.did ORDER BY 2 DESC, i.did LIMIT 25`, did); err != nil {
		return nil, err
	}
	return n, nil
}

// ---- GET /api/v1/graph/replies

// ReplyEdge is one account replying to another, summed over the window.
type ReplyEdge struct {
	From    AuthorRef `json:"from"`
	To      AuthorRef `json:"to"`
	Replies int       `json:"replies"`
}

func (a *API) handleReplyGraph(r *http.Request) (any, error) {
	snap, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	p := a.params(r)
	since, until := p.timeMS("since"), p.timeMS("until")
	minCount := p.integer("min_count", 3, 1, 1_000_000)
	self := p.boolean("include_self")
	pg := p.page(100, 1000)
	if err := p.Err(); err != nil {
		return nil, err
	}
	where := []string{"c.reply_parent <> ''"}
	var args []any
	if since > 0 {
		where, args = append(where, "c.created_at >= ?"), append(args, since)
	}
	if until > 0 {
		where, args = append(where, "c.created_at <= ?"), append(args, until)
	}
	if self == nil || !*self {
		where = append(where, parentDID+" <> c.did")
	}
	base := `SELECT c.did AS src, ` + parentDID + ` AS dst, count(*) AS n FROM posts c WHERE ` + strings.Join(where, " AND ") +
		` GROUP BY src, dst HAVING n >= ?`
	args = append(args, minCount)
	var total int
	if err := a.cfg.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM (`+base+`)`, args...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := a.cfg.DB.QueryContext(r.Context(), base+` ORDER BY n DESC, src, dst LIMIT ? OFFSET ?`, append(args, pg.limit, pg.offset)...)
	if err != nil {
		return nil, err
	}
	type raw struct {
		src, dst string
		n        int
	}
	var rs []raw
	var dids []string
	for rows.Next() {
		var e raw
		if err := rows.Scan(&e.src, &e.dst, &e.n); err != nil {
			rows.Close()
			return nil, err
		}
		rs, dids = append(rs, e), append(dids, e.src, e.dst)
	}
	rows.Close()
	refs, err := a.authorRefs(r.Context(), dids)
	if err != nil {
		return nil, err
	}
	out := make([]ReplyEdge, len(rs))
	for i, e := range rs {
		out[i] = ReplyEdge{From: refs[e.src], To: refs[e.dst], Replies: e.n}
	}
	return newList(snap.ID, out, total, pg), nil
}
