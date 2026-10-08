// Command ingest backfills town.delve.* from Jetstream's archive and then follows the live stream,
// writing posts, likes, reposts, follows and profiles to SQLite.
//
// Environment / flags:
//
//	JETSTREAM_API_KEY   required (archive replay)
//	JETSTREAM_HOST      default jetstream.us-east.bsky.network
//	-db                 SQLite path (default data/delve.db)
//	-days               on a first run, how many days to replay (default 7)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haileyok/delve-atlas/internal/ingest"
	"github.com/haileyok/delve-atlas/internal/store"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "data/delve.db", "SQLite database path")
	days := flag.Float64("days", 7, "on a first run, how many days of history to replay")
	flag.Parse()

	key := os.Getenv("JETSTREAM_API_KEY")
	if key == "" {
		return errors.New("JETSTREAM_API_KEY must be set")
	}
	host := os.Getenv("JETSTREAM_HOST")
	if host == "" {
		host = "jetstream.us-east.bsky.network"
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	in := &ingest.Ingester{
		Cfg: ingest.Config{
			Host: host, APIKey: key,
			StartBack:  time.Duration(*days * 24 * float64(time.Hour)),
			FlushRows:  5000,
			FlushEvery: 2 * time.Second,
		},
		DB:  db,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	return in.Run(ctx)
}
