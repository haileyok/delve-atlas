// Package store is the SQLite database shared by the ingester, the embedder, the atlas
// build and the server. All timestamps are Unix milliseconds.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS posts (
	uri          TEXT PRIMARY KEY,
	did          TEXT NOT NULL,
	rkey         TEXT NOT NULL,
	text         TEXT NOT NULL,
	langs        TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	indexed_at   INTEGER NOT NULL,
	reply_parent TEXT NOT NULL DEFAULT '',
	reply_root   TEXT NOT NULL DEFAULT '',
	quote_uri    TEXT NOT NULL DEFAULT '',
	embed_kind   TEXT NOT NULL DEFAULT '',
	embed_text   TEXT NOT NULL DEFAULT '',
	link_url     TEXT NOT NULL DEFAULT '',
	tags         TEXT NOT NULL DEFAULT '',
	n_images     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS posts_created ON posts(created_at);
CREATE INDEX IF NOT EXISTS posts_did     ON posts(did, created_at);
CREATE INDEX IF NOT EXISTS posts_root    ON posts(reply_root);
CREATE INDEX IF NOT EXISTS posts_parent  ON posts(reply_parent);

-- likes, reposts and follows. subject is a post URI for likes/reposts, a DID for follows.
CREATE TABLE IF NOT EXISTS interactions (
	uri        TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	did        TEXT NOT NULL,
	subject    TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS interactions_subject ON interactions(subject);
CREATE INDEX IF NOT EXISTS interactions_kind    ON interactions(kind, created_at);
CREATE INDEX IF NOT EXISTS interactions_did     ON interactions(did, kind);

CREATE TABLE IF NOT EXISTS actors (
	did          TEXT PRIMARY KEY,
	handle       TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	description  TEXT NOT NULL DEFAULT '',
	first_seen   INTEGER NOT NULL,
	active       INTEGER NOT NULL DEFAULT 1,
	status       TEXT NOT NULL DEFAULT '',
	resolved_at  INTEGER NOT NULL DEFAULT 0
);

-- every collection and operation seen, per UTC day, so we know what exists on the network
CREATE TABLE IF NOT EXISTS collection_stats (
	collection TEXT NOT NULL,
	op         TEXT NOT NULL,
	day        TEXT NOT NULL,
	n          INTEGER NOT NULL,
	PRIMARY KEY (collection, op, day)
);

CREATE TABLE IF NOT EXISTS embeddings (
	uri   TEXT PRIMARY KEY,
	model TEXT NOT NULL,
	dim   INTEGER NOT NULL,
	vec   BLOB NOT NULL
);
`

// DB wraps the SQLite handle.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*DB, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(OFF)")
	d, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(8)
	if _, err := d.Exec(schema); err != nil {
		d.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{d}, nil
}

// migrate adds columns that older databases lack.
func migrate(d *sql.DB) error {
	has := func(table, col string) (bool, error) {
		rows, err := d.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return false, err
			}
			if n == col {
				return true, nil
			}
		}
		return false, rows.Err()
	}
	if ok, err := has("actors", "resolved_at"); err != nil {
		return err
	} else if !ok {
		if _, err := d.Exec(`ALTER TABLE actors ADD COLUMN resolved_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

// Cursor returns the saved Jetstream sequence number.
func (d *DB) Cursor(ctx context.Context) (uint64, bool, error) {
	var s string
	err := d.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='cursor'`).Scan(&s)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil, err
}

// SetMeta stores a string value.
func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Meta reads a string value ("" when absent).
func (d *DB) Meta(ctx context.Context, key string) (string, error) {
	var s string
	err := d.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&s)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return s, err
}
