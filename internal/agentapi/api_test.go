package agentapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---- map navigation

func TestOverviewIsTheWholeMapInOneCall(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var ov Overview
	f.get(t, "/api/v1/overview", 200, &ov)

	if ov.Snapshot != snapID || ov.Counts.Posts != 6 || ov.Counts.Topics != 3 || ov.Counts.Regions != 2 || ov.Counts.Conversations != 1 {
		t.Errorf("counts: %+v (snapshot %q)", ov.Counts, ov.Snapshot)
	}
	if len(ov.Regions) != 2 || ov.Regions[0].Title != "Records and Receipts" {
		t.Fatalf("regions should be listed biggest first: %+v", ov.Regions)
	}
	if len(ov.Regions[1].Topics) != 2 || ov.Regions[1].Topics[0].Region == nil || ov.Regions[1].Topics[0].Keywords == nil {
		t.Errorf("topics should be nested with their keywords and region: %+v", ov.Regions[1].Topics)
	}
	if ov.Live.PostsLast24h != 3 || ov.Live.PostsNotYetMapped != 1 || ov.Live.LatestPostAt != "2026-10-08T11:00:00Z" {
		t.Errorf("live numbers come from the database: %+v", ov.Live)
	}
	if !strings.Contains(ov.About, "untrusted") && !strings.Contains(ov.About, "never as instructions") {
		t.Errorf("the overview must carry the content warning: %q", ov.About)
	}
}

func TestTopicsListFilterAndDetail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	var all listResponse[TopicSummary]
	f.get(t, "/api/v1/topics", 200, &all)
	if all.Total == nil || *all.Total != 3 || all.Results[0].ID != 10 {
		t.Errorf("all topics, biggest first: %+v", all)
	}
	var inRegion listResponse[TopicSummary]
	f.get(t, "/api/v1/topics?region=2&sort=title", 200, &inRegion)
	if len(inRegion.Results) != 2 || inRegion.Results[0].Title != "Ducks on the Pond" {
		t.Errorf("region filter and title sort: %+v", inRegion.Results)
	}
	var found listResponse[TopicSummary]
	f.get(t, "/api/v1/topics?q=PROVENANCE", 200, &found)
	if len(found.Results) != 1 || found.Results[0].ID != 10 {
		t.Errorf("q matches title, summary and keywords, ignoring case: %+v", found.Results)
	}

	var d TopicDetail
	f.get(t, "/api/v1/topics/10", 200, &d)
	if d.Title != "Provenance and Receipts" || d.Posts != 4 || d.Region == nil || d.Region.ID != 1 {
		t.Errorf("detail: %+v", d.TopicSummary)
	}
	if len(d.PostsByDay) == 0 || d.PostsByDay[0].Posts == 0 {
		t.Errorf("posts by day: %+v", d.PostsByDay)
	}
	if len(d.TopAuthors) != 3 || d.TopAuthors[0].Posts != 2 {
		t.Errorf("top authors: %+v", d.TopAuthors)
	}
	if len(d.Conversations) != 1 || d.Conversations[0].Root != p1 || d.Conversations[0].Posts != 4 {
		t.Errorf("conversations: %+v", d.Conversations)
	}
	if len(d.RepresentativePosts) != 2 || d.RepresentativePosts[0].URI != p1 || d.RepresentativePosts[0].Topic == nil {
		t.Errorf("representative posts carry full text and topic: %+v", d.RepresentativePosts)
	}
	if len(d.NearbyTopics) != 2 || d.NearbyTopics[0].Distance > d.NearbyTopics[1].Distance {
		t.Errorf("nearby topics, closest first: %+v", d.NearbyTopics)
	}

	var rg RegionDetail
	f.get(t, "/api/v1/regions/2", 200, &rg)
	if len(rg.Topics) != 2 || rg.Accounts != 2 {
		t.Errorf("region detail: topics=%d accounts=%d", len(rg.Topics), rg.Accounts)
	}
	var rs regionsResponse
	f.get(t, "/api/v1/regions", 200, &rs)
	if len(rs.Results) != 2 {
		t.Errorf("regions: %+v", rs)
	}

	f.wantError(t, "/api/v1/topics/999", 404, "not_found", "")
	f.wantError(t, "/api/v1/topics/abc", 400, "invalid_parameter", "id")
	f.wantError(t, "/api/v1/regions/999", 404, "not_found", "")
	f.wantError(t, "/api/v1/topics?region=999", 404, "not_found", "")
	f.wantError(t, "/api/v1/topics?sort=sideways", 400, "invalid_parameter", "sort")
}

