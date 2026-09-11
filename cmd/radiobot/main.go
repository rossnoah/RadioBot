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
	"sync"
	"syscall"
	"time"

	"github.com/rossnoah/radiobot/internal/backup"
	"github.com/rossnoah/radiobot/internal/config"
	"github.com/rossnoah/radiobot/internal/db"
	"github.com/rossnoah/radiobot/internal/escalation"
	"github.com/rossnoah/radiobot/internal/hub"
	"github.com/rossnoah/radiobot/internal/moonshine"
	"github.com/rossnoah/radiobot/internal/notify"
	"github.com/rossnoah/radiobot/internal/organizer"
	"github.com/rossnoah/radiobot/internal/processor"
	"github.com/rossnoah/radiobot/internal/radio"
	"github.com/rossnoah/radiobot/internal/systemd"
	"github.com/rossnoah/radiobot/internal/transcribe"
	"github.com/rossnoah/radiobot/internal/web"
)

// recordFolder is where organized recordings live, as files/YYYYMMDD/.
const recordFolder = "files"

func main() {
	addr := flag.String("addr", ":4000", "address for the web server to listen on")
	configPath := flag.String("config", config.Path, "path to config.yaml")
	moonshineLib := flag.String("moonshine-lib", "",
		"path to libmoonshine (default: $MOONSHINE_LIB, then lib/ beside the binary)")
	moonshineModels := flag.String("moonshine-models", moonshine.DefaultModelDir,
		"directory holding the on-device transcription model")
	notifyMode := flag.String("notify", "send",
		"where alerts go: \"send\" delivers to GroupMe/Discord, \"console\" only logs them (use this when testing against a real config)")
	flag.Usage = usage
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	opts := moonshine.Options{LibraryPath: *moonshineLib, ModelDir: *moonshineModels}
	if err := run(*addr, *configPath, *notifyMode, opts, flag.Args()); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `radiobot - DMR radio monitoring and transcription

Usage:
  radiobot [flags]                 run the server
  radiobot [flags] ingest <file>   transcribe and file one WAV, then exit
  radiobot [flags] fetch-model     download the on-device fallback model

When testing against a real config, pass -notify console so alerts are logged
instead of delivered to GroupMe or Discord.

Flags:
`)
	flag.PrintDefaults()
}

func run(addr, configPath, notifyMode string, moonshineOpts moonshine.Options, args []string) error {
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

	notifier, err := newNotifier(cfg, notifyMode)
	if err != nil {
		return err
	}

	if len(args) > 0 {
		return runSubcommand(cfg, store, notifier, moonshineOpts, args)
	}
	return runServer(addr, cfg, store, notifier, moonshineOpts)
}

// newNotifier builds the alert sender for the chosen mode. Console mode exists
// because these alerts reach real people: running the app against a real
// config to test something should not page them.
func newNotifier(cfg *config.Config, mode string) (*notify.Notifier, error) {
	switch mode {
	case "send":
		return notify.New(cfg.Notifications), nil
	case "console":
		slog.Warn("notifications are in console mode; nothing will be delivered")
		return notify.NewConsole(cfg.Notifications), nil
	default:
		return nil, fmt.Errorf("unknown -notify mode %q; want \"send\" or \"console\"", mode)
	}
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
	case "fetch-model":
		if len(args) != 1 {
			return errors.New("usage: radiobot fetch-model")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q; run with -h for usage", args[0])
	}
}

// runSubcommand handles the one-shot CLI modes, which share the config and
// database with the server but start none of the background services.
func runSubcommand(cfg *config.Config, store *db.DB, notifier *notify.Notifier,
	moonshineOpts moonshine.Options, args []string) error {
	switch args[0] {
	case "ingest":
		return ingest(cfg, store, notifier, moonshineOpts, args[1])
	case "fetch-model":
		return fetchModel(moonshineOpts)
	default:
		return fmt.Errorf("unknown command %q; run with -h for usage", args[0])
	}
}

// ingest transcribes and records a single WAV file supplied by an external
// script, without emitting a live update (no browser is expecting one).
func ingest(cfg *config.Config, store *db.DB, notifier *notify.Notifier,
	moonshineOpts moonshine.Options, filePath string) error {
	transcriber := transcribe.New(cfg.APIs.DeepgramAPIKey, cfg.APIs.DeepgramKeyterms, store, moonshineOpts)
	defer transcriber.Close()
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

// fetchModel downloads the on-device transcription model ahead of time, so a
// Deepgram outage does not also mean waiting on a large download.
func fetchModel(opts moonshine.Options) error {
	slog.Info("fetching the on-device transcription model", "dir", opts.ModelDir)

	transcriber, err := moonshine.Open(opts)
	if err != nil {
		return err
	}
	transcriber.Close()

	slog.Info("the on-device transcription model is ready")
	return nil
}

func runServer(addr string, cfg *config.Config, store *db.DB, notifier *notify.Notifier,
	moonshineOpts moonshine.Options) error {
	// Cancelled on the first shutdown signal, which stops every background service.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sd := systemd.Connect()
	defer sd.Close()

	events := hub.New()
	transcriber := transcribe.New(cfg.APIs.DeepgramAPIKey, cfg.APIs.DeepgramKeyterms, store, moonshineOpts)
	defer transcriber.Close()
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

	// Escalating means exiting non-zero so systemd restarts the whole service,
	// which clears state an in-process restart cannot. The policy refuses to
	// escalate once it has done so too often, so a fault that survives a
	// restart or a reboot cannot turn into a loop.
	escalating := make(chan struct{})
	var giveUpOnce sync.Once
	policy := escalation.New(store, notifier, func() {
		giveUpOnce.Do(func() { close(escalating) })
	})

	supervisorDone := make(chan struct{})
	go func() {
		defer close(supervisorDone)
		radioManager.Supervise(ctx, policy)
	}()

	server := web.New(cfg, store, radioManager, transcriber, proc, recordFolder, events, policy)
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

	sd.Ready()
	sd.Status("serving on %s", addr)
	go pingWatchdog(ctx, sd, radioManager)

	var exitErr error
	select {
	case err := <-serverErr:
		if err != nil {
			exitErr = fmt.Errorf("web server: %w", err)
		}
	case <-escalating:
		// The escalation policy has already logged and alerted; exiting is
		// the handoff to systemd.
		exitErr = errors.New("radio unrecoverable in-process, restarting the service")
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	sd.Stopping()
	stop() // stop the background services before tearing down the server

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("web server shutdown failed", "error", err)
	}

	slog.Info("stopping radio process")
	select {
	case <-supervisorDone:
	case <-time.After(15 * time.Second):
		slog.Error("radio supervisor did not stop within 15s")
	}
	return exitErr
}

// pingWatchdog tells systemd the service is alive, but only while the radio
// supervisor is still cycling. If that goroutine wedges, the pings stop and
// systemd restarts the service — which is the whole point of WatchdogSec, and
// the one failure no amount of in-process logic can catch itself.
func pingWatchdog(ctx context.Context, sd *systemd.Notifier, manager *radio.Manager) {
	if !sd.WatchdogEnabled() {
		return
	}
	// Allow several supervisor cycles to be missed before declaring it stuck,
	// so ordinary scheduling jitter never trips the watchdog.
	staleAfter := 4 * radio.HeartbeatInterval()

	ticker := time.NewTicker(sd.WatchdogInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if idle := time.Since(manager.Heartbeat()); idle > staleAfter {
				slog.Error("radio supervisor has stalled; withholding the systemd watchdog ping",
					"idle", idle.Round(time.Second))
				continue
			}
			sd.Alive()
		}
	}
}
