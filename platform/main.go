package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/valid/ntm/platform/internal/api"
	"github.com/valid/ntm/platform/internal/config"
	"github.com/valid/ntm/platform/internal/db"
)

//go:embed web
var webFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[ntm-platform] ")

	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	store, err := db.Open(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()
	log.Printf("connected to database, schema ready")

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("web assets: %v", err)
	}
	web := http.FileServer(http.FS(sub))

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.New(store, cfg).Routes(web),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Schedule ticker: fire due recurring scans.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if n := store.RunDueSchedules(ctx); n > 0 {
				log.Printf("schedules: triggered %d scan(s)", n)
			}
			cancel()
		}
	}()

	go func() {
		log.Printf("NTM platform listening on %s (HTTPS/TLS)", cfg.ListenAddr)
		err := srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
	shutCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	_ = srv.Shutdown(shutCtx)
}
