package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
	"os/signal"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/config"
	"secureshare/api/internal/database"
	apihttp "secureshare/api/internal/http"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/storage"
	"secureshare/api/internal/users"
	"syscall"
	"time"
)

func main() {
	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	db, err := pgxpool.New(ctx, c.DatabaseURL)
	if err != nil {
		log.Fatal("database configuration failed")
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		log.Fatal("database connection failed")
	}
	if err = database.Migrate(ctx, db); err != nil {
		log.Fatal("database migration failed")
	}
	objects, err := storage.New(ctx, c.Storage)
	if err != nil {
		log.Fatal("storage configuration failed")
	}
	server := &http.Server{Addr: ":" + c.Port, Handler: apihttp.Router(auth.New(c, users.Store{DB: db}), shares.Store{DB: db, Objects: objects}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 150 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("SecureShare API listening")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("HTTP server failed")
	}
}