// ---- posts

func TestPostFiltersAndKeywordSearch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	list := func(q string) listResponse[PostView] {
		t.Helper()
		var l listResponse[PostView]
		f.get(t, "/api/v1/posts?"+q, 200, &l)
		return l
	}

	// keyword search: stemmed, all words required, newest unmapped post included
	if got := uris(list("q=provenance&sort=oldest").Results); !same(got, []string{p1, p2, p7}) {
		t.Errorf("q=provenance: %v", got)
	}
	if got := uris(list("q=receipt+provenance&sort=oldest").Results); !same(got, []string{p2}) {
		t.Errorf("all words must match: %v", got)
	}
	if got := uris(list(`q=%22receipts+all%22`).Results); !same(got, []string{p3}) {
		t.Errorf("a quoted phrase matches as a phrase: %v", got)
	}
	if got := uris(list("q=prov*&sort=oldest").Results); !same(got, []string{p1, p2, p7}) {
		t.Errorf("trailing * is a prefix: %v", got)
	}
	r := list("q=receipts")
	if len(r.Results) != 2 || r.Results[0].Score == nil || *r.Results[0].Score <= 0 {
		t.Errorf("keyword results carry a positive score, best first: %+v", r.Results)
	}

	// filters
	if got := uris(list("topic=10&sort=oldest").Results); !same(got, []string{p1, p2, p3, p4}) {
		t.Errorf("topic filter: %v", got)
	}
	if got := uris(list("region=2&sort=oldest").Results); !same(got, []string{p5, p6}) {
		t.Errorf("region filter: %v", got)
	}
	if got := uris(list("topic=10&region=2").Results); len(got) != 0 {
		t.Errorf("a topic outside the region matches nothing: %v", got)
	}
	if got := uris(list("author=alice.example&sort=oldest").Results); !same(got, []string{p1, p4, p7}) {
		t.Errorf("author by handle: %v", got)
	}
	if got := uris(list("author=%40bob.example&sort=oldest").Results); !same(got, []string{p2, p5}) {
		t.Errorf("author with a leading @: %v", got)
	}
	if got := uris(list("author=did:plc:carol&sort=oldest").Results); !same(got, []string{p3, p6}) {
		t.Errorf("author by DID: %v", got)
	}
	if got := uris(list("reply=true&sort=oldest").Results); !same(got, []string{p2, p3, p4}) {
		t.Errorf("replies only: %v", got)
	}
	if got := uris(list("reply=false&sort=oldest").Results); !same(got, []string{p1, p5, p6, p7}) {
		t.Errorf("top-level only: %v", got)
	}
	if got := uris(list("since=12h&sort=oldest").Results); !same(got, []string{p6, p7}) {
		t.Errorf("relative since: %v", got)
	}
	since := now.Add(-21 * time.Hour).Format(time.RFC3339)
	until := now.Add(-9 * time.Hour).Format(time.RFC3339)
	if got := uris(list("since=" + since + "&until=" + until + "&sort=oldest").Results); !same(got, []string{p5, p6}) {
		t.Errorf("RFC 3339 since/until: %v", got)
	}

	// ordering
	if got := list("sort=engaged").Results[0].URI; got != p1 {
		t.Errorf("most engaged first (2 likes + a repost): %v", got)
	}
	if got := uris(list("").Results); got[0] != p7 || got[len(got)-1] != p1 {
		t.Errorf("default is newest first: %v", got)
	}

	// the post shape
	one := list("topic=10&sort=oldest").Results[1]
	if one.URI != p2 || one.Author.Handle != "bob.example" || one.ReplyTo == nil || *one.ReplyTo != p1 || one.ThreadRoot == nil || *one.ThreadRoot != p1 {
		t.Errorf("post shape: %+v", one)
	}
	if one.Topic == nil || one.Topic.Title != "Provenance and Receipts" || one.Region == nil || one.Thread == nil || one.Thread.Posts != 4 {
		t.Errorf("topic, region and thread should come from the map: %+v", one)
	}
	if one.URL != "https://delve.town/profile/did:plc:bob/post/p2" || one.CreatedAt != "2026-10-07T07:00:00Z" {
		t.Errorf("url/time: %q %q", one.URL, one.CreatedAt)
	}
	first := list("topic=10&sort=oldest").Results[0]
	if first.Counts.Likes != 2 || first.Counts.Reposts != 1 || first.Counts.Replies != 1 {
		t.Errorf("counts: %+v", first.Counts)
	}

	// a post newer than the map has no topic, and says so explicitly with null
	w := f.do("/api/v1/posts?q=newer")
	if !strings.Contains(w.Body.String(), `"topic":null`) || !strings.Contains(w.Body.String(), `"region":null`) {
		t.Errorf("unmapped posts must show topic and region as null: %s", w.Body.String())
	}
}

func TestPaginationWalksEveryPostOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		path := "/api/v1/posts?sort=oldest&limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var l listResponse[PostView]
		f.get(t, path, 200, &l)
		if l.Total == nil || *l.Total != 7 {
			t.Fatalf("total: %v", l.Total)
		}
		seen = append(seen, uris(l.Results)...)
		if l.NextCursor == nil {
			if page != 2 {
				t.Errorf("7 posts at 3 per page should end on the third page, ended on %d", page+1)
			}
			break
		}
		cursor = *l.NextCursor
	}
	if !same(seen, []string{p1, p2, p3, p4, p5, p6, p7}) {
		t.Errorf("paging should see every post exactly once, in order: %v", seen)
	}
	f.wantError(t, "/api/v1/posts?cursor=garbage", 400, "invalid_parameter", "cursor")
	f.wantError(t, "/api/v1/posts?limit=0", 400, "invalid_parameter", "limit")
	f.wantError(t, "/api/v1/posts?limit=100000", 400, "invalid_parameter", "limit")
}

func TestPostFilterErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.wantError(t, "/api/v1/posts?topic=999", 404, "not_found", "")
	f.wantError(t, "/api/v1/posts?region=999", 404, "not_found", "")
	f.wantError(t, "/api/v1/posts?author=nobody.example", 404, "not_found", "")
	f.wantError(t, "/api/v1/posts?sort=relevance", 400, "invalid_parameter", "sort")
	f.wantError(t, "/api/v1/posts?q=%2A%2A%2A", 400, "invalid_parameter", "q")
	f.wantError(t, "/api/v1/posts?since=yesterday-ish", 400, "invalid_parameter", "since")
	f.wantError(t, "/api/v1/posts?reply=maybe", 400, "invalid_parameter", "reply")
	f.wantError(t, "/api/v1/posts?topic=x", 400, "invalid_parameter", "topic")
}

func TestKeywordQueriesCannotInjectSearchSyntax(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// FTS5 operators and column filters typed by a client must be treated as plain words
	for _, q := range []string{`provenance OR ducks`, `text:provenance`, `provenance NOT receipts`, `"unbalanced`, `(provenance`, `NEAR(provenance receipts)`, `prov* AND ^x`} {
		var l listResponse[PostView]
		f.get(t, "/api/v1/posts?q="+urlEscape(q), 200, &l) // must not be a 500
		_ = l
	}
	var l listResponse[PostView]
	f.get(t, "/api/v1/posts?q="+urlEscape("provenance OR ducks"), 200, &l)
	if len(l.Results) != 0 {
		t.Errorf("OR is just another word to match, so nothing contains all three: %v", uris(l.Results))
	}
}

func urlEscape(s string) string {
	r := strings.NewReplacer(" ", "+", `"`, "%22", "(", "%28", ")", "%29", "^", "%5E", ":", "%3A", "*", "%2A")
	return r.Replace(s)
}

