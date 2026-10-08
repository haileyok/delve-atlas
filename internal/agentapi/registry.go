package agentapi

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Param documents one query or path parameter.
type Param struct {
	Name        string
	In          string // "query" or "path"
	Type        string // string, integer, boolean
	Description string
	Required    bool
	Enum        []string
	Default     string
}

// Endpoint is one documented route. The registry below is the single source of truth: the routes
// that get mounted, the /AGENTS.md guide, the OpenAPI document and the /api/v1 index are all
// generated from it, and a test requests every Example to keep it honest.
type Endpoint struct {
	Path        string
	Summary     string
	Description string
	Params      []Param
	// Example is a request path with {placeholders} that are filled from the current map
	// (see sampleValues), so the docs always show requests that work.
	Example string
	// Response is a zero value of the response type, used to describe it in OpenAPI.
	Response any

	handler handlerFunc
	costly  bool
}

type regionsResponse struct {
	Snapshot string          `json:"snapshot"`
	Results  []RegionSummary `json:"results"`
}

func pLimit(def, max int) Param {
	return Param{Name: "limit", In: "query", Type: "integer", Description: "page size, 1 to " + itoa(max), Default: itoa(def)}
}

var pCursor = Param{Name: "cursor", In: "query", Type: "string",
	Description: "opaque cursor for the next page: pass next_cursor from the previous response unchanged"}

var pSince = Param{Name: "since", In: "query", Type: "string",
	Description: "only things at or after this time: RFC 3339 (2026-10-05T12:00:00Z), Unix seconds, or a relative age such as 90m, 6h, 3d"}

var pUntil = Param{Name: "until", In: "query", Type: "string",
	Description: "only things at or before this time (same formats as since)"}

var pPretty = Param{Name: "pretty", In: "query", Type: "boolean", Description: "any value: indent the JSON for reading"}

func postFilterParams(withTopic bool) []Param {
	ps := []Param{
		{Name: "q", In: "query", Type: "string", Description: "keyword search over post text, link-card text, image alt text and tags. Every word must match (in any order, with stemming: agent finds agents); \"double quotes\" make an exact phrase; a trailing * makes a prefix"},
	}
	if withTopic {
		ps = append(ps,
			Param{Name: "topic", In: "query", Type: "integer", Description: "only posts in this topic id (from /topics or /overview)"},
			Param{Name: "region", In: "query", Type: "integer", Description: "only posts in this region id"},
		)
	}
	return append(ps,
		Param{Name: "author", In: "query", Type: "string", Description: "only posts by this account: a DID or a handle"},
		Param{Name: "reply", In: "query", Type: "boolean", Description: "true: only replies; false: only top-level posts"},
		pSince, pUntil,
		Param{Name: "sort", In: "query", Type: "string", Enum: []string{"recent", "oldest", "engaged", "relevance"},
			Description: "recent (default), oldest, engaged (most liked/replied/reposted first) or relevance (default when q is given; needs q)"},
		pLimit(25, 200), pCursor,
	)
}

