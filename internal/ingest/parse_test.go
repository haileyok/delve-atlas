package ingest

import (
	"testing"
	"time"

	"github.com/bluesky-social/jetstream"

	"github.com/haileyok/delve-atlas/internal/store"
)

func commit(op jetstream.Operation, coll, rkey string, rec map[string]any) *jetstream.Event {
	return &jetstream.Event{
		DID:    "did:plc:alice",
		TimeUS: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).UnixMicro(),
		Kind:   jetstream.KindCommit,
		Commit: &jetstream.Commit{Operation: op, Collection: coll, Rkey: rkey, Record: rec},
	}
}

func TestHandlePostWithReplyAndLinkCard(t *testing.T) {
	t.Parallel()
	var b store.Batch
	Handle(commit(jetstream.OpCreate, collPost, "3abc", map[string]any{
		"text":      "hello world",
		"createdAt": "2026-10-05T11:59:00.000Z",
		"langs":     []any{"en", "fr"},
		"tags":      []any{"intro"},
		"reply": map[string]any{
			"root":   map[string]any{"uri": "at://did:plc:bob/town.delve.feed.post/root"},
			"parent": map[string]any{"uri": "at://did:plc:bob/town.delve.feed.post/par"},
		},
		"embed": map[string]any{
			"$type": "town.delve.embed.external",
			"external": map[string]any{
				"uri": "https://example.com/x", "title": "A title", "description": "A description",
			},
		},
	}), &b)

	if len(b.Posts) != 1 {
		t.Fatalf("want 1 post, got %d", len(b.Posts))
	}
	p := b.Posts[0]
	if p.URI != "at://did:plc:alice/town.delve.feed.post/3abc" || p.Text != "hello world" {
		t.Errorf("unexpected post %+v", p)
	}
	if p.ReplyRoot != "at://did:plc:bob/town.delve.feed.post/root" || p.ReplyParent != "at://did:plc:bob/town.delve.feed.post/par" {
		t.Errorf("reply refs wrong: %+v", p)
	}
	if p.Langs != "en,fr" || p.Tags != "intro" {
		t.Errorf("langs/tags: %q %q", p.Langs, p.Tags)
	}
	if p.EmbedKind != "external" || p.LinkURL != "https://example.com/x" {
		t.Errorf("embed: kind=%q link=%q", p.EmbedKind, p.LinkURL)
	}
	if p.EmbedText != "A description\nA title" && p.EmbedText != "A title\nA description" {
		t.Errorf("embed text: %q", p.EmbedText)
	}
	if want := time.Date(2026, 10, 5, 11, 59, 0, 0, time.UTC).UnixMilli(); p.CreatedAt != want {
		t.Errorf("createdAt %d, want %d", p.CreatedAt, want)
	}
}

func TestHandleQuoteWithImages(t *testing.T) {
	t.Parallel()
	var b store.Batch
	Handle(commit(jetstream.OpCreate, collPost, "3q", map[string]any{
		"text": "look", "createdAt": "2026-10-05T11:59:00Z",
		"embed": map[string]any{
			"$type":  "town.delve.embed.recordWithMedia",
			"record": map[string]any{"record": map[string]any{"uri": "at://did:plc:bob/town.delve.feed.post/q"}},
			"media": map[string]any{
				"$type":  "town.delve.embed.images",
				"images": []any{map[string]any{"alt": "a cat", "image": map[string]any{}}, map[string]any{"alt": "a dog", "image": map[string]any{}}},
			},
		},
	}), &b)
	p := b.Posts[0]
	if p.QuoteURI != "at://did:plc:bob/town.delve.feed.post/q" {
		t.Errorf("quote uri %q", p.QuoteURI)
	}
	if p.EmbedKind != "recordWithMedia+images" || p.NImages != 2 {
		t.Errorf("kind=%q images=%d", p.EmbedKind, p.NImages)
	}
}

func TestHandleFutureCreatedAtIsClamped(t *testing.T) {
	t.Parallel()
	var b store.Batch
	Handle(commit(jetstream.OpCreate, collPost, "3f", map[string]any{"text": "x", "createdAt": "2030-01-01T00:00:00Z"}), &b)
	p := b.Posts[0]
	if p.CreatedAt != p.IndexedAt {
		t.Errorf("future createdAt should fall back to the event time, got %d vs %d", p.CreatedAt, p.IndexedAt)
	}
}

func TestHandleInteractionsAndDeletes(t *testing.T) {
	t.Parallel()
	var b store.Batch
	Handle(commit(jetstream.OpCreate, collLike, "l1", map[string]any{
		"subject": map[string]any{"uri": "at://did:plc:bob/town.delve.feed.post/p"}, "createdAt": "2026-10-05T11:00:00Z",
	}), &b)
	Handle(commit(jetstream.OpCreate, collRepost, "r1", map[string]any{
		"subject": map[string]any{"uri": "at://did:plc:bob/town.delve.feed.post/p"}, "createdAt": "2026-10-05T11:00:00Z",
	}), &b)
	Handle(commit(jetstream.OpCreate, collFollow, "f1", map[string]any{"subject": "did:plc:bob", "createdAt": "2026-10-05T11:00:00Z"}), &b)
	Handle(commit(jetstream.OpDelete, collPost, "gone", nil), &b)
	Handle(commit(jetstream.OpDelete, collLike, "l0", nil), &b)

	kinds := map[string]string{}
	for _, i := range b.Interactions {
		kinds[i.Kind] = i.Subject
	}
	if kinds["like"] == "" || kinds["repost"] == "" || kinds["follow"] != "did:plc:bob" {
		t.Errorf("interactions: %+v", b.Interactions)
	}
	if len(b.DeletePosts) != 1 || len(b.DeleteInter) != 1 {
		t.Errorf("deletes: posts=%v inter=%v", b.DeletePosts, b.DeleteInter)
	}
}

func TestHandleProfileAndUnknownCollectionStats(t *testing.T) {
	t.Parallel()
	var b store.Batch
	Handle(commit(jetstream.OpCreate, collProfile, "self", map[string]any{"displayName": "Ann", "description": "bio"}), &b)
	Handle(commit(jetstream.OpCreate, "town.delve.graph.list", "x", map[string]any{"name": "n"}), &b)
	if len(b.Actors) != 1 || b.Actors[0].DisplayName != "Ann" {
		t.Errorf("actors: %+v", b.Actors)
	}
	// An unstored collection still shows up in the per-collection counts.
	if b.Stats[[3]string{"town.delve.graph.list", "create", "2026-10-05"}] != 1 {
		t.Errorf("stats: %v", b.Stats)
	}
	if len(b.Posts)+len(b.Interactions) != 0 {
		t.Errorf("unexpected rows for an unknown collection")
	}
}
