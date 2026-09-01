// Package organizer watches the temp folder dsd-fme writes into and files each
// recording under files/YYYYMMDD/ before handing it to the processor.
package organizer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/rossnoah/radiobot/internal/util"
)

const (
	TempFolder  = "temp"
	FilesFolder = "files"

	// processingWindow is how long a path stays marked as recently handled,
	// so a create followed by a rename does not process the same file twice.
	processingWindow = 2 * time.Second

	// queueSize is how many recordings may be waiting for the processor. The
	// worker runs serially (a transcription can take minutes), so the queue
	// absorbs bursts that would otherwise stall the fsnotify event loop.
	queueSize = 256
)

// Processor handles a filed recording.
type Processor interface {
	Process(ctx context.Context, filePath string, emitEvent bool) bool
}

// Organizer moves recordings out of temp and into dated folders.
type Organizer struct {
	processor Processor

	mu        sync.Mutex
	processed map[string]time.Time

	queue chan string
}

// New builds an organizer.
func New(processor Processor) *Organizer {
	return &Organizer{
		processor: processor,
		processed: make(map[string]time.Time),
		queue:     make(chan string, queueSize),
	}
}

// Start files any recordings already sitting in temp, then watches for new
// ones until ctx is cancelled. It returns once the watcher is established.
func (o *Organizer) Start(ctx context.Context) error {
	for _, dir := range []string{TempFolder, FilesFolder} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(TempFolder); err != nil {
		watcher.Close()
		return err
	}

	go o.processLoop(ctx)
	go o.watchLoop(ctx, watcher)

	// Backfill anything left over from a previous run before new events arrive.
	o.organizeExisting()

	slog.Info("watching temp folder", "path", TempFolder)
	return nil
}

func (o *Organizer) watchLoop(ctx context.Context, watcher *fsnotify.Watcher) {
	defer watcher.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// A file created in temp and a file moved into temp both surface
			// as Create, covering both cases the Python handlers watched for.
			if !event.Has(fsnotify.Create) || !strings.HasSuffix(event.Name, ".wav") {
				continue
			}
			slog.Info("new recording detected", "path", event.Name)
			o.organize(event.Name)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("file watching error", "error", err)
		}
	}
}

// processLoop consumes filed recordings one at a time. Serial processing
// preserves the ordering the single-threaded Python observer had, and keeps
// concurrent transcriptions from stacking up against Deepgram.
func (o *Organizer) processLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case path := <-o.queue:
			o.processor.Process(ctx, path, true)
		}
	}
}

// organize moves one recording from temp into its dated folder and queues it
// for processing. It returns whether the file was moved.
func (o *Organizer) organize(filePath string) bool {
	if o.recentlyProcessed(filePath) {
		slog.Debug("skipping duplicate processing", "path", filePath)
		return false
	}

	if _, err := os.Stat(filePath); err != nil {
		slog.Debug("file no longer exists (likely already moved)", "path", filePath)
		return false
	}

	filename := filepath.Base(filePath)
	dateStr := util.DateFromFilename(filename)
	if dateStr == "" {
		slog.Warn("could not parse date from filename", "filename", filename)
		return false
	}

	targetDir := filepath.Join(FilesFolder, dateStr)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		slog.Error("could not create target directory", "path", targetDir, "error", err)
		return false
	}
	targetPath := filepath.Join(targetDir, filename)

	if _, err := os.Stat(targetPath); err == nil {
		slog.Warn("recording already exists at target, overwriting", "path", targetPath)
	}

	if err := move(filePath, targetPath); err != nil {
		slog.Error("could not move recording", "from", filePath, "to", targetPath, "error", err)
		return false
	}
	slog.Info("filed recording", "filename", filename, "folder", targetDir)

	select {
	case o.queue <- targetPath:
	default:
		slog.Error("processing queue is full, dropping recording", "path", targetPath)
		return false
	}
	return true
}

// recentlyProcessed marks a path as handled and reports whether it already was.
func (o *Organizer) recentlyProcessed(filePath string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	for path, seen := range o.processed {
		if now.Sub(seen) > processingWindow {
			delete(o.processed, path)
		}
	}
	if _, ok := o.processed[filePath]; ok {
		return true
	}
	o.processed[filePath] = now
	return false
}

// organizeExisting files any recordings left in temp from a previous run.
func (o *Organizer) organizeExisting() {
	entries, err := os.ReadDir(TempFolder)
	if err != nil {
		slog.Error("could not read temp folder", "error", err)
		return
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".wav") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		slog.Info("no existing files in temp folder")
		return
	}
	slog.Info("found existing recordings in temp folder", "count", len(names))

	organized := 0
	for _, name := range names {
		if o.organize(filepath.Join(TempFolder, name)) {
			organized++
		}
	}
	slog.Info("organized existing recordings", "organized", organized, "total", len(names))
}

// move renames a file, falling back to copy-and-delete when the source and
// destination are on different filesystems.
func move(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscallEXDEV) {
		// Rename fails with EXDEV across filesystems; anything else is fatal
		// unless the copy fallback happens to succeed, so try it either way.
		slog.Debug("rename failed, falling back to copy", "from", src, "error", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	in.Close()
	return os.Remove(src)
}