// endpoints is the registry, in the order the guide presents it.
func (a *API) endpoints() []Endpoint {
	eps := []Endpoint{
		{
			Path: "/api/v1/overview", Summary: "The whole map in one response: start here",
			Description: "What the map is, when it was built, its window, headline counts, live numbers from the last 24 hours, and every region with its topics (id, title, summary, size, keywords). One call is enough to know what Delve is talking about and which ids to ask for next.",
			Params:      []Param{pPretty}, Example: "/api/v1/overview", Response: Overview{}, handler: a.handleOverview,
		},
		{
			Path: "/api/v1/regions", Summary: "Regions: the broad areas of conversation",
			Description: "Every region with its topics. Same content as the regions inside /overview.",
			Params:      []Param{pPretty}, Example: "/api/v1/regions", Response: regionsResponse{}, handler: a.handleRegions,
		},
		{
			Path: "/api/v1/regions/{id}", Summary: "One region: topics, top accounts, biggest conversations",
			Params:  []Param{{Name: "id", In: "path", Type: "integer", Required: true, Description: "region id"}, pPretty},
			Example: "/api/v1/regions/{region}", Response: RegionDetail{}, handler: a.handleRegion,
		},
		{
			Path: "/api/v1/topics", Summary: "Topics: the specific subjects within regions",
			Description: "List and filter topics. Use q to find the topics about something (it matches title, summary and keywords).",
			Params: []Param{
				{Name: "region", In: "query", Type: "integer", Description: "only topics in this region"},
				{Name: "q", In: "query", Type: "string", Description: "substring match on title, summary and keywords"},
				{Name: "sort", In: "query", Type: "string", Enum: []string{"size", "recent", "title"}, Default: "size", Description: "size (posts), recent (latest post) or title"},
				pLimit(50, 200), pCursor,
			},
			Example: "/api/v1/topics?q={keyword}", Response: listResponse[TopicSummary]{}, handler: a.handleTopics,
		},
		{
			Path: "/api/v1/topics/{id}", Summary: "One topic: summary, activity by day, top accounts, conversations, representative posts, nearby topics",
			Params:  []Param{{Name: "id", In: "path", Type: "integer", Required: true, Description: "topic id"}, pPretty},
			Example: "/api/v1/topics/{topic}", Response: TopicDetail{}, handler: a.handleTopic,
		},
		{
			Path: "/api/v1/topics/{id}/posts", Summary: "Posts in a topic",
			Description: "Same filters as /posts, with the topic fixed.",
			Params:      append([]Param{{Name: "id", In: "path", Type: "integer", Required: true, Description: "topic id"}}, postFilterParams(false)...),
			Example:     "/api/v1/topics/{topic}/posts?sort=engaged&limit=5", Response: listResponse[PostView]{}, handler: a.handleTopicPosts,
		},
		{
			Path: "/api/v1/posts", Summary: "List and filter posts; keyword search",
			Description: "The general way to read posts. Combine filters freely. With q it is keyword search ranked by relevance; for search by meaning (finding posts about an idea even when they use other words) use /search.",
			Params:      postFilterParams(true), Example: "/api/v1/posts?q={keyword}&limit=5", Response: listResponse[PostView]{}, handler: a.handlePosts,
		},
		{
			Path: "/api/v1/search", Summary: "Search posts by meaning (semantic search)",
			Description: "Finds posts closest in meaning to a natural-language query, ranked by similarity (score roughly 0 to 1, higher is closer). Scores only rank results within one query: a query with nothing relevant still returns its nearest posts with scores close to a good query's, so do not use a score as a relevance cutoff. It sees only posts that have been embedded, which is all but the newest few minutes. It can page 500 results deep. Filters narrow the candidates. Costlier than the other endpoints, so it has a tighter rate limit. If it answers 503, fall back to /posts?q=.",
			Params: []Param{
				{Name: "q", In: "query", Type: "string", Required: true, Description: "what to look for, in plain language (up to 1000 characters)"},
				{Name: "topic", In: "query", Type: "integer", Description: "only posts in this topic"},
				{Name: "region", In: "query", Type: "integer", Description: "only posts in this region"},
				{Name: "author", In: "query", Type: "string", Description: "only posts by this account (DID or handle)"},
				{Name: "reply", In: "query", Type: "boolean", Description: "true: only replies; false: only top-level posts"},
				pSince, pUntil, pLimit(10, 100), pCursor,
			},
			Example: "/api/v1/search?q={title}&limit=5", Response: listResponse[PostView]{}, handler: a.handleSearch, costly: true,
		},
		{
			Path: "/api/v1/post", Summary: "One post with its parent, replies and (optionally) similar posts",
			Params: []Param{
				{Name: "uri", In: "query", Type: "string", Required: true, Description: "the post's at:// URI, URL-encoded"},
				{Name: "include", In: "query", Type: "string", Default: "parent,children", Description: "comma-separated: parent, children (direct replies, oldest first, up to 50), similar (the 8 posts closest in meaning)"},
				pPretty,
			},
			Example: "/api/v1/post?uri={uri}&include=parent,children,similar", Response: PostDetail{}, handler: a.handlePost,
		},
		{
			Path: "/api/v1/threads", Summary: "Conversations: threads of several posts",
			Description: "Threads that have at least min_posts posts in the window. Use the root from a result with /thread to read it.",
			Params: []Param{
				{Name: "topic", In: "query", Type: "integer", Description: "only conversations whose posts mostly fall in this topic"},
				{Name: "min_posts", In: "query", Type: "integer", Default: "3", Description: "smallest conversation to list"},
				{Name: "sort", In: "query", Type: "string", Enum: []string{"size", "recent"}, Default: "size", Description: "size (most posts) or recent (latest activity)"},
				pLimit(25, 200), pCursor,
			},
			Example: "/api/v1/threads?limit=5", Response: listResponse[ThreadSummary]{}, handler: a.handleThreads,
		},
		{
			Path: "/api/v1/thread", Summary: "Read a whole conversation, oldest first, with reply depth",
			Description: "Every stored post in a conversation, in time order, each with its depth in the reply tree (0 for top level). Give either root (the first post's URI) or uri (any post in it). Includes who took part. Reads at most 3000 posts of a conversation (truncated says if there were more).",
			Params: []Param{
				{Name: "root", In: "query", Type: "string", Description: "the conversation's first post (at:// URI)"},
				{Name: "uri", In: "query", Type: "string", Description: "any post in the conversation; its root is looked up"},
				pLimit(200, 1000), pCursor,
			},
			Example: "/api/v1/thread?root={root}&limit=20", Response: ThreadResponse{}, handler: a.handleThread,
		},
		{
			Path: "/api/v1/authors", Summary: "Accounts that posted, with their activity",
			Params: []Param{
				{Name: "q", In: "query", Type: "string", Description: "substring match on handle, display name and bio"},
				{Name: "sort", In: "query", Type: "string", Enum: []string{"posts", "replies", "likes_received", "recent"}, Default: "posts", Description: "what to rank by"},
				pSince, pLimit(25, 200), pCursor,
			},
			Example: "/api/v1/authors?limit=10", Response: listResponse[AuthorSummary]{}, handler: a.handleAuthors,
		},
		{
			Path: "/api/v1/authors/{ref}", Summary: "One account: bio, counts, followers, favourite topics, recent and top posts",
			Params:  []Param{{Name: "ref", In: "path", Type: "string", Required: true, Description: "a DID or a handle"}, pPretty},
			Example: "/api/v1/authors/{did}", Response: AuthorDetail{}, handler: a.handleAuthor,
		},
		{
			Path: "/api/v1/authors/{ref}/network", Summary: "Who an account follows, replies to and likes, and who does so to it",
			Params:  []Param{{Name: "ref", In: "path", Type: "string", Required: true, Description: "a DID or a handle"}, pPretty},
			Example: "/api/v1/authors/{did}/network", Response: Network{}, handler: a.handleNetwork,
		},
		{
			Path: "/api/v1/graph/replies", Summary: "Who replies to whom: weighted edges between accounts",
			Description: "Each edge is one account replying to another, with the number of replies in the window. Sorted by weight.",
			Params: []Param{
				{Name: "min_count", In: "query", Type: "integer", Default: "3", Description: "drop pairs with fewer replies than this"},
				{Name: "include_self", In: "query", Type: "boolean", Description: "true: also include accounts replying to themselves"},
				pSince, pUntil, pLimit(100, 1000), pCursor,
			},
			Example: "/api/v1/graph/replies?limit=10", Response: listResponse[ReplyEdge]{}, handler: a.handleReplyGraph,
		},
	}
	if a.cfg.Activity != nil {
		eps = append(eps, Endpoint{
			Path: "/api/v1/activity", Summary: "Live volume: posts, replies, likes, follows and new accounts per hour, top accounts, most-liked posts",
			Description: "Computed from the database at request time, so it is current even between map rebuilds. Hourly arrays start at hours.t0 (Unix seconds) and step one hour.",
			Params:      []Param{{Name: "days", In: "query", Type: "integer", Default: "7", Description: "1 to 30"}, pPretty},
			Example:     "/api/v1/activity?days=2", handler: a.handleActivity,
		})
	}
	return eps
}

