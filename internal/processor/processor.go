// Package processor turns a newly recorded WAV file into a transcript, a
// database row, a live UI update, and (when it matches) an alert.
package processor

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
	"github.com/rossnoah/radiobot/internal/util"
	"github.com/rossnoah/radiobot/internal/wavutil"
)

// minDuration is the length below which a recording is treated as noise and
// discarded rather than transcribed.
const minDuration = 0.5

// Store is the subset of the database the processor uses.
type Store interface {
	Transcript(filename string) (string, error)
	SaveTranscript(filename, transcript, apiResponse string, isFake bool, duration *float64) error
}

// Transcriber turns a recording into a saved transcript.
type Transcriber interface {
	Transcribe(ctx context.Context, filePath string, duration *float64)
}

// Notifier checks a transcript for alert keywords.
type Notifier interface {
	Check(message, unitName string)
}

// Broadcaster pushes a live event to connected browsers.
type Broadcaster interface {
	Broadcast(event string, data any)
}

// RadioRecorder notes that a transmission arrived, for the status page.
type RadioRecorder interface {
	RecordMessage()
}

// Processor wires together the pieces the recording pipeline needs.
type Processor struct {
	cfg         *config.Config
	store       Store
	transcriber Transcriber
	notifier    Notifier
	hub         Broadcaster
	radio       RadioRecorder
}

// New builds a processor. radio may be nil for CLI use, where there is no
// running receiver to report to.
func New(cfg *config.Config, store Store, transcriber Transcriber, notifier Notifier, hub Broadcaster, radio RadioRecorder) *Processor {
	return &Processor{cfg: cfg, store: store, transcriber: transcriber, notifier: notifier, hub: hub, radio: radio}
}

// FileData is the metadata derived from a recording's filename and header.
type FileData struct {
	FilePath      string
	Filename      string
	FolderName    string
	FormattedTime string
	Duration      string
	FileLength    float64
	RadioUID      int
	HasRadioUID   bool
	UnitName      string
}

// fileEvent is the payload broadcast to browsers when a recording lands. The
// field names match what files.html reads.
type fileEvent struct {
	Filename      string `json:"filename"`
	FormattedTime string `json:"formatted_time"`
	Duration      string `json:"duration"`
	Transcript    string `json:"transcript"`
	FolderName    string `json:"folder_name"`
	UnitName      string `json:"unit_name"`
}

// Describe extracts metadata from a recording without touching the database or
// sending notifications. It returns false when the file should be skipped.
func (p *Processor) Describe(filePath string) (FileData, bool) {
	if !strings.HasSuffix(filePath, ".wav") {
		slog.Warn("skipping non-WAV file", "file", filePath)
		return FileData{}, false
	}

	length, err := wavutil.Duration(filePath)
	if err != nil {
		slog.Warn("could not read wav duration", "file", filePath, "error", err)
		return FileData{}, false
	}
	if length < minDuration {
		slog.Info("file too short", "file", filePath, "seconds", length)
		return FileData{}, false
	}

	filename := filepath.Base(filePath)
	data := FileData{
		FilePath:      filePath,
		Filename:      filename,
		FolderName:    filepath.Base(filepath.Dir(filePath)),
		FormattedTime: util.FormatTimeFromFilename(filename),
		Duration:      util.FormatDuration(length),
		FileLength:    length,
	}
	if uid, ok := util.RadioUIDFromFilename(filename); ok {
		data.RadioUID = uid
		data.HasRadioUID = true
		data.UnitName = p.cfg.UnitName(uid)
	}
	return data, true
}

// Process handles a new recording: transcribe, store, broadcast, alert.
// emitEvent is false for batch processing, where no browser is watching.
func (p *Processor) Process(ctx context.Context, filePath string, emitEvent bool) bool {
	data, ok := p.Describe(filePath)
	if !ok {
		return false
	}

	slog.Info("processing recording", "file", data.Filename)

	if p.radio != nil {
		p.radio.RecordMessage()
	}

	// Transcribe and save; the duration is passed through so the DB caches it.
	p.transcriber.Transcribe(ctx, filePath, &data.FileLength)

	transcript, err := p.store.Transcript(filePath)
	if err != nil {
		slog.Error("could not read back transcript", "file", filePath, "error", err)
	}

	if emitEvent && p.hub != nil {
		p.hub.Broadcast("file_added", fileEvent{
			Filename:      data.Filename,
			FormattedTime: data.FormattedTime,
			Duration:      data.Duration,
			Transcript:    transcript,
			FolderName:    data.FolderName,
			UnitName:      data.UnitName,
		})
	}

	if transcript != "" && p.notifier != nil {
		p.notifier.Check(transcript, data.UnitName)
	}
	return true
}

// InjectResult identifies the recording created by InjectFake.
type InjectResult struct {
	Filename string
	Date     string
}

// InjectFake creates a synthetic transmission for the test console: a silent
// WAV plus a transcript written straight to the database, then the same
// broadcast and alert checks a real transmission would get. Nothing is sent
// to Deepgram.
func (p *Processor) InjectFake(transcript string, unitID int, unitName, recordFolder string) (*InjectResult, error) {
	now := time.Now()
	dateStr := now.Format("20060102")
	filename := fmt.Sprintf("%s_%s_%d_DMR_CC_1_GROUP_TGT_1_SRC_%d.wav",
		dateStr, now.Format("150405"), rand.Intn(90000)+10000, unitID)

	// Roughly 2.5 words/sec for radio speech, with a 2s floor.
	duration := max(2.0, float64(len(strings.Fields(transcript)))/2.5)

	dateFolder := filepath.Join(recordFolder, dateStr)
	if err := os.MkdirAll(dateFolder, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dateFolder, err)
	}
	filePath := filepath.Join(dateFolder, filename)

	const sampleRate = 8000
	if err := wavutil.WriteSilence(filePath, sampleRate, duration); err != nil {
		return nil, fmt.Errorf("creating fake WAV file: %w", err)
	}

	if err := p.store.SaveTranscript(filePath, transcript, "{}", true, &duration); err != nil {
		return nil, fmt.Errorf("saving fake transcript: %w", err)
	}

	if p.hub != nil {
		p.hub.Broadcast("file_added", fileEvent{
			Filename:      filename,
			FormattedTime: util.FormatTimeFromFilename(filename),
			Duration:      util.FormatDuration(duration),
			Transcript:    transcript,
			FolderName:    dateStr,
			UnitName:      unitName,
		})
	}

	if p.notifier != nil {
		p.notifier.Check(transcript, unitName)
	}

	slog.Info("injected fake message", "file", filename)
	return &InjectResult{Filename: filename, Date: dateStr}, nil
}
