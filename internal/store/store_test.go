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
