package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bluesky-social/jetstream"

	"github.com/haileyok/delve-atlas/internal/store"
)

// Config for the ingester.
type Config struct {
	Host       string        // Jetstream host
	APIKey     string        // archive replay key
	StartBack  time.Duration // on a first run, how far back to replay
	FlushRows  int
	FlushEvery time.Duration
}

// Ingester runs the Jetstream → SQLite loop.
type Ingester struct {
	Cfg Config
	DB  *store.DB
	Log *slog.Logger
}

// Archive downloads are metered (a burst allowance, then a slow refill), so we fetch a couple
// of whole segments at a time rather than hammering it.
const (
	downloadConcurrency = 2
	segmentStripes      = 1
	// While replaying history (further behind than this), any stream error restarts from the
	// last written position: the SDK would otherwise skip a failed segment, leaving a hole.
	replayingLag = time.Hour
)

var errRestart = errors.New("restart stream")

// Run consumes events until ctx ends or the stream fails fatally.
func (in *Ingester) Run(ctx context.Context) error {
	after, skipBefore, err := in.startPoint(ctx)
	if err != nil {
		return err
	}
	backoff := 30 * time.Second
	for {
		cursor, err := in.runOnce(ctx, after, skipBefore)
		if !errors.Is(err, errRestart) {
			return err
		}
		if cursor > after {
			after = cursor
			backoff = 30 * time.Second
		}
		in.Log.Warn("restarting stream from last written position", "after_seq", after, "wait", backoff.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Minute)
	}
}

func (in *Ingester) startPoint(ctx context.Context) (uint64, time.Time, error) {
	seq, ok, err := in.DB.Cursor(ctx)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("load cursor: %w", err)
	}
	if ok {
		in.Log.Info("resuming from saved cursor", "seq", seq)
		return seq, time.Time{}, nil
	}
	start := time.Now().Add(-in.Cfg.StartBack)
	seq, err = SeqAtTime(ctx, in.Cfg.Host, in.Cfg.APIKey, start)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("find start sequence: %w", err)
	}
	in.Log.Info("first run: replaying archive", "start_time", start.UTC().Format(time.RFC3339), "after_seq", seq)
	_ = in.DB.SetMeta(ctx, "backfill_start", start.UTC().Format(time.RFC3339))
	return seq, start, nil
}

func (in *Ingester) runOnce(ctx context.Context, after uint64, skipBefore time.Time) (uint64, error) {
	in.Log.Info("starting stream", "after_seq", after)
	client, err := jetstream.Subscribe(in.Cfg.Host,
		jetstream.WithAPIKey(in.Cfg.APIKey),
		jetstream.WithCollections(Collections),
		// Commits only. Account and identity events appear in nearly every archive block, so
		// asking for them turns a sparse plan (a few blocks per segment) into whole-segment
		// downloads of the entire network. Handles are resolved separately.
		jetstream.WithKinds([]jetstream.Kind{jetstream.KindCommit}),
		jetstream.WithAfterSeq(after),
		jetstream.WithBatchSize(1024),
		jetstream.WithDownloadConcurrency(downloadConcurrency),
		jetstream.WithSegmentStripes(segmentStripes),
		jetstream.WithLogger(in.Log.With("component", "jetstream")),
	)
	if err != nil {
		return after, fmt.Errorf("subscribe: %w", err)
	}
	defer client.Close()

	var (
		batch      store.Batch
		lastCursor = after
		lastFlush  = time.Now()
		lastLog    = time.Now()
		lastEvent  time.Time
		nEvents    int
		nPosts     int
	)
	skipUS := skipBefore.UnixMicro()

	flush := func() error {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		backoff := time.Second
		for {
			err := in.DB.Write(fctx, &batch, lastCursor)
			if err == nil {
				break
			}
			in.Log.Error("write failed; retrying", "err", err)
			select {
			case <-fctx.Done():
				return err
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 15*time.Second)
		}
		batch.Reset()
		lastFlush = time.Now()
		return nil
	}

	for evs, err := range client.Events(ctx) {
		if err != nil {
			if errors.Is(err, jetstream.ErrFatal) {
				return lastCursor, err
			}
			if ctx.Err() != nil {
				break
			}
			in.Log.Warn("stream error", "err", err)
			if lastEvent.IsZero() || time.Since(lastEvent) > replayingLag {
				if err := flush(); err != nil {
					return lastCursor, err
				}
				return lastCursor, errRestart
			}
			continue
		}
		events := evs.Events()
		for i := range events {
			ev := &events[i]
			if ev.TimeUS < skipUS {
				continue
			}
			Handle(ev, &batch)
			nEvents++
			lastEvent = time.UnixMicro(ev.TimeUS)
		}
		nPosts = len(batch.Posts)
		if c := evs.LastCursor(); c > 0 {
			lastCursor = c
		}
		if batch.Len() >= in.Cfg.FlushRows || time.Since(lastFlush) >= in.Cfg.FlushEvery {
			if err := flush(); err != nil {
				return lastCursor, err
			}
		}
		if time.Since(lastLog) >= 15*time.Second {
			st := client.Stats()
			attrs := []any{"cursor", lastCursor, "events", nEvents, "pending_posts", nPosts, "archive_remaining_seqs", st.ResidualGap}
			if !lastEvent.IsZero() {
				attrs = append(attrs, "event_time", lastEvent.UTC().Format(time.RFC3339), "lag", time.Since(lastEvent).Round(time.Second).String())
			}
			in.Log.Info("progress", attrs...)
			lastLog = time.Now()
		}
		if ctx.Err() != nil {
			break
		}
	}
	if batch.Len() > 0 || lastCursor > after {
		if err := flush(); err != nil {
			return lastCursor, err
		}
	}
	if ctx.Err() != nil {
		return lastCursor, ctx.Err()
	}
	return lastCursor, errRestart
}
