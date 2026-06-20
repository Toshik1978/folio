package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/Toshik1978/folio/internal/api"
	"github.com/Toshik1978/folio/internal/auth"
	"github.com/Toshik1978/folio/internal/config"
	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/events"
	"github.com/Toshik1978/folio/internal/googlebooks"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/libtype"
	"github.com/Toshik1978/folio/internal/logging"
	"github.com/Toshik1978/folio/internal/opds"
	"github.com/Toshik1978/folio/internal/server"
	"github.com/Toshik1978/folio/internal/settings"
	"github.com/Toshik1978/folio/internal/sync"
)

const shutdownTimeout = 15 * time.Second

func main() {
	os.Exit(run())
}

// run is the composition root: it wires config, logging, database, covers, the
// metadata extractor, the sync engine, and the HTTP server, then serves until a
// termination signal triggers a graceful shutdown.
func run() int { //revive:disable:function-length
	_ = godotenv.Load()
	cfg := config.MustParse()
	log := logging.New(cfg.NoColorEnabled(), cfg.Env)

	database, err := db.Open(log, cfg.DataDir)
	if err != nil {
		log.Error("database open failed", slog.Any("error", err))
		return 1
	}
	defer func() { _ = database.Close() }()

	// One ebook dispatcher owns the per-format parser set; injected into every consumer.
	parser := ebook.NewDispatcher(ebook.NewEPUB(), ebook.NewFB2(), ebook.NewMOBI(), ebook.NewPDF())

	// One extractor recovers covers and metadata from source files; it backs
	// lazy cover serving, cover-warming, and metadata backfill.
	extractor := ingest.NewExtractor(database, log, cfg.DataDir, parser)

	coverStore, err := covers.NewStore(cfg.DataDir, extractor)
	if err != nil {
		log.Error("cover store open failed", slog.Any("error", err))
		return 2
	}

	// The enricher backs online (Google Books) metadata enrichment for sparse
	// books on view. The API key is optional — an empty key uses the anonymous
	// quota. Covers it fetches are cached via the cover store.
	enricher := ingest.NewEnricher(database, googlebooks.NewClient(log, cfg.GoogleKey))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authn := auth.New(log, database)
	authn.WarnIfUnprotected(ctx)

	// One broker fans live sync events to SSE subscribers; it is shared by the
	// engine (publisher) and the API (subscriber).
	broker := events.NewBroker()

	catalogHandler := api.NewCatalog(log, database)

	syncEngine, err := sync.New(
		log,
		database,
		map[string]sync.Parser{
			libtype.Calibre: ingest.NewCalibreParser(log),
			libtype.INPX:    ingest.NewINPXParser(log),
			libtype.Folder:  ingest.NewFolderParser(log, parser),
		},
		coverStore,
		extractor,
		sync.WithEvents(broker),
		sync.WithStatsObserver(catalogHandler),
	)
	if err != nil {
		log.Error("sync engine init failed", slog.Any("error", err))
		return 3
	}
	syncEngine.Start()

	srv := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: server.New(log, server.Handlers{
			API: []server.Registrar{
				api.NewBooks(log, database, coverStore, extractor, enricher, coverStore),
				catalogHandler,
				api.NewLibraries(log, database, syncEngine),
				api.NewSync(log, syncEngine, broker),
				settings.New(log, authn),
			},
			OPDS: opds.New(log, database, coverStore, authn, cfg.PublicURL),
		}, cfg.Env, cfg.NoColorEnabled()),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return serve(ctx, log, srv, syncEngine, broker)
}

// serve runs the HTTP server until ctx is cancelled (a termination signal) or
// the server fails, then performs a graceful shutdown and stops the sync engine.
func serve(
	ctx context.Context,
	log *slog.Logger,
	srv *http.Server,
	syncEngine *sync.Engine,
	broker *events.Broker,
) int {
	serverErr := make(chan error, 1)
	go func() {
		log.Info("server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		log.Error("server failed", slog.Any("error", err))
		syncEngine.Stop()
		return 4
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// Close the event broker to disconnect all long-running SSE subscribers.
	broker.Close()

	// Stop accepting requests, draining in-flight ones, then stop the engine
	// (which waits for any in-flight sync and the scheduler to wind down).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", slog.Any("error", err))
	}
	syncEngine.Stop()

	return 0
}
