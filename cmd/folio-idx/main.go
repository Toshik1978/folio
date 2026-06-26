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
	"github.com/Toshik1978/folio/internal/metasearch"
	msamazon "github.com/Toshik1978/folio/internal/metasearch/providers/amazon"
	msgoodreads "github.com/Toshik1978/folio/internal/metasearch/providers/goodreads"
	msgoogle "github.com/Toshik1978/folio/internal/metasearch/providers/googlebooks"
	msopenlibrary "github.com/Toshik1978/folio/internal/metasearch/providers/openlibrary"
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

	// One process-wide write guard enforces SQLite's single-writer invariant: the
	// sync engine and every API write handler serialize on it instead of racing at
	// the SQLite layer. Readers do not take it (WAL allows them alongside a writer).
	writeGuard := db.NewWriteGuard()

	// One ebook dispatcher owns the per-format parser set; injected into every consumer.
	parser := ebook.NewDispatcher(ebook.NewEPUB(), ebook.NewFB2(), ebook.NewMOBI(), ebook.NewPDF())

	// One extractor recovers covers and metadata from source files; it backs
	// lazy cover serving, cover-warming, and metadata backfill.
	extractor := ingest.NewExtractor(database, log, cfg.DataDir, parser)

	backfiller := ingest.NewLocalBackfiller(log, database, writeGuard, extractor)

	coverState := ingest.NewCoverState(database)

	coverStore, err := covers.NewStore(cfg.DataDir, extractor, coverState)
	if err != nil {
		log.Error("cover store open failed", slog.Any("error", err))
		return 2
	}

	// Cover search aggregates candidates from several providers. Google Books is
	// reused here as a cover-only source; Amazon/Goodreads scrape; Open Library
	// is REST. The providers carry their own per-request timeouts.
	const providerTimeout = 8 * time.Second
	coverRegistry := metasearch.NewRegistry(
		msamazon.New(providerTimeout),
		msgoodreads.New(providerTimeout),
		msopenlibrary.New(providerTimeout),
		msgoogle.New(googlebooks.NewClient(log, cfg.GoogleKey), ingest.VolumeToMetadata),
	)
	coverSearch := metasearch.NewAggregator(log, coverRegistry)

	// The enricher backs online metadata enrichment and Fix Match for sparse
	// books. The Coordinator reuses the Google Books adapter already in the
	// registry — one client serves both cover search and metadata enrichment.
	enricher := metasearch.NewCoordinator(log, coverRegistry, ingest.NewBookLookup(database))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authn := auth.New(log, database)
	authn.WarnIfUnprotected(ctx)

	// One broker fans live sync events to SSE subscribers; it is shared by the
	// engine (publisher) and the API (subscriber).
	broker := events.NewBroker()

	catalogHandler := api.NewCatalog(log, database, ingest.CanonicalGenres())

	syncEngine, err := sync.New(
		log,
		database,
		writeGuard,
		map[string]sync.Parser{
			libtype.Calibre: ingest.NewCalibreParser(log),
			libtype.INPX:    ingest.NewINPXParser(log),
			libtype.Folder:  ingest.NewFolderParser(log, parser),
		},
		coverStore,
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
				api.NewBooks(log, database, writeGuard, coverStore, backfiller, enricher, coverStore, coverSearch),
				catalogHandler,
				api.NewLibraries(log, database, writeGuard, syncEngine, cfg.LibraryRoot),
				api.NewSync(log, syncEngine, broker),
				settings.New(log, authn),
			},
			OPDS: opds.New(log, database, coverStore, authn, cfg.PublicURL),
		}, cfg.Env, cfg.NoColorEnabled(), cfg.PublicURL),
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