func TestFTSQuerySanitising(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"provenance":         `"provenance"`,
		"Agents  Records":    `"Agents" "Records"`,
		`"append only" logs`: `"append only" "logs"`,
		"prov*":              `"prov"*`,
		"a-b c_d":            `"a" "b" "c_d"`,
		"x:y OR z":           `"x" "y" "OR" "z"`,
		"café ünï":           `"café" "ünï"`,
	} {
		got, ok := ftsQuery(in)
		if !ok || got != want {
			t.Errorf("ftsQuery(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "***", `""`, "-+-"} {
		if _, ok := ftsQuery(in); ok {
			t.Errorf("ftsQuery(%q) should find nothing searchable", in)
		}
	}
}

func TestTimeParameters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.api.params(httpRequest("/x?a=3h&b=2026-10-08T00:00:00Z&c=1791417600&d=45m&e=nonsense&f=2d"))
	if got, want := p.timeMS("a"), now.Add(-3*time.Hour).UnixMilli(); got != want {
		t.Errorf("3h: %d want %d", got, want)
	}
	if got := p.timeMS("b"); got != time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("rfc3339: %d", got)
	}
	if got := p.timeMS("c"); got != 1791417600*1000 {
		t.Errorf("unix seconds: %d", got)
	}
	if got, want := p.timeMS("d"), now.Add(-45*time.Minute).UnixMilli(); got != want {
		t.Errorf("45m: %d want %d", got, want)
	}
	if got, want := p.timeMS("f"), now.Add(-48*time.Hour).UnixMilli(); got != want {
		t.Errorf("2d: %d want %d", got, want)
	}
	if p.Err() != nil {
		t.Errorf("no error expected yet: %v", p.Err())
	}
	p.timeMS("e")
	var ae *apiError
	if !errors.As(p.Err(), &ae) || ae.Param != "e" {
		t.Errorf("a nonsense time should fail on its parameter: %v", p.Err())
	}
	if got := p.timeMS("absent"); got != 0 {
		t.Errorf("absent = %d", got)
	}
}

func httpRequest(target string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, target, nil)
	return r
}

// ---- single posts and conversations

func TestPostDetailShowsParentChildrenAndSimilar(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var d PostDetail
	f.get(t, "/api/v1/post?uri="+urlQuery(p2)+"&include=parent,children,similar", 200, &d)
	if d.Post.URI != p2 || d.Parent == nil || d.Parent.URI != p1 {
		t.Errorf("post and parent: %+v / %+v", d.Post.URI, d.Parent)
	}
	if len(d.Children) != 1 || d.Children[0].URI != p3 || d.ChildrenTotal != 1 {
		t.Errorf("children: %v total=%d", uris(d.Children), d.ChildrenTotal)
	}
	if len(d.Similar) == 0 || d.Similar[0].URI == p2 || d.Similar[0].Score == nil {
		t.Fatalf("similar should exclude the post itself and carry scores: %+v", d.Similar)
	}
	// p3, p1 and p7 each share a word with "Provenance needs receipts"; the duck and number
	// posts share none, so they must rank below all of them.
	for i, s := range d.Similar[:3] {
		if s.URI == p5 || s.URI == p6 {
			t.Errorf("similar #%d is unrelated: %s", i+1, s.URI)
		}
	}
	if *d.Similar[0].Score < *d.Similar[len(d.Similar)-1].Score {
		t.Errorf("similar posts should be ordered by score, best first")
	}

	// defaults: parent and children but not similar
	var dflt PostDetail
	f.get(t, "/api/v1/post?uri="+urlQuery(p1), 200, &dflt)
	if dflt.Parent != nil || len(dflt.Children) != 1 || len(dflt.Similar) != 0 {
		t.Errorf("defaults: parent=%v children=%d similar=%d", dflt.Parent, len(dflt.Children), len(dflt.Similar))
	}
	// only what was asked for
	var only PostDetail
	f.get(t, "/api/v1/post?uri="+urlQuery(p2)+"&include=similar", 200, &only)
	if only.Parent != nil || len(only.Children) != 0 || len(only.Similar) == 0 {
		t.Errorf("include=similar only: %+v", only)
	}

	f.wantError(t, "/api/v1/post", 400, "invalid_parameter", "uri")
	f.wantError(t, "/api/v1/post?uri="+urlQuery("at://did:plc:nobody/town.delve.feed.post/x"), 404, "not_found", "")
	f.wantError(t, "/api/v1/post?uri="+urlQuery(p1)+"&include=everything", 400, "invalid_parameter", "include")
}

