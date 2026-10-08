// Command embed fills the embeddings table for posts that don't have one, using local Ollama.
//
//	embed [-db data/delve.db] [-days 7] [-watch] [-batch 32] [-workers 4]
//
// With -watch it keeps polling for new posts; otherwise it exits when nothing is pending.
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/haileyok/delve-atlas/internal/embed"
	"github.com/haileyok/delve-atlas/internal/store"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "embed:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "data/delve.db", "SQLite database path")
	days := flag.Float64("days", 7, "embed posts created within this many days")
	watch := flag.Bool("watch", false, "keep polling for new posts")
	batch := flag.Int("batch", 32, "inputs per Ollama request")
	workers := flag.Int("workers", 4, "requests in flight")
	model := flag.String("model", "nomic-embed-text", "Ollama embedding model")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cl := embed.New(os.Getenv("OLLAMA_URL"), *model)
	// The stored model name also records how the document text is built, so changing that
	// recipe means re-embedding rather than mixing incompatible vectors.
	cl.Key = *model + "+thread1"

	var total atomic.Int64
	for {
		since := time.Now().Add(-time.Duration(*days * 24 * float64(time.Hour))).UnixMilli()
		pending, err := db.PendingEmbeddings(ctx, cl.Key, since, 4000)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			if !*watch {
				log.Info("nothing pending", "embedded", total.Load())
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Second):
			}
			continue
		}

		jobs := make(chan []store.PendingPost)
		var wg sync.WaitGroup
		var firstErr atomic.Value
		for w := 0; w < *workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for chunk := range jobs {
					if err := embedChunk(ctx, db, cl, chunk); err != nil {
						firstErr.CompareAndSwap(nil, err)
						continue
					}
					n := total.Add(int64(len(chunk)))
					if n%500 < int64(len(chunk)) {
						log.Info("embedded", "total", n)
					}
				}
			}()
		}
		for i := 0; i < len(pending); i += *batch {
			j := min(i+*batch, len(pending))
			select {
			case jobs <- pending[i:j]:
			case <-ctx.Done():
			}
		}
		close(jobs)
		wg.Wait()
		if e := firstErr.Load(); e != nil {
			return e.(error)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func embedChunk(ctx context.Context, db *store.DB, cl *embed.Client, chunk []store.PendingPost) error {
	inputs := make([]string, len(chunk))
	for i, p := range chunk {
		inputs[i] = p.Doc
		if inputs[i] == "" {
			inputs[i] = "(no text)"
		}
	}
	vecs, err := cl.Embed(ctx, inputs)
	if err != nil {
		return err
	}
	uris := make([]string, len(chunk))
	packed := make([][]byte, len(chunk))
	for i := range chunk {
		uris[i] = chunk[i].URI
		packed[i] = embed.Pack(vecs[i])
	}
	return db.PutEmbeddings(ctx, cl.Key, len(vecs[0]), uris, packed)
}