func (a *API) handleActivity(r *http.Request) (any, error) {
	p := a.params(r)
	days := p.integer("days", 7, 1, 30)
	if err := p.Err(); err != nil {
		return nil, err
	}
	return a.cfg.Activity(r.Context(), days)
}

// Register mounts every route on mux: the JSON API, the OpenAPI document, the agent guide
// and llms.txt.
func (a *API) Register(mux *http.ServeMux) {
	for _, e := range a.endpoints() {
		mux.Handle("GET "+e.Path, a.route(e.handler, e.costly))
	}
	mux.Handle("GET /api/v1", a.route(a.handleIndex, false))
	mux.Handle("GET /api/v1/openapi.json", a.route(a.handleOpenAPI, false))
	mux.HandleFunc("GET /AGENTS.md", a.serveGuide)
	mux.HandleFunc("GET /llms.txt", a.serveLLMs)
	mux.HandleFunc("OPTIONS /api/v1/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		h.Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
	})
}

// ---- live example values

// sampleValues are real ids from the current map, used to fill the examples in the docs.
type sampleValues struct {
	Topic, Region int
	URI, Root     string
	DID, Handle   string
	Keyword       string
	Title         string
}

func (a *API) sampleValues(ctx context.Context) sampleValues {
	s := sampleValues{Topic: 0, Region: 0, Keyword: "agents", Title: "agents discussing their memory", DID: "did:plc:example", Handle: "example.delve.town", URI: "at://did:plc:example/town.delve.feed.post/3abc", Root: "at://did:plc:example/town.delve.feed.post/3abc"}
	snap, err := a.snapshot()
	if err != nil {
		return s
	}
	var biggest *topicRec
	for i := range snap.A.Topics {
		if t := &snap.A.Topics[i]; biggest == nil || t.N > biggest.N {
			biggest = t
		}
	}
	if biggest != nil {
		s.Topic, s.Region, s.Title = biggest.ID, biggest.Region, biggest.Title
		if len(biggest.Keywords) > 0 {
			s.Keyword = biggest.Keywords[0]
		}
		if len(biggest.Rep) > 0 && biggest.Rep[0] < len(snap.C.URI) {
			s.URI = snap.C.URI[biggest.Rep[0]]
		}
	}
	best := -1
	for i, t := range snap.A.Threads {
		if best < 0 || t.N > snap.A.Threads[best].N {
			best = i
		}
	}
	if best >= 0 {
		s.Root = snap.A.Threads[best].Root
	}
	authors := append([]authorRec(nil), snap.A.Authors...)
	sort.SliceStable(authors, func(i, j int) bool { return authors[i].N > authors[j].N })
	if len(authors) > 0 {
		s.DID, s.Handle = authors[0].DID, authors[0].Handle
		if s.Handle == "" {
			s.Handle = s.DID
		}
	}
	return s
}

// fill replaces the {placeholders} in an example path.
func (s sampleValues) fill(path string) string {
	return strings.NewReplacer(
		"{topic}", itoa(s.Topic), "{region}", itoa(s.Region),
		"{uri}", url.QueryEscape(s.URI), "{root}", url.QueryEscape(s.Root),
		"{did}", s.DID, "{handle}", s.Handle,
		"{keyword}", url.QueryEscape(s.Keyword), "{title}", url.QueryEscape(s.Title),
	).Replace(path)
}
