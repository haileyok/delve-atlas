package store

import (
	"context"
	"strconv"
)

// Post is one town.delve.feed.post record.
type Post struct {
	URI, DID, Rkey, Text, Langs         string
	CreatedAt, IndexedAt                int64
	ReplyParent, ReplyRoot, QuoteURI    string
	EmbedKind, EmbedText, LinkURL, Tags string
	NImages                             int
}

// Interaction is a like, repost or follow.
type Interaction struct {
	URI, Kind, DID, Subject string
	CreatedAt               int64
}

// ActorUpdate carries whatever we learned about an account; empty fields are left alone.
// Active is only applied when SetActive is true.
type ActorUpdate struct {
	DID, Handle, DisplayName, Description string
	SeenAt                                int64
	SetActive                             bool
	Active                                bool
	Status                                string
}

// Batch collects rows to write in one transaction.
type Batch struct {
	Posts        []Post
	Interactions []Interaction
	Actors       []ActorUpdate
	DeletePosts  []string          // URIs
	DeleteInter  []string          // URIs
	Stats        map[[3]string]int // collection, op, day
}

// Len is the number of pending rows.
func (b *Batch) Len() int {
	return len(b.Posts) + len(b.Interactions) + len(b.Actors) + len(b.DeletePosts) + len(b.DeleteInter) + len(b.Stats)
}

// Reset clears the batch, keeping allocated slices.
func (b *Batch) Reset() {
	b.Posts = b.Posts[:0]
	b.Interactions = b.Interactions[:0]
	b.Actors = b.Actors[:0]
	b.DeletePosts = b.DeletePosts[:0]
	b.DeleteInter = b.DeleteInter[:0]
	b.Stats = nil
}

// Stat counts one event.
func (b *Batch) Stat(collection, op, day string) {
	if b.Stats == nil {
		b.Stats = make(map[[3]string]int)
	}
	b.Stats[[3]string{collection, op, day}]++
}

// Write applies the batch and, when cursor > 0, saves it in the same transaction so the
// saved position never gets ahead of the rows.
func (d *DB) Write(ctx context.Context, b *Batch, cursor uint64) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(b.Posts) > 0 {
		st, err := tx.PrepareContext(ctx, `INSERT INTO posts
			(uri,did,rkey,text,langs,created_at,indexed_at,reply_parent,reply_root,quote_uri,embed_kind,embed_text,link_url,tags,n_images)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(uri) DO UPDATE SET text=excluded.text, langs=excluded.langs, embed_kind=excluded.embed_kind,
				embed_text=excluded.embed_text, link_url=excluded.link_url, tags=excluded.tags, n_images=excluded.n_images`)
		if err != nil {
			return err
		}
		for _, p := range b.Posts {
			if _, err := st.ExecContext(ctx, p.URI, p.DID, p.Rkey, p.Text, p.Langs, p.CreatedAt, p.IndexedAt, p.ReplyParent,
				p.ReplyRoot, p.QuoteURI, p.EmbedKind, p.EmbedText, p.LinkURL, p.Tags, p.NImages); err != nil {
				st.Close()
				return err
			}
		}
		st.Close()
	}
	if len(b.Interactions) > 0 {
		st, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO interactions(uri,kind,did,subject,created_at) VALUES (?,?,?,?,?)`)
		if err != nil {
			return err
		}
		for _, i := range b.Interactions {
			if _, err := st.ExecContext(ctx, i.URI, i.Kind, i.DID, i.Subject, i.CreatedAt); err != nil {
				st.Close()
				return err
			}
		}
		st.Close()
	}
	for _, a := range b.Actors {
		active := 1
		if a.SetActive && !a.Active {
			active = 0
		}
		setActive := 0
		if a.SetActive {
			setActive = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO actors(did,handle,display_name,description,first_seen,active,status)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(did) DO UPDATE SET
				handle       = CASE WHEN excluded.handle <> '' THEN excluded.handle ELSE actors.handle END,
				display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE actors.display_name END,
				description  = CASE WHEN excluded.description <> '' THEN excluded.description ELSE actors.description END,
				active       = CASE WHEN ?=1 THEN excluded.active ELSE actors.active END,
				status       = CASE WHEN ?=1 THEN excluded.status ELSE actors.status END`,
			a.DID, a.Handle, a.DisplayName, a.Description, a.SeenAt, active, a.Status, setActive, setActive); err != nil {
			return err
		}
	}
	for _, u := range b.DeletePosts {
		if _, err := tx.ExecContext(ctx, `DELETE FROM posts WHERE uri=?`, u); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE uri=?`, u); err != nil {
			return err
		}
	}
	for _, u := range b.DeleteInter {
		if _, err := tx.ExecContext(ctx, `DELETE FROM interactions WHERE uri=?`, u); err != nil {
			return err
		}
	}
	for k, n := range b.Stats {
		if _, err := tx.ExecContext(ctx, `INSERT INTO collection_stats(collection,op,day,n) VALUES (?,?,?,?)
			ON CONFLICT(collection,op,day) DO UPDATE SET n = n + excluded.n`, k[0], k[1], k[2], n); err != nil {
			return err
		}
	}
	if cursor > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			strconv.FormatUint(cursor, 10)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
