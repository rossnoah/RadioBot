package transcribe

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rossnoah/radiobot/internal/moonshine"
)

const (
	maxRetries = 3
	// deepgramRetryInterval is how long to wait between Deepgram probes while
	// running on the Moonshine fallback.
	deepgramRetryInterval = 5 * time.Minute

	moonshineResponse = `{"engine": "moonshine"}`
)

// retryDelay is the pause between Deepgram attempts. It is a variable so tests
// can shorten it rather than sleeping through the real schedule.
var retryDelay = 5 * time.Second

// Store is the subset of the database the transcriber writes to.
type Store interface {
	SaveTranscript(filename, transcript, apiResponse string, isFake bool, duration *float64) error
}

// Status reports which engine is currently in use, for the dashboard.
type Status struct {
	Engine        string `json:"engine"`
	FallbackSince string `json:"fallback_since"`
}

// Fallback is an on-device transcription engine, used when Deepgram cannot
// be reached.
type Fallback interface {
	transcribe(ctx context.Context, filePath string) (string, error)
	Close()
}

// Service transcribes recordings, falling back from Deepgram to Moonshine
// when Deepgram fails and probing periodically for its recovery.
type Service struct {
	deepgram *deepgramClient
	fallback Fallback
	store    Store

	mu                sync.Mutex
	usingFallback     bool
	fallbackSince     string
	lastDeepgramRetry time.Time
}

// New builds a transcription service backed by the given Deepgram key, with
// the on-device Moonshine model as its fallback.
func New(apiKey string, store Store, moonshineOpts moonshine.Options) *Service {
	return &Service{
		deepgram: newDeepgramClient(apiKey),
		fallback: newMoonshineEngine(moonshineOpts),
		store:    store,
	}
}

// Close releases the on-device model.
func (s *Service) Close() {
	if s.fallback != nil {
		s.fallback.Close()
	}
}

// SetEndpoint overrides the Deepgram endpoint. It exists for tests.
func (s *Service) SetEndpoint(url string) { s.deepgram.url = url }

// Status returns the current engine state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usingFallback {
		return Status{Engine: "moonshine", FallbackSince: s.fallbackSince}
	}
	return Status{Engine: "deepgram"}
}

// Transcribe transcribes a file and saves the result. A nil duration leaves
// the column NULL for the backfill to fill in later.
func (s *Service) Transcribe(ctx context.Context, filePath string, duration *float64) {
	if s.inFallback() {
		s.transcribeInFallback(ctx, filePath, duration)
		return
	}

	// Normal path: try Deepgram with retries.
	for attempt := 1; attempt <= maxRetries; attempt++ {
		transcript, raw, err := s.deepgram.transcribe(ctx, filePath)
		if err == nil {
			s.save(filePath, transcript, raw, duration)
			slog.Info("transcribed", "file", filePath)
			return
		}
		if attempt < maxRetries {
			slog.Warn("transcription attempt failed, retrying",
				"attempt", attempt, "of", maxRetries, "file", filePath, "error", err, "retry_in", retryDelay)
			if !sleep(ctx, retryDelay) {
				return
			}
			continue
		}
		slog.Error("deepgram failed after all attempts",
			"attempts", maxRetries, "file", filePath, "error", err)
	}

	// All Deepgram retries exhausted — switch to fallback.
	s.enterFallback()
	slog.Warn("switching to moonshine fallback after deepgram failures")
	s.saveViaMoonshine(ctx, filePath, duration)
}

// transcribeInFallback runs while Deepgram is considered down. It probes
// Deepgram at most once per deepgramRetryInterval and otherwise uses Moonshine.
func (s *Service) transcribeInFallback(ctx context.Context, filePath string, duration *float64) {
	if s.shouldProbeDeepgram() {
		transcript, raw, err := s.deepgram.transcribe(ctx, filePath)
		if err == nil {
			s.exitFallback()
			s.save(filePath, transcript, raw, duration)
			slog.Info("deepgram recovered", "file", filePath)
			return
		}
		slog.Warn("deepgram still failing during retry", "error", err)
	}
	s.saveViaMoonshine(ctx, filePath, duration)
}

func (s *Service) saveViaMoonshine(ctx context.Context, filePath string, duration *float64) {
	var transcript string
	if s.fallback == nil {
		slog.Error("no on-device transcription engine configured", "file", filePath)
	} else if text, err := s.fallback.transcribe(ctx, filePath); err != nil {
		// Record the (empty) result anyway so the recording still appears in
		// the UI and is not retried forever.
		slog.Error("on-device transcription failed", "file", filePath, "error", err)
	} else {
		transcript = text
	}

	s.save(filePath, transcript, moonshineResponse, duration)
	slog.Info("transcribed on device", "file", filePath)
}

func (s *Service) save(filePath, transcript, raw string, duration *float64) {
	if err := s.store.SaveTranscript(filePath, transcript, raw, false, duration); err != nil {
		slog.Error("could not save transcript", "file", filePath, "error", err)
	}
}

func (s *Service) inFallback() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usingFallback
}

// shouldProbeDeepgram reports whether enough time has passed to try Deepgram
// again, recording the attempt so concurrent callers do not all probe at once.
func (s *Service) shouldProbeDeepgram() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastDeepgramRetry.IsZero() && time.Since(s.lastDeepgramRetry) < deepgramRetryInterval {
		return false
	}
	s.lastDeepgramRetry = time.Now()
	return true
}

func (s *Service) enterFallback() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usingFallback = true
	s.fallbackSince = time.Now().Format("2006-01-02 15:04:05")
	s.lastDeepgramRetry = time.Now()
}

func (s *Service) exitFallback() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usingFallback = false
	s.fallbackSince = ""
}

// sleep waits for d, reporting false if the context is cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
