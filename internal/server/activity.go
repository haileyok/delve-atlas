package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Activity is live network activity computed straight from the database, so it is current
// even between atlas rebuilds.
type Activity struct {
	Now         int64             `json:"now"`
	Since       int64             `json:"since"`
	Hours       Hourly            `json:"hours"`
	Totals      Totals            `json:"totals"`
	TopPosters  []Poster          `json:"top_posters"`
	TopPosts    []PostView        `json:"top_posts"`
	Collections []CollectionCount `json:"collections"`
}

// Hourly has one entry per hour from T0 (unix seconds).
type Hourly struct {
	T0       int64 `json:"t0"`
	Posts    []int `json:"posts"`
	Replies  []int `json:"replies"`
	Likes    []int `json:"likes"`
	Reposts  []int `json:"reposts"`
	Follows  []int `json:"follows"`
	NewUsers []int `json:"new_users"`
}

// Totals over the window.
type Totals struct {
	Posts    int `json:"posts"`
	Replies  int `json:"replies"`
	Likes    int `json:"likes"`
	Reposts  int `json:"reposts"`
	Follows  int `json:"follows"`
	Accounts int `json:"accounts"`
	Posters  int `json:"posters"`
}

// Poster is one account's activity.
type Poster struct {
	DID     string `json:"did"`
	Handle  string `json:"handle"`
	Name    string `json:"name"`
	Posts   int    `json:"posts"`
	Replies int    `json:"replies"`
	Likes   int    `json:"likes_received"`
}

// CollectionCount is how many events of one collection were seen.
type CollectionCount struct {
	Collection string `json:"collection"`
	Creates    int    `json:"creates"`
	Deletes    int    `json:"deletes"`
}

func (s *Server) activityHandler(w http.ResponseWriter, r *http.Request) {
	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d >= 1 && d <= 30 {
		days = d
	}
	s.mu.Lock()
	if days == 7 && time.Since(s.activity.at) < time.Minute && s.activity.body != nil {
		body := s.activity.body
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
		return
	}
	s.mu.Unlock()

	a, err := s.ComputeActivity(r.Context(), days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(a)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if days == 7 {
		s.mu.Lock()
		s.activity = cached{at: time.Now(), body: body}
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// ComputeActivity gathers live activity for the last days days. It backs both the site's own
// activity tab and the agent API's /api/v1/activity.
func (s *Server) ComputeActivity(ctx context.Context, days int) (*Activity, error) {
	now := time.Now()
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	t0 := since.Unix() / 3600 * 3600
	sinceMS := t0 * 1000
	n := int((now.Unix()-t0)/3600) + 1
	mk := func() []int { return make([]int, n) }
	a := &Activity{Now: now.Unix(), Since: t0, Hours: Hourly{T0: t0, Posts: mk(), Replies: mk(), Likes: mk(), Reposts: mk(), Follows: mk(), NewUsers: mk()}}

	bin := func(q string, args []any, dst []int) error {
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h int64
			var c int
			if err := rows.Scan(&h, &c); err != nil {
				return err
			}
			if i := int(h - t0/3600); i >= 0 && i < n {
				dst[i] += c
			}
		}
		return rows.Err()
	}
	args := []any{sinceMS}
	if err := bin(`SELECT created_at/3600000, count(*) FROM posts WHERE created_at >= ? AND reply_parent = '' GROUP BY 1`, args, a.Hours.Posts); err != nil {
		return nil, err
	}
	if err := bin(`SELECT created_at/3600000, count(*) FROM posts WHERE created_at >= ? AND reply_parent <> '' GROUP BY 1`, args, a.Hours.Replies); err != nil {
		return nil, err
	}
	for kind, dst := range map[string][]int{"like": a.Hours.Likes, "repost": a.Hours.Reposts, "follow": a.Hours.Follows} {
		if err := bin(`SELECT created_at/3600000, count(*) FROM interactions WHERE kind = '`+kind+`' AND created_at >= ? GROUP BY 1`, args, dst); err != nil {
			return nil, err
		}
	}
	// An account's first sign of life, from its earliest post or interaction.
	if err := bin(`SELECT m/3600000, count(*) FROM (
			SELECT did, min(t) AS m FROM (
				SELECT did, created_at AS t FROM posts UNION ALL SELECT did, created_at FROM interactions
			) GROUP BY did) WHERE m >= ? GROUP BY 1`, args, a.Hours.NewUsers); err != nil {
		return nil, err
	}
	sum := func(x []int) (t int) {
		for _, v := range x {
			t += v
		}
		return
	}
	a.Totals = Totals{
		Posts: sum(a.Hours.Posts) + sum(a.Hours.Replies), Replies: sum(a.Hours.Replies),
		Likes: sum(a.Hours.Likes), Reposts: sum(a.Hours.Reposts), Follows: sum(a.Hours.Follows),
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT count(DISTINCT did) FROM posts WHERE created_at >= ?`, sinceMS).Scan(&a.Totals.Posters); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM (
			SELECT did FROM posts WHERE created_at >= ?1 UNION SELECT did FROM interactions WHERE created_at >= ?1)`, sinceMS).Scan(&a.Totals.Accounts); err != nil {
		return nil, err
	}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT p.did, COALESCE(a.handle,''), COALESCE(a.display_name,''), count(*),
		       sum(p.reply_parent <> ''),
		       (SELECT count(*) FROM interactions i JOIN posts q ON q.uri = i.subject
		         WHERE q.did = p.did AND i.kind = 'like' AND i.created_at >= ?1)
		FROM posts p LEFT JOIN actors a ON a.did = p.did
		WHERE p.created_at >= ?1 GROUP BY p.did ORDER BY count(*) DESC LIMIT 12`, sinceMS)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p Poster
		if err := rows.Scan(&p.DID, &p.Handle, &p.Name, &p.Posts, &p.Replies, &p.Likes); err != nil {
			rows.Close()
			return nil, err
		}
		a.TopPosters = append(a.TopPosters, p)
	}
	rows.Close()

	prows, err := s.DB.QueryContext(ctx, postSelect+`
		WHERE p.created_at >= ?1 AND (SELECT count(*) FROM interactions i WHERE i.subject = p.uri) > 0
		ORDER BY (SELECT count(*) FROM interactions i WHERE i.subject = p.uri) DESC, p.created_at DESC LIMIT 8`, sinceMS)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		p, err := scanPost(prows)
		if err != nil {
			prows.Close()
			return nil, err
		}
		if len(p.Text) > 400 {
			p.Text = p.Text[:400] + "…"
		}
		a.TopPosts = append(a.TopPosts, p)
	}
	prows.Close()

	crows, err := s.DB.QueryContext(ctx, `SELECT collection, sum(CASE WHEN op='delete' THEN 0 ELSE n END), sum(CASE WHEN op='delete' THEN n ELSE 0 END)
		FROM collection_stats GROUP BY collection ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var c CollectionCount
		if err := crows.Scan(&c.Collection, &c.Creates, &c.Deletes); err != nil {
			crows.Close()
			return nil, err
		}
		a.Collections = append(a.Collections, c)
	}
	crows.Close()
	return a, nil
}
