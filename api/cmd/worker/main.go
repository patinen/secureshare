package main

import (
	"context"
	"flag"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"os"
	"os/signal"
	"secureshare/api/internal/cleanup"
	"secureshare/api/internal/config"
	"secureshare/api/internal/database"
	"secureshare/api/internal/storage"
	"secureshare/api/internal/uploads"
	"syscall"
	"time"
)

func main() {
	once := flag.Bool("once", false, "perform one coordinated cleanup sweep and exit")
	flag.Parse()
	if !run(*once) {
		os.Exit(1)
	}
}
func run(once bool) bool {
	c, err := config.LoadWorker()
	if err != nil {
		log.Print("worker configuration failed")
		return false
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	db, err := pgxpool.New(ctx, c.DatabaseURL)
	if err != nil {
		log.Print("worker database configuration failed")
		return false
	}
	defer db.Close()
	if db.Ping(ctx) != nil || database.Migrate(ctx, db) != nil {
		log.Print("worker database initialization failed")
		return false
	}
	objects, err := storage.New(ctx, c.Storage)
	if err != nil || uploads.Ensure(c.Cleanup.TempDir) != nil {
		log.Print("worker storage initialization failed")
		return false
	}
	runner := cleanup.Runner{DB: db, Objects: objects, Options: c.Cleanup}
	ticker := time.NewTicker(c.Cleanup.Interval)
	defer ticker.Stop()
	log.Print("SecureShare cleanup worker started")
	for {
		stats, err := runner.RunOnce(ctx)
		if ctx.Err() != nil {
			log.Print("SecureShare cleanup worker stopped")
			return true
		}
		log.Printf("cleanup pending_removed=%d purged=%d temporary_removed=%d failures=%d skipped=%t", stats.PendingRemoved, stats.Purged, stats.TempRemoved, stats.Failures, stats.Skipped)
		if err != nil {
			log.Print("cleanup sweep failed")
		}
		if once {
			return err == nil && stats.Failures == 0
		}
		select {
		case <-ctx.Done():
			log.Print("SecureShare cleanup worker stopped")
			return true
		case <-ticker.C:
		}
	}
}