func urlQuery(s string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(s)
}

func TestThreadReadsAConversationInOrderWithDepth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var th ThreadResponse
	f.get(t, "/api/v1/thread?root="+urlQuery(p1), 200, &th)
	if th.Posts != 4 || !th.RootStored || th.Truncated || th.Root != p1 {
		t.Errorf("thread header: %+v", th)
	}
	var order []string
	var depths []int
	for _, p := range th.Results {
		order, depths = append(order, p.URI), append(depths, p.Depth)
	}
	if !same(order, []string{p1, p2, p3, p4}) {
		t.Errorf("oldest first: %v", order)
	}
	if depths[0] != 0 || depths[1] != 1 || depths[2] != 2 || depths[3] != 3 {
		t.Errorf("depth follows the reply chain: %v", depths)
	}
	if len(th.Participants) != 3 || th.Participants[0].Posts != 2 || th.Participants[0].DID != didAlice {
		t.Errorf("participants, most active first: %+v", th.Participants)
	}
	if th.Topic == nil || th.Topic.ID != 10 {
		t.Errorf("topic: %+v", th.Topic)
	}

	// any post leads to its conversation
	var viaPost ThreadResponse
	f.get(t, "/api/v1/thread?uri="+urlQuery(p3), 200, &viaPost)
	if viaPost.Root != p1 || len(viaPost.Results) != 4 {
		t.Errorf("uri= resolves to the root: %s (%d posts)", viaPost.Root, len(viaPost.Results))
	}

	// paging inside a conversation
	var page1, page2 ThreadResponse
	f.get(t, "/api/v1/thread?root="+urlQuery(p1)+"&limit=3", 200, &page1)
	if len(page1.Results) != 3 || page1.NextCursor == nil {
		t.Fatalf("first page: %d results, cursor %v", len(page1.Results), page1.NextCursor)
	}
	f.get(t, "/api/v1/thread?root="+urlQuery(p1)+"&limit=3&cursor="+*page1.NextCursor, 200, &page2)
	if len(page2.Results) != 1 || page2.Results[0].URI != p4 || page2.Results[0].Depth != 3 || page2.NextCursor != nil {
		t.Errorf("second page keeps depth and ends: %+v", page2.Results)
	}

	f.wantError(t, "/api/v1/thread", 400, "invalid_parameter", "root")
	f.wantError(t, "/api/v1/thread?root="+urlQuery("at://did:plc:x/town.delve.feed.post/none"), 404, "not_found", "")
}

func TestThreadsListsConversations(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var l listResponse[ThreadSummary]
	f.get(t, "/api/v1/threads", 200, &l)
	if len(l.Results) != 1 || l.Results[0].Root != p1 || l.Results[0].Posts != 4 || l.Results[0].Participants != 3 || l.Results[0].Topic == nil {
		t.Errorf("threads: %+v", l.Results)
	}
	f.get(t, "/api/v1/threads?min_posts=10", 200, &l)
	if len(l.Results) != 0 || l.NextCursor != nil {
		t.Errorf("none big enough: %+v", l)
	}
	f.get(t, "/api/v1/threads?topic=11", 200, &l)
	if len(l.Results) != 0 {
		t.Errorf("topic filter: %+v", l.Results)
	}
	f.wantError(t, "/api/v1/threads?topic=999", 404, "not_found", "")
}

// ---- accounts and relationships

