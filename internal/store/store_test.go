package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestWriteSavesCursorWithRows(t *testing.T) {
	t.Parallel()
	db, ctx := open(t), context.Background()
	var b Batch
	b.Posts = append(b.Posts, Post{URI: "at://a/p/1", DID: "did:a", Rkey: "1", Text: "hi", CreatedAt: 1000, IndexedAt: 1000})
	if err := db.Write(ctx, &b, 42); err != nil {
		t.Fatal(err)
	}
	seq, ok, err := db.Cursor(ctx)
	if err != nil || !ok || seq != 42 {
		t.Fatalf("cursor = %d, %v, %v", seq, ok, err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM posts`).Scan(&n)
	if n != 1 {
		t.Fatalf("posts = %d", n)
	}
}

func TestActorUpdatesKeepKnownValues(t *testing.T) {
	t.Parallel()
	db, ctx := open(t), context.Background()
	var b Batch
	b.Actors = append(b.Actors,
		ActorUpdate{DID: "did:a", Handle: "a.example", DisplayName: "Ann", SeenAt: 1},
		// a later update that only knows a bio must not erase the name or handle
		ActorUpdate{DID: "did:a", Description: "bio", SeenAt: 2},
	)
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	var h, n, d string
	db.QueryRow(`SELECT handle, display_name, description FROM actors WHERE did='did:a'`).Scan(&h, &n, &d)
	if h != "a.example" || n != "Ann" || d != "bio" {
		t.Fatalf("got %q %q %q", h, n, d)
	}
}

func TestDeletingAPostRemovesItsEmbedding(t *testing.T) {
	t.Parallel()
	db, ctx := open(t), context.Background()
	var b Batch
	b.Posts = append(b.Posts, Post{URI: "at://a/p/1", DID: "did:a", Rkey: "1", Text: "hi", CreatedAt: 1000, IndexedAt: 1000})
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.PutEmbeddings(ctx, "m", 2, []string{"at://a/p/1"}, [][]byte{{0, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	b.DeletePosts = []string{"at://a/p/1"}
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT (SELECT count(*) FROM posts) + (SELECT count(*) FROM embeddings)`).Scan(&n)
	if n != 0 {
		t.Fatalf("expected the post and its embedding gone, %d rows left", n)
	}
}

func TestPendingEmbeddingsAddThreadContextToReplies(t *testing.T) {
	t.Parallel()
	db, ctx := open(t), context.Background()
	var b Batch
	b.Posts = append(b.Posts,
		Post{URI: "at://a/p/root", DID: "did:a", Rkey: "root", Text: "Should agents have wallets?", CreatedAt: 1000, IndexedAt: 1000},
		Post{URI: "at://a/p/r1", DID: "did:b", Rkey: "r1", Text: "o7", CreatedAt: 2000, IndexedAt: 2000,
			ReplyRoot: "at://a/p/root", ReplyParent: "at://a/p/root"},
	)
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	got, err := db.PendingEmbeddings(ctx, "m", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{}
	for _, p := range got {
		docs[p.URI] = p.Doc
	}
	if docs["at://a/p/root"] != "Should agents have wallets?" {
		t.Errorf("a root post is embedded as itself, got %q", docs["at://a/p/root"])
	}
	if !strings.Contains(docs["at://a/p/r1"], "Should agents have wallets?") || !strings.HasSuffix(docs["at://a/p/r1"], "o7") {
		t.Errorf("a reply should carry its thread's opening, got %q", docs["at://a/p/r1"])
	}

	// once embedded under this model key they are no longer pending, but another key re-queues them
	if err := db.PutEmbeddings(ctx, "m", 1, []string{"at://a/p/root", "at://a/p/r1"}, [][]byte{{0, 0, 0, 0}, {0, 0, 0, 0}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PendingEmbeddings(ctx, "m", 0, 10); len(got) != 0 {
		t.Errorf("nothing should be pending, got %d", len(got))
	}
	if got, _ := db.PendingEmbeddings(ctx, "m2", 0, 10); len(got) != 2 {
		t.Errorf("a new model key re-queues everything, got %d", len(got))
	}
}

func ftsHits(t *testing.T, db *DB, query string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT p.uri FROM posts p JOIN posts_fts ON posts_fts.rowid = p.rowid WHERE posts_fts MATCH ? ORDER BY p.uri`, query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		rows.Scan(&u)
		out = append(out, u)
	}
	return out
}

func TestFullTextIndexTracksInsertsUpdatesAndDeletes(t *testing.T) {
	t.Parallel()
	db, ctx := open(t), context.Background()
	var b Batch
	b.Posts = append(b.Posts,
		Post{URI: "at://a/p/1", DID: "did:a", Rkey: "1", Text: "Agents debating append-only provenance", CreatedAt: 1, IndexedAt: 1},
		Post{URI: "at://a/p/2", DID: "did:a", Rkey: "2", Text: "ducks", EmbedText: "a ledger of receipts", Tags: "audit", CreatedAt: 2, IndexedAt: 2},
	)
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	// stemming ("agent" finds "Agents"), and the embed text and tags are searchable too
	if got := ftsHits(t, db, `"agent"`); len(got) != 1 || got[0] != "at://a/p/1" {
		t.Errorf("stemmed match: %v", got)
	}
	if got := ftsHits(t, db, `"receipt"`); len(got) != 1 || got[0] != "at://a/p/2" {
		t.Errorf("embed text match: %v", got)
	}
	if got := ftsHits(t, db, `"audit"`); len(got) != 1 {
		t.Errorf("tag match: %v", got)
	}

	// an upsert that changes the text must replace the indexed words, not add to them
	b.Reset()
	b.Posts = append(b.Posts, Post{URI: "at://a/p/1", DID: "did:a", Rkey: "1", Text: "now about marmots", CreatedAt: 1, IndexedAt: 1})
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	if got := ftsHits(t, db, `"provenance"`); len(got) != 0 {
		t.Errorf("old words should be gone after an update: %v", got)
	}
	if got := ftsHits(t, db, `"marmot"`); len(got) != 1 {
		t.Errorf("new words should be searchable: %v", got)
	}

	b.Reset()
	b.DeletePosts = []string{"at://a/p/1"}
	if err := db.Write(ctx, &b, 0); err != nil {
		t.Fatal(err)
	}
	if got := ftsHits(t, db, `"marmot"`); len(got) != 0 {
		t.Errorf("a deleted post must leave the index: %v", got)
	}
}

func TestOpenBackfillsTheFullTextIndexForExistingPosts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a database from before the index existed: rows present, triggers/flag absent.
	for _, q := range []string{
		`DROP TRIGGER posts_fts_ai`, `DROP TRIGGER posts_fts_ad`, `DROP TRIGGER posts_fts_au`,
		`INSERT INTO posts(uri,did,rkey,text,created_at,indexed_at) VALUES ('at://a/p/9','did:a','9','legacy hedgehog post',1,1)`,
		`DELETE FROM meta WHERE key='fts_built'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if got := ftsHits(t, db, `"hedgehog"`); len(got) != 1 {
		t.Errorf("existing posts should be indexed on open: %v", got)
	}
}

func TestReactionCountsUseTheCompositeIndex(t *testing.T) {
	t.Parallel()
	db := open(t)
	// The per-post count the API and the map build both run. If SQLite can't find a (subject, kind)
	// index it scans every like for every post: seconds for a long conversation.
	for _, q := range []string{
		`SELECT (SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind = 'like') FROM posts p`,
		`SELECT (SELECT count(*) FROM interactions i WHERE i.subject = p.uri AND i.kind = 'repost') FROM posts p`,
	} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			rows.Scan(&id, &parent, &unused, &detail)
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), "interactions_subject_kind") || strings.Contains(plan.String(), "interactions_kind") {
			t.Errorf("reaction count should use interactions_subject_kind, plan was:\n%s", plan.String())
		}
	}
}

func TestOpenMigratesOlderActorsTable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
	// Create the table as it existed before resolved_at was added.
	old, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`DROP TABLE actors; CREATE TABLE actors (did TEXT PRIMARY KEY, handle TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', first_seen INTEGER NOT NULL,
		active INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if err := db.MarkResolved(context.Background(), "did:x", "x.example", "", ""); err != nil {
		t.Fatalf("resolved_at should exist after migration: %v", err)
	}
}
