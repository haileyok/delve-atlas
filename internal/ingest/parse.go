// Package ingest follows Jetstream for the town.delve.* collections and writes them to SQLite.
package ingest

import (
	"strings"
	"time"

	"github.com/bluesky-social/jetstream"

	"github.com/haileyok/delve-atlas/internal/store"
)

const (
	collPost    = "town.delve.feed.post"
	collLike    = "town.delve.feed.like"
	collRepost  = "town.delve.feed.repost"
	collFollow  = "town.delve.graph.follow"
	collProfile = "town.delve.actor.profile"
)

// Collections is the Jetstream filter. The wildcard also counts collections we don't store
// (so we can see what else exists on the network); account and identity events pass anyway.
var Collections = []string{"town.delve.*"}

// maxRecordAge: when a repo is resynced, Jetstream re-sends all its records as creates with
// their original rkey and createdAt. Records much older than their event are still stored (the
// atlas filters on created_at itself) but flagged here for stats.
const maxFuture = 10 * time.Minute

// Handle appends the rows for one event to the batch.
func Handle(ev *jetstream.Event, b *store.Batch) {
	at := time.UnixMicro(ev.TimeUS).UTC()
	switch ev.Kind {
	case jetstream.KindAccount:
		if a := ev.Account; a != nil {
			b.Actors = append(b.Actors, store.ActorUpdate{
				DID: ev.DID, SeenAt: at.UnixMilli(), SetActive: true, Active: a.Active, Status: a.Status,
			})
		}
		return
	case jetstream.KindIdentity:
		if i := ev.Identity; i != nil && i.Handle != "" {
			b.Actors = append(b.Actors, store.ActorUpdate{DID: ev.DID, Handle: i.Handle, SeenAt: at.UnixMilli()})
		}
		return
	case jetstream.KindCommit:
	default:
		return
	}
	c := ev.Commit
	if c == nil {
		return
	}
	b.Stat(c.Collection, string(c.Operation), at.Format("2006-01-02"))
	uri := "at://" + ev.DID + "/" + c.Collection + "/" + c.Rkey

	if c.Operation == jetstream.OpDelete {
		switch c.Collection {
		case collPost:
			b.DeletePosts = append(b.DeletePosts, uri)
		case collLike, collRepost, collFollow:
			b.DeleteInter = append(b.DeleteInter, uri)
		}
		return
	}

	rec := c.Record
	switch c.Collection {
	case collPost:
		b.Posts = append(b.Posts, parsePost(ev.DID, c.Rkey, uri, rec, at))
	case collLike, collRepost:
		subj := str(mapv(rec, "subject"), "uri")
		if subj == "" {
			return
		}
		kind := "like"
		if c.Collection == collRepost {
			kind = "repost"
		}
		b.Interactions = append(b.Interactions, store.Interaction{
			URI: uri, Kind: kind, DID: ev.DID, Subject: subj, CreatedAt: parseTime(str(rec, "createdAt"), at).UnixMilli(),
		})
	case collFollow:
		subj, _ := rec["subject"].(string)
		if subj == "" {
			return
		}
		b.Interactions = append(b.Interactions, store.Interaction{
			URI: uri, Kind: "follow", DID: ev.DID, Subject: subj, CreatedAt: parseTime(str(rec, "createdAt"), at).UnixMilli(),
		})
	case collProfile:
		b.Actors = append(b.Actors, store.ActorUpdate{
			DID: ev.DID, DisplayName: str(rec, "displayName"), Description: str(rec, "description"), SeenAt: at.UnixMilli(),
		})
	}
}

func parsePost(did, rkey, uri string, rec map[string]any, at time.Time) store.Post {
	p := store.Post{
		URI: uri, DID: did, Rkey: rkey,
		Text:        str(rec, "text"),
		IndexedAt:   at.UnixMilli(),
		CreatedAt:   parseTime(str(rec, "createdAt"), at).UnixMilli(),
		ReplyParent: str(mapv(mapv(rec, "reply"), "parent"), "uri"),
		ReplyRoot:   str(mapv(mapv(rec, "reply"), "root"), "uri"),
	}
	// A createdAt in the future would put the post at the edge of the atlas window forever.
	if max := at.Add(maxFuture).UnixMilli(); p.CreatedAt > max {
		p.CreatedAt = p.IndexedAt
	}
	var langs, tags []string
	if l, ok := rec["langs"].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				langs = append(langs, s)
			}
		}
	}
	if l, ok := rec["tags"].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}
	p.Langs = strings.Join(langs, ",")
	p.Tags = strings.Join(tags, ",")

	if emb := mapv(rec, "embed"); emb != nil {
		t, _ := emb["$type"].(string)
		p.EmbedKind = lastSegment(t)
		var texts []string
		collectText(emb, &texts)
		p.EmbedText = strings.Join(texts, "\n")
		p.NImages = countImages(emb)
		switch p.EmbedKind {
		case "record":
			p.QuoteURI = str(mapv(emb, "record"), "uri")
		case "recordWithMedia":
			p.QuoteURI = str(mapv(mapv(emb, "record"), "record"), "uri")
			if m := mapv(emb, "media"); m != nil {
				if mt, _ := m["$type"].(string); mt != "" {
					p.EmbedKind += "+" + lastSegment(mt)
				}
			}
		}
		if ext := findExternal(emb); ext != nil {
			p.LinkURL = str(ext, "uri")
		}
	}
	return p
}

func lastSegment(s string) string {
	s = strings.TrimSuffix(s, "#main")
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// collectText gathers the human text of an embed (image alt text, link card title and
// description) without depending on the exact embed shapes.
func collectText(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if s, ok := vv.(string); ok {
				switch k {
				case "alt", "title", "description":
					if t := strings.TrimSpace(s); t != "" {
						*out = append(*out, t)
					}
				}
				continue
			}
			collectText(vv, out)
		}
	case []any:
		for _, vv := range x {
			collectText(vv, out)
		}
	}
}

func countImages(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["image"]; ok {
			n++
		}
		for _, vv := range x {
			n += countImages(vv)
		}
	case []any:
		for _, vv := range x {
			n += countImages(vv)
		}
	}
	return n
}

func findExternal(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		if e, ok := x["external"].(map[string]any); ok {
			return e
		}
		for _, vv := range x {
			if e := findExternal(vv); e != nil {
				return e
			}
		}
	case []any:
		for _, vv := range x {
			if e := findExternal(vv); e != nil {
				return e
			}
		}
	}
	return nil
}

func mapv(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[k].(map[string]any)
	return v
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func parseTime(s string, fallback time.Time) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return fallback
}
