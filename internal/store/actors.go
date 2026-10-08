package store

import (
	"context"
	"time"
)

// AccountsToResolve lists DIDs seen in posts or interactions that were never resolved or were
// last resolved before olderThanMS.
func (d *DB) AccountsToResolve(ctx context.Context, olderThanMS int64) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
		WITH seen(did) AS (
			SELECT did FROM posts UNION SELECT did FROM interactions
			UNION SELECT subject FROM interactions WHERE kind = 'follow'
		)
		SELECT s.did FROM seen s LEFT JOIN actors a ON a.did = s.did
		WHERE s.did LIKE 'did:%' AND (a.did IS NULL OR a.resolved_at < ?)`, olderThanMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkResolved records the outcome of resolving did. Empty values leave existing ones alone.
func (d *DB) MarkResolved(ctx context.Context, did, handle, displayName, description string) error {
	now := time.Now().UnixMilli()
	_, err := d.ExecContext(ctx, `
		INSERT INTO actors(did, handle, display_name, description, first_seen, resolved_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(did) DO UPDATE SET
			handle       = CASE WHEN excluded.handle <> '' THEN excluded.handle ELSE actors.handle END,
			display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE actors.display_name END,
			description  = CASE WHEN excluded.description <> '' THEN excluded.description ELSE actors.description END,
			resolved_at  = excluded.resolved_at`,
		did, handle, displayName, description, now, now)
	return err
}
