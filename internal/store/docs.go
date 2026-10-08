package store

import (
	"context"
	"strings"
)

// DocText is the text that represents a post: its own words plus whatever its embed says
// (image alt text, link card title and description).
func DocText(text, embedText string) string {
	text = strings.TrimSpace(text)
	embedText = strings.TrimSpace(embedText)
	var s string
	switch {
	case text != "" && embedText != "":
		s = text + "\n" + embedText
	case text != "":
		s = text
	default:
		s = embedText
	}
	const maxRunes = 1800
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes])
	}
	return s
}

// PendingPost is a post that has no embedding yet.
type PendingPost struct {
	URI string
	Doc string
}

// PendingEmbeddings returns up to limit posts created at or after sinceMS that lack an
// embedding for model and have some text to embed.
func (d *DB) PendingEmbeddings(ctx context.Context, model string, sinceMS int64, limit int) ([]PendingPost, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT p.uri, p.text, p.embed_text, p.reply_root, COALESCE(r.text, '') FROM posts p
		LEFT JOIN embeddings e ON e.uri = p.uri AND e.model = ?
		LEFT JOIN posts r ON r.uri = p.reply_root AND p.reply_root <> p.uri
		WHERE e.uri IS NULL AND p.created_at >= ?
		ORDER BY p.created_at
		LIMIT ?`, model, sinceMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingPost
	for rows.Next() {
		var uri, text, et, root, rootText string
		if err := rows.Scan(&uri, &text, &et, &root, &rootText); err != nil {
			return nil, err
		}
		doc := DocText(text, et)
		// A reply on its own ("o7", "hi") says little. Prefix a snippet of the thread's first
		// post so a conversation lands together on the map.
		if root != "" && strings.TrimSpace(rootText) != "" {
			doc = "[thread: " + snippet(rootText, 240) + "]\n" + doc
		}
		out = append(out, PendingPost{URI: uri, Doc: doc})
	}
	return out, rows.Err()
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// PutEmbeddings stores vectors (already packed) for the given URIs.
func (d *DB) PutEmbeddings(ctx context.Context, model string, dim int, uris []string, vecs [][]byte) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO embeddings(uri,model,dim,vec) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for i, u := range uris {
		if _, err := st.ExecContext(ctx, u, model, dim, vecs[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
