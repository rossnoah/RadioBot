package transcribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// savedTranscript is one call to the fake store.
type savedTranscript struct {
	filename   string
	transcript string
	response   string
	isFake     bool
	duration   *float64
}

type fakeStore struct {
	mu    sync.Mutex
	saved []savedTranscript
}

func (f *fakeStore) SaveTranscript(filename, transcript, apiResponse string, isFake bool, duration *float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, savedTranscript{filename, transcript, apiResponse, isFake, duration})
	return nil
}

func (f *fakeStore) last() savedTranscript {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saved[len(f.saved)-1]
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

const deepgramBody = `{"results":{"channels":[{"alternatives":[{"transcript":"unit one en route"}]}]}}`

func tempAudio(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.wav")
	if err := os.WriteFile(path, []byte("RIFF....WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newService wires a service against a stub Deepgram endpoint, with the retry
// delay shortened so failure paths do not make the test sleep for 10 seconds.
func newService(t *testing.T, handler http.HandlerFunc) (*Service, *fakeStore) {
	t.Helper()

	original := retryDelay
	retryDelay = 20 * time.Millisecond
	t.Cleanup(func() { retryDelay = original })

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	store := &fakeStore{}
	service := New("test-key", store)
	service.SetEndpoint(server.URL)
	return service, store
}

func TestTranscribeSuccess(t *testing.T) {
	var gotAuth, gotContentType string
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		w.Write([]byte(deepgramBody))
	})

	duration := 3.5
	service.Transcribe(context.Background(), tempAudio(t), &duration)

	if store.count() != 1 {
		t.Fatalf("saved %d transcripts, want 1", store.count())
	}
	saved := store.last()
	if saved.transcript != "unit one en route" {
		t.Errorf("transcript = %q", saved.transcript)
	}
	if saved.response != deepgramBody {
		t.Errorf("stored response = %q, want the raw Deepgram body", saved.response)
	}
	if saved.isFake {
		t.Error("a real transcription was marked fake")
	}
	if saved.duration == nil || *saved.duration != 3.5 {
		t.Errorf("duration = %v, want 3.5", saved.duration)
	}
	if gotAuth != "Token test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotContentType != "audio/wav" {
		t.Errorf("Content-Type = %q", gotContentType)
	}

	if status := service.Status(); status.Engine != "deepgram" {
		t.Errorf("engine = %q, want deepgram", status.Engine)
	}
}

// TestTranscribeRetriesThenSucceeds covers a transient Deepgram failure that
// resolves before the retry budget is spent.
func TestTranscribeRetriesThenSucceeds(t *testing.T) {
	var attempts int
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			http.Error(w, "upstream hiccup", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(deepgramBody))
	})

	service.Transcribe(context.Background(), tempAudio(t), nil)

	if attempts != 2 {
		t.Errorf("made %d attempts, want 2", attempts)
	}
	if store.last().transcript != "unit one en route" {
		t.Errorf("transcript = %q", store.last().transcript)
	}
	if service.Status().Engine != "deepgram" {
		t.Error("a recovered transcription left the service in fallback")
	}
}

// TestFallbackAfterExhaustedRetries checks the switch to Moonshine. The
// sidecar is absent in tests, so the transcript is empty — what matters is
// that the recording is still recorded and the engine state flips.
func TestFallbackAfterExhaustedRetries(t *testing.T) {
	var attempts int
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "down", http.StatusServiceUnavailable)
	})

	start := time.Now()
	service.Transcribe(context.Background(), tempAudio(t), nil)
	elapsed := time.Since(start)

	if attempts != maxRetries {
		t.Errorf("made %d attempts, want %d", attempts, maxRetries)
	}
	// Two waits of retryDelay sit between the three attempts.
	if elapsed < retryDelay {
		t.Errorf("retried without waiting (elapsed %v)", elapsed)
	}

	if store.count() != 1 {
		t.Fatalf("saved %d transcripts, want 1", store.count())
	}
	if got := store.last().response; got != moonshineResponse {
		t.Errorf("stored response = %q, want %q", got, moonshineResponse)
	}

	status := service.Status()
	if status.Engine != "moonshine" {
		t.Errorf("engine = %q, want moonshine", status.Engine)
	}
	if status.FallbackSince == "" {
		t.Error("fallback timestamp was not recorded")
	}
}

// TestFallbackDoesNotProbeDeepgramEveryFile is the point of the retry
// interval: while in fallback, Deepgram is probed at most once per interval.
func TestFallbackDoesNotProbeDeepgramEveryFile(t *testing.T) {
	var attempts int
	service, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "down", http.StatusServiceUnavailable)
	})

	service.Transcribe(context.Background(), tempAudio(t), nil)
	attemptsAfterFallback := attempts

	// Three more recordings arrive well inside the retry interval.
	for i := 0; i < 3; i++ {
		service.Transcribe(context.Background(), tempAudio(t), nil)
	}
	if attempts != attemptsAfterFallback {
		t.Errorf("probed Deepgram %d extra times inside the retry interval",
			attempts-attemptsAfterFallback)
	}
}

// TestDeepgramRecoveryExitsFallback covers the probe succeeding.
func TestDeepgramRecoveryExitsFallback(t *testing.T) {
	var healthy bool
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(deepgramBody))
	})

	service.Transcribe(context.Background(), tempAudio(t), nil)
	if service.Status().Engine != "moonshine" {
		t.Fatal("service did not enter fallback")
	}

	// Make the next probe due, then bring Deepgram back.
	service.mu.Lock()
	service.lastDeepgramRetry = time.Now().Add(-2 * deepgramRetryInterval)
	service.mu.Unlock()
	healthy = true

	service.Transcribe(context.Background(), tempAudio(t), nil)

	status := service.Status()
	if status.Engine != "deepgram" {
		t.Errorf("engine = %q, want deepgram after recovery", status.Engine)
	}
	if status.FallbackSince != "" {
		t.Errorf("fallback timestamp = %q, want cleared", status.FallbackSince)
	}
	if store.last().transcript != "unit one en route" {
		t.Errorf("transcript = %q", store.last().transcript)
	}
}

// TestTranscribeStopsOnCancelledContext keeps shutdown from waiting out the
// full retry schedule.
func TestTranscribeStopsOnCancelledContext(t *testing.T) {
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		service.Transcribe(ctx, tempAudio(t), nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Transcribe did not return promptly on a cancelled context")
	}
	if store.count() != 0 {
		t.Error("a cancelled transcription still wrote a transcript")
	}
}
