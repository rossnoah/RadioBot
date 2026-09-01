package transcribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rossnoah/radiobot/internal/moonshine"
	"github.com/rossnoah/radiobot/internal/wavutil"
)

// initRetryInterval is how long to wait before trying to load the model again
// after a failure. Loading downloads hundreds of megabytes on a cold device,
// so a transient network failure should not be retried on every recording.
const initRetryInterval = 5 * time.Minute

// moonshineEngine is the on-device fallback. The model is loaded on first use
// rather than at startup: it costs seconds and a large resident allocation,
// and on a healthy device it is never needed at all. Run `radiobot fetch-model`
// during setup so the download does not happen mid-outage.
type moonshineEngine struct {
	opts moonshine.Options

	mu          sync.Mutex
	transcriber *moonshine.Transcriber
	lastAttempt time.Time
	lastErr     error
}

func newMoonshineEngine(opts moonshine.Options) *moonshineEngine {
	return &moonshineEngine{opts: opts}
}

// transcribe converts a recording to text using the on-device model.
func (m *moonshineEngine) transcribe(ctx context.Context, filePath string) (string, error) {
	transcriber, err := m.load(ctx)
	if err != nil {
		return "", err
	}

	samples, rate, err := wavutil.ReadPCM(filePath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", filePath, err)
	}
	return transcriber.Transcribe(samples, rate)
}

// load returns the transcriber, loading it if necessary. A failed load is
// remembered for initRetryInterval so a broken install does not retry on
// every recording.
func (m *moonshineEngine) load(ctx context.Context) (*moonshine.Transcriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.transcriber != nil {
		return m.transcriber, nil
	}
	if m.lastErr != nil && time.Since(m.lastAttempt) < initRetryInterval {
		return nil, m.lastErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	slog.Info("loading the on-device transcription model")
	m.lastAttempt = time.Now()

	transcriber, err := moonshine.Open(m.opts)
	if err != nil {
		m.lastErr = fmt.Errorf("on-device transcription unavailable: %w", err)
		return nil, m.lastErr
	}

	m.transcriber = transcriber
	m.lastErr = nil
	return transcriber, nil
}

// Close releases the model.
func (m *moonshineEngine) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.transcriber != nil {
		m.transcriber.Close()
		m.transcriber = nil
	}
}

// errNoFallback reports that no on-device engine is configured.
var errNoFallback = errors.New("no on-device transcription engine configured")
