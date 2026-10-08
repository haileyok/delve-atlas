package agentapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The shapes of atlas.json and cols.json as written by pipeline/build_atlas.py.

type atlasFile struct {
	ID         string      `json:"id"`
	BuiltAt    int64       `json:"built_at"`
	WindowDays float64     `json:"window_days"`
	NPosts     int         `json:"n_posts"`
	NAuthors   int         `json:"n_authors"`
	NFollows   int         `json:"n_follows"`
	NTopics    int         `json:"n_topics"`
	TsRange    [2]int64    `json:"ts_range"`
	Regions    []regionRec `json:"regions"`
	Topics     []topicRec  `json:"topics"`
	Threads    []threadRec `json:"threads"`
	Authors    []authorRec `json:"authors"`
	Model      string      `json:"model"`
	LabelModel *string     `json:"label_model"`
}

type regionRec struct {
	ID      int     `json:"id"`
	N       int     `json:"n"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Topics  []int   `json:"topics"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
}

type topicRec struct {
	ID         int      `json:"id"`
	Region     int      `json:"region"`
	N          int      `json:"n"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	R          float64  `json:"r"`
	Keywords   []string `json:"keywords"`
	Authors    int      `json:"authors"`
	TopAuthors []int    `json:"top_authors"`
	First      int64    `json:"first"`
	Last       int64    `json:"last"`
	Rep        []int    `json:"rep"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
}

type threadRec struct {
	Root    string `json:"root"`
	N       int    `json:"n"`
	Topic   int    `json:"topic"`
	Authors int    `json:"authors"`
	First   int64  `json:"first"`
	Last    int64  `json:"last"`
	Title   string `json:"title"`
}

type authorRec struct {
	DID    string `json:"did"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
	N      int    `json:"n"`
	Topics []int  `json:"topics"`
}

type colsFile struct {
	URI     []string `json:"uri"`
	Text    []string `json:"text"`
	Author  []int    `json:"author"`
	Ts      []int64  `json:"ts"`
	Topic   []int    `json:"topic"`
	Region  []int    `json:"region"`
	Thread  []int    `json:"thread"`
	Parent  []int    `json:"parent"`
	Reply   []int    `json:"reply"`
	Likes   []int    `json:"likes"`
	Replies []int    `json:"replies"`
	Reposts []int    `json:"reposts"`
	Kind    []string `json:"kind"`
}

// Snapshot is one built map with the lookups the endpoints need.
type Snapshot struct {
	ID string
	A  atlasFile
	C  colsFile

	Idx          map[string]int // post URI -> row in C
	Topics       map[int]*topicRec
	Regions      map[int]*regionRec
	ByTopic      map[int][]int // topic id -> rows in C
	ByRegion     map[int][]int
	ThreadByRoot map[string]int
	AuthorByDID  map[string]int
	ByAuthor     map[int][]int // author index (into A.Authors) -> rows in C
}

func loadSnapshot(dir, id string) (*Snapshot, error) {
	s := &Snapshot{ID: id}
	read := func(name string, v any) error {
		b, err := os.ReadFile(filepath.Join(dir, id, name))
		if err != nil {
			return err
		}
		return json.Unmarshal(b, v)
	}
	if err := read("atlas.json", &s.A); err != nil {
		return nil, fmt.Errorf("read atlas.json: %w", err)
	}
	if err := read("cols.json", &s.C); err != nil {
		return nil, fmt.Errorf("read cols.json: %w", err)
	}
	n := len(s.C.URI)
	s.Idx = make(map[string]int, n)
	for i, u := range s.C.URI {
		s.Idx[u] = i
	}
	s.Topics = make(map[int]*topicRec, len(s.A.Topics))
	for i := range s.A.Topics {
		s.Topics[s.A.Topics[i].ID] = &s.A.Topics[i]
	}
	s.Regions = make(map[int]*regionRec, len(s.A.Regions))
	for i := range s.A.Regions {
		s.Regions[s.A.Regions[i].ID] = &s.A.Regions[i]
	}
	s.ByTopic = map[int][]int{}
	s.ByRegion = map[int][]int{}
	for i := 0; i < n; i++ {
		s.ByTopic[s.C.Topic[i]] = append(s.ByTopic[s.C.Topic[i]], i)
		s.ByRegion[s.C.Region[i]] = append(s.ByRegion[s.C.Region[i]], i)
	}
	s.ThreadByRoot = make(map[string]int, len(s.A.Threads))
	for i, t := range s.A.Threads {
		s.ThreadByRoot[t.Root] = i
	}
	s.AuthorByDID = make(map[string]int, len(s.A.Authors))
	for i, a := range s.A.Authors {
		s.AuthorByDID[a.DID] = i
	}
	s.ByAuthor = map[int][]int{}
	for i := 0; i < n; i++ {
		s.ByAuthor[s.C.Author[i]] = append(s.ByAuthor[s.C.Author[i]], i)
	}
	return s, nil
}

// snapCache keeps the newest snapshot in memory and notices when a newer one is built.
type snapCache struct {
	mu      sync.Mutex
	cur     *Snapshot
	checked time.Time
}

// snapshot returns the newest map. The directory is only re-checked every few seconds.
func (a *API) snapshot() (*Snapshot, error) {
	c := &a.snaps
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && time.Since(c.checked) < 5*time.Second {
		return c.cur, nil
	}
	target, err := os.Readlink(filepath.Join(a.cfg.AtlasDir, "latest"))
	if err != nil {
		if c.cur != nil {
			return c.cur, nil // keep serving what we have
		}
		return nil, &apiError{Status: 503, Code: "snapshot_unavailable", Message: "no map has been built yet"}
	}
	c.checked = time.Now()
	id := filepath.Base(target)
	if c.cur != nil && c.cur.ID == id {
		return c.cur, nil
	}
	s, err := loadSnapshot(a.cfg.AtlasDir, id)
	if err != nil {
		if c.cur != nil {
			a.cfg.Log.Error("loading new snapshot failed; keeping the previous one", "id", id, "err", err)
			return c.cur, nil
		}
		return nil, &apiError{Status: 503, Code: "snapshot_unavailable", Message: "the map could not be loaded"}
	}
	c.cur = s
	return s, nil
}

// ---- small views shared by the endpoints

// Ref names a topic or region.
type Ref struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

func (s *Snapshot) topicRef(id int) *Ref {
	if t, ok := s.Topics[id]; ok {
		return &Ref{ID: t.ID, Title: t.Title}
	}
	return nil
}

func (s *Snapshot) regionRef(id int) *Ref {
	if r, ok := s.Regions[id]; ok {
		return &Ref{ID: r.ID, Title: r.Title}
	}
	return nil
}

func rfc3339(unixMS int64) string { return time.UnixMilli(unixMS).UTC().Format(time.RFC3339) }
func rfc3339s(unixS int64) string { return time.Unix(unixS, 0).UTC().Format(time.RFC3339) }