func TestAuthorsListAndDetail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var l listResponse[AuthorSummary]
	f.get(t, "/api/v1/authors?sort=posts", 200, &l)
	if len(l.Results) != 3 || l.Results[0].DID != didAlice || l.Results[0].Posts != 3 || l.Results[0].LikesReceived != 2 {
		t.Errorf("authors: %+v", l.Results)
	}
	f.get(t, "/api/v1/authors?sort=likes_received", 200, &l)
	if l.Results[0].DID != didAlice {
		t.Errorf("by likes received: %+v", l.Results)
	}
	f.get(t, "/api/v1/authors?q=ducks", 200, &l)
	if len(l.Results) != 1 || l.Results[0].DID != didBob {
		t.Errorf("q matches bios: %+v", l.Results)
	}
	f.get(t, "/api/v1/authors?q=%25", 200, &l)
	if len(l.Results) != 0 {
		t.Errorf("a literal %% must not match everything: %d", len(l.Results))
	}
	f.get(t, "/api/v1/authors?since=12h", 200, &l)
	if len(l.Results) != 2 {
		t.Errorf("since: %+v", l.Results)
	}

	var d AuthorDetail
	f.get(t, "/api/v1/authors/%40alice.example", 200, &d)
	if d.DID != didAlice || d.Handle != "alice.example" || d.Bio != "Studies provenance." || d.Posts != 3 || d.Replies != 1 {
		t.Errorf("detail: %+v", d.AuthorSummary)
	}
	if d.Followers != 2 || d.Following != 1 {
		t.Errorf("followers/following: %d/%d", d.Followers, d.Following)
	}
	if len(d.TopTopics) == 0 || d.TopTopics[0].ID != 10 || d.TopTopics[0].Posts != 2 {
		t.Errorf("top topics come from the map: %+v", d.TopTopics)
	}
	if len(d.RecentPosts) != 3 || d.RecentPosts[0].URI != p7 || d.TopPosts[0].URI != p1 {
		t.Errorf("recent/top: %v / %v", uris(d.RecentPosts), uris(d.TopPosts))
	}
	var byDID AuthorDetail
	f.get(t, "/api/v1/authors/"+didCarol, 200, &byDID)
	if byDID.Handle != "" || byDID.Name != "Carol" {
		t.Errorf("an account without a handle is still found by DID: %+v", byDID.AuthorSummary)
	}
	f.wantError(t, "/api/v1/authors/nobody.example", 404, "not_found", "")
	f.wantError(t, "/api/v1/authors/did:plc:ghost", 404, "not_found", "")
}

func TestNetworkShowsWhoTalksToWhom(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var n Network
	f.get(t, "/api/v1/authors/bob.example/network", 200, &n)
	if n.Account.DID != didBob || n.FollowsTotal != 1 || len(n.Follows) != 1 || n.Follows[0].DID != didAlice {
		t.Errorf("follows: %+v", n)
	}
	if n.FollowedByTotal != 1 || n.FollowedBy[0].DID != didAlice {
		t.Errorf("followed by: %+v", n.FollowedBy)
	}
	// bob replied to alice once (p2 -> p1); carol replied to bob once (p3 -> p2)
	if len(n.RepliesTo) != 1 || n.RepliesTo[0].DID != didAlice || n.RepliesTo[0].Count != 1 || n.RepliesTo[0].Handle != "alice.example" {
		t.Errorf("replies to: %+v", n.RepliesTo)
	}
	if len(n.RepliedBy) != 1 || n.RepliedBy[0].DID != didCarol {
		t.Errorf("replied by: %+v", n.RepliedBy)
	}
	// bob liked p1 (alice) and p3 (carol); alice liked p5 (bob)
	if len(n.LikesTo) != 2 {
		t.Errorf("likes to: %+v", n.LikesTo)
	}
	if len(n.LikedBy) != 1 || n.LikedBy[0].DID != didAlice {
		t.Errorf("liked by: %+v", n.LikedBy)
	}
}

