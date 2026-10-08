// Command server serves the atlas website and its API.
//
//	server [-db data/delve.db] [-atlas data/atlas] [-addr :8080] [-web DIR]
//
// -web serves the frontend from a directory instead of the embedded copy, for development.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haileyok/delve-atlas/internal/agentapi"
	"github.com/haileyok/delve-atlas/internal/embed"
	"github.com/haileyok/delve-atlas/internal/server"
	"github.com/haileyok/delve-atlas/internal/store"
	"github.com/haileyok/delve-atlas/web"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "data/delve.db", "SQLite database path")
	atlasDir := flag.String("atlas", "data/atlas", "directory of atlas snapshots")
	addr := flag.String("addr", ":8080", "listen address")
	webDir := flag.String("web", "", "serve the frontend from this directory (development)")
	noSemantic := flag.Bool("no-semantic", false, "disable meaning-based search (/api/v1/search), which needs a local Ollama")
	publicURL := flag.String("public-url", "", "the site's address (https://example.com) for link-preview tags; default: taken from each request")
	embedModel := flag.String("embed-model", "nomic-embed-text", "Ollama model for search queries; must match the one the posts were embedded with")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var static fs.FS = web.Static()
	if *webDir != "" {
		static = os.DirFS(*webDir)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s := &server.Server{DB: db, AtlasDir: *atlasDir, Web: static, Log: log, PublicURL: *publicURL}

	// The agent API. Search embeds each query with the same model and recipe the posts were
	// embedded with (see cmd/embed), so the vectors are comparable.
	cfg := agentapi.Config{
		DB: db, AtlasDir: *atlasDir, Log: log,
		ModelKey: *embedModel + "+thread1",
		Activity: func(ctx context.Context, days int) (any, error) { return s.ComputeActivity(ctx, days) },
	}
	if !*noSemantic {
		cfg.Embedder = embed.New(os.Getenv("OLLAMA_URL"), *embedModel)
	}
	s.Agent = agentapi.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{
		Addr: *addr, Handler: s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute, // the snapshot's cols.json is a few MB for slow connections
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sc)
	}()
	log.Info("listening", "addr", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
