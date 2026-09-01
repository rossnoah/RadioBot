// Command radiobot captures DMR radio transmissions, transcribes them, and
// serves them over a web dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rossnoah/radiobot/internal/backup"
	"github.com/rossnoah/radiobot/internal/config"
	"github.com/rossnoah/radiobot/internal/db"
	"github.com/rossnoah/radiobot/internal/hub"
	"github.com/rossnoah/radiobot/internal/notify"
	"github.com/rossnoah/radiobot/internal/organizer"
	"github.com/rossnoah/radiobot/internal/processor"
	"github.com/rossnoah/radiobot/internal/radio"
	"github.com/rossnoah/radiobot/internal/transcribe"
	"github.com/rossnoah/radiobot/internal/web"
)

// recordFolder is where organized recordings live, as files/YYYYMMDD/.
const recordFolder = "files"

func main() {
	addr := flag.String("addr", ":4000", "address for the web server to listen on")
	configPath := flag.String("config", config.Path, "path to config.yaml")
	flag.Usage = usage
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(*addr, *configPath, flag.Args()); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `radiobot - DMR radio monitoring and transcription

Usage:
  radiobot [flags]                 run the server
  radiobot [flags] ingest <file>   transcribe and file one WAV, then exit

Flags:
`)
	flag.PrintDefaults()
}

func run(addr, configPath string, args []string) error {
	for _, dir := range []string{"logs", "temp", recordFolder} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// Validate the subcommand before touching config or the database, so a
	// typo fails immediately with usage instead of after startup work.
	if err := checkArgs(args); err != nil {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	store, err := db.Open(db.File)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer store.Close()
	slog.Info("database initialized")

	if len(args) > 0 {
		return runSubcommand(cfg, store, args)
	}
	return runServer(addr, cfg, store)
}

// checkArgs validates the subcommand shape.
func checkArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "ingest":
		if len(args) != 2 {
			return errors.New("usage: radiobot ingest <file.wav>")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q; run with -h for usage", args[0])
	}
}

// runSubcommand handles the one-shot CLI modes, which share the config and
// database with the server but start none of the background services.
func runSubcommand(cfg *config.Config, store *db.DB, args []string) error {
	switch args[0] {
	case "ingest":
		return ingest(cfg, store, args[1])
	default:
		return fmt.Errorf("unknown command %q; run with -h for usage", args[0])
	}
}

// ingest transcribes and records a single WAV file supplied by an external
// script, without emitting a live update (no browser is expecting one).
func ingest(cfg *config.Config, store *db.DB, filePath string) error {
	transcriber := transcribe.New(cfg.APIs.DeepgramAPIKey, store)
	notifier := notify.New(cfg.Notifications)
	proc := processor.New(cfg, store, transcriber, notifier, nil, nil)

	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("file not found: %s", filePath)
	}
	if !proc.Process(context.Background(), filePath, false) {
		return fmt.Errorf("ingest failed for %s", filePath)
	}
	slog.Info("ingested file", "file", filePath)
	return nil
}

func runServer(addr string, cfg *config.Config, store *db.DB) error {
	// Cancelled on the first shutdown signal, which stops every background service.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	events := hub.New()
	transcriber := transcribe.New(cfg.APIs.DeepgramAPIKey, store)
	notifier := notify.New(cfg.Notifications)
	radioManager := radio.New(cfg.Radio, store)
	proc := processor.New(cfg, store, transcriber, notifier, events, radioManager)

	fileOrganizer := organizer.New(proc)
	if err := fileOrganizer.Start(ctx); err != nil {
		return fmt.Errorf("starting file organizer: %w", err)
	}
	slog.Info("file organizer started")

	// Backup is opt-in; New returns nil when it is disabled, and Run is a
	// no-op on a nil service.
	go backup.New(cfg.Backup, store).Run(ctx)

	// A receiver that will not start is not fatal: the dashboard still serves
	// everything already recorded.
	if err := radioManager.Start(); err != nil {
		slog.Error("failed to start radio process", "error", err)
		slog.Error("server will continue without radio monitoring; check your configuration and dsd-fme installation")
	} else {
		status := radioManager.Status()
		slog.Info("radio monitoring started",
			"frequency_mhz", status.Config.FrequencyString(), "gain", *status.Config.Gain)
	}

	server := web.New(cfg, store, radioManager, transcriber, proc, recordFolder, events)
	defer server.Close()

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting web server", "addr", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("web server: %w", err)
		}
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("web server shutdown failed", "error", err)
	}

	slog.Info("stopping radio process")
	if err := radioManager.Stop(true); err != nil {
		slog.Error("error stopping radio process during cleanup", "error", err)
	}
	return nil
}