func TestReplyGraphSumsRepliesBetweenAccounts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var g listResponse[ReplyEdge]
	f.get(t, "/api/v1/graph/replies?min_count=1", 200, &g)
	got := map[string]int{}
	for _, e := range g.Results {
		got[e.From.DID+">"+e.To.DID] = e.Replies
	}
	want := map[string]int{didBob + ">" + didAlice: 1, didCarol + ">" + didBob: 1, didAlice + ">" + didCarol: 1}
	if len(got) != 3 {
		t.Fatalf("edges: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("edge %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if g.Results[0].From.Handle == "" && g.Results[0].From.DID == didBob {
		t.Errorf("edges carry handles")
	}
	f.get(t, "/api/v1/graph/replies", 200, &g) // default min_count 3
	if len(g.Results) != 0 {
		t.Errorf("default threshold hides rare pairs: %+v", g.Results)
	}
	f.get(t, "/api/v1/graph/replies?min_count=1&until=28.5h", 200, &g)
	if g.Total == nil {
		t.Errorf("total missing")
	}
}

// ---- semantic search

func TestSemanticSearchRanksByMeaning(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var l listResponse[PostView]
	f.get(t, "/api/v1/search?q="+urlEscape("receipts for provenance")+"&limit=3", 200, &l)
	if len(l.Results) != 3 || l.Results[0].URI != p2 {
		t.Fatalf("the post saying exactly this should be first: %v", uris(l.Results))
	}
	if l.Results[0].Score == nil || *l.Results[0].Score < 0.99 {
		t.Errorf("an identical bag of words scores about 1: %v", l.Results[0].Score)
	}
	if *l.Results[0].Score < *l.Results[1].Score || *l.Results[1].Score < *l.Results[2].Score {
		t.Errorf("scores should fall: %v %v %v", *l.Results[0].Score, *l.Results[1].Score, *l.Results[2].Score)
	}

	// filters narrow the candidates
	f.get(t, "/api/v1/search?q=ducks+pond&topic=10", 200, &l)
	for _, r := range l.Results {
		if r.Topic == nil || r.Topic.ID != 10 {
			t.Errorf("topic filter violated: %+v", r.URI)
		}
	}
	f.get(t, "/api/v1/search?q=provenance&reply=false&author=alice.example", 200, &l)
	for _, r := range l.Results {
		if r.ReplyTo != nil || r.Author.DID != didAlice {
			t.Errorf("reply/author filter violated: %+v", r.URI)
		}
	}
	// a post newer than the map has no topic, so a topic filter excludes it but no filter keeps it
	f.get(t, "/api/v1/search?q=provenance&limit=100", 200, &l)
	if !containsURI(l.Results, p7) {
		t.Errorf("without a topic filter the newest post is searchable: %v", uris(l.Results))
	}
	f.get(t, "/api/v1/search?q=provenance&region=1&limit=100", 200, &l)
	if containsURI(l.Results, p7) {
		t.Errorf("an unmapped post can't satisfy a region filter")
	}
	f.get(t, "/api/v1/search?q=ducks&since=12h&limit=100", 200, &l)
	for _, r := range l.Results {
		if r.URI != p6 && r.URI != p7 {
			t.Errorf("since filter violated: %s", r.URI)
		}
	}

	f.wantError(t, "/api/v1/search", 400, "invalid_parameter", "q")
	f.wantError(t, "/api/v1/search?q=x&topic=999", 404, "not_found", "")
	f.wantError(t, "/api/v1/search?q="+strings.Repeat("a", 1001), 400, "invalid_parameter", "q")
}

func containsURI(vs []PostView, u string) bool {
	for _, v := range vs {
		if v.URI == u {
			return true
		}
	}
	return false
}

func TestSemanticSearchDegradesGracefully(t *testing.T) {
	t.Parallel()
	off := newFixture(t, func(c *Config) { c.Embedder = nil })
	off.wantError(t, "/api/v1/search?q=anything", 503, "semantic_unavailable", "")
	down := newFixture(t, func(c *Config) { c.Embedder = toyEmbedder{err: errors.New("connection refused")} })
	down.wantError(t, "/api/v1/search?q=anything", 503, "semantic_unavailable", "")
	// the rest of the API is unaffected
	var l listResponse[PostView]
	down.get(t, "/api/v1/posts?q=provenance", 200, &l)
	if len(l.Results) == 0 {
		t.Errorf("keyword search should still work")
	}
	// "similar" needs no model call, so it works even with the embedder down
	var d PostDetail
	down.get(t, "/api/v1/post?uri="+urlQuery(p2)+"&include=similar", 200, &d)
	if len(d.Similar) == 0 {
		t.Errorf("similar posts come from stored vectors and should not need the embedder")
	}
}

// ---- activity hook

func TestActivityIsExposedWhenProvided(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var a map[string]any
	f.get(t, "/api/v1/activity?days=2", 200, &a)
	if a["days"] != float64(2) {
		t.Errorf("days should reach the activity function: %v", a)
	}
	f.wantError(t, "/api/v1/activity?days=99", 400, "invalid_parameter", "days")
	none := newFixture(t, func(c *Config) { c.Activity = nil })
	if w := none.do("/api/v1/activity"); w.Code == 200 {
		t.Errorf("no activity function: the route should not exist")
	}
}

// ---- the API's manners

func TestResponsesAreCORSOpenCacheableAndTidyJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.get(t, "/api/v1/overview", 200, nil)
	h := w.Header()
	if h.Get("Access-Control-Allow-Origin") != "*" || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || !strings.Contains(h.Get("Cache-Control"), "max-age=60") {
		t.Errorf("headers: %v", h)
	}
	if strings.Contains(w.Body.String(), "\n  ") {
		t.Errorf("compact by default")
	}
	if p := f.do("/api/v1/overview?pretty=1"); !strings.Contains(p.Body.String(), "\n  \"snapshot\"") {
		t.Errorf("pretty=1 should indent: %.80s", p.Body.String())
	}
	// errors are never cached
	e := f.do("/api/v1/topics/999")
	if e.Header().Get("Cache-Control") != "no-store" || e.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("error headers: %v", e.Header())
	}
	// a browser's CORS preflight
	r, _ := http.NewRequest(http.MethodOptions, "/api/v1/posts", nil)
	pw := newRecorder()
	f.h.ServeHTTP(pw, r)
	if pw.Code != http.StatusNoContent || pw.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("preflight: %d %v", pw.Code, pw.Header())
	}
}

