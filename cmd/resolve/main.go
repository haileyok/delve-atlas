// Command resolve fills in handles, display names and bios for every account seen in posts
// and interactions, from DID documents and each account's Delve profile record.
//
//	resolve [-db data/delve.db] [-watch] [-refresh 24h]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/haileyok/delve-atlas/internal/identity"
	"github.com/haileyok/delve-atlas/internal/store"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "data/delve.db", "SQLite database path")
	watch := flag.Bool("watch", false, "keep running, resolving new accounts every few minutes")
	refresh := flag.Duration("refresh", 24*time.Hour, "re-resolve accounts older than this")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	res := identity.New()

	for {
		dids, err := db.AccountsToResolve(ctx, time.Now().Add(-*refresh).UnixMilli())
		if err != nil {
			return err
		}
		if len(dids) > 0 {
			log.Info("resolving accounts", "n", len(dids))
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		var mu sync.Mutex
		var failed int
		for _, did := range dids {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				a, err := res.Resolve(ctx, did)
				if err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					log.Warn("resolve failed", "did", did, "err", err)
					// Mark it tried so a dead DID isn't retried every pass.
					_ = db.MarkResolved(ctx, did, "", "", "")
					return
				}
				if err := db.MarkResolved(ctx, did, a.Handle, a.DisplayName, a.Description); err != nil {
					log.Error("save actor", "did", did, "err", err)
				}
			}()
		}
		wg.Wait()
		if len(dids) > 0 {
			log.Info("resolved", "n", len(dids), "failed", failed)
		}
		if !*watch {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Minute):
		}
	}
}