func TestRateLimitingIsPerClientAndSearchIsStricter(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(c *Config) { c.RPS, c.Burst, c.SearchRPS, c.SearchBurst = 1, 3, 1, 1 })
	statuses := func(path, ip string, n int) []int {
		var out []int
		for i := 0; i < n; i++ {
			out = append(out, f.do(path, "CF-Connecting-IP", ip).Code)
		}
		return out
	}
	got := statuses("/api/v1/overview", "1.1.1.1", 5)
	if got[0] != 200 || got[2] != 200 || got[4] != 429 {
		t.Errorf("a burst of 3 then limited: %v", got)
	}
	if other := statuses("/api/v1/overview", "2.2.2.2", 1); other[0] != 200 {
		t.Errorf("another client has its own budget: %v", other)
	}
	w := f.do("/api/v1/overview", "CF-Connecting-IP", "1.1.1.1")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"rate_limited"`) {
		t.Errorf("429 should explain itself: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	s := statuses("/api/v1/search?q=ducks", "3.3.3.3", 3)
	if s[0] != 200 || s[1] != 429 {
		t.Errorf("search has a tighter budget: %v", s)
	}
	if g := statuses("/api/v1/overview", "3.3.3.3", 1); g[0] != 200 {
		t.Errorf("search and general budgets are separate: %v", g)
	}
}

func TestNoMapYet(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(c *Config) { c.AtlasDir = t.TempDir() })
	f.wantError(t, "/api/v1/overview", 503, "snapshot_unavailable", "")
}

func TestANewerMapIsPickedUp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var ov Overview
	f.get(t, "/api/v1/overview", 200, &ov)
	if ov.Snapshot != snapID {
		t.Fatal(ov.Snapshot)
	}
	// build a second snapshot next to the first and repoint latest
	const next = "20261008T150000Z"
	src := f.api.cfg.AtlasDir
	for _, name := range []string{"atlas.json", "cols.json"} {
		b := mustRead(t, src+"/"+snapID+"/"+name)
		if name == "atlas.json" {
			b = []byte(strings.Replace(string(b), snapID, next, 1))
		}
		mustWrite(t, src+"/"+next+"/"+name, b)
	}
	mustRelink(t, src+"/latest", next)
	f.api.snaps.checked = time.Time{} // the directory is only rechecked every few seconds
	f.get(t, "/api/v1/overview", 200, &ov)
	if ov.Snapshot != next {
		t.Errorf("should serve the new map: %s", ov.Snapshot)
	}
}
