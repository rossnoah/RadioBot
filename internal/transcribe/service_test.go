package transcribe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rossnoah/radiobot/internal/moonshine"
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
	service := New("test-key", nil, store, moonshine.Options{})
	service.SetEndpoint(server.URL)

	// Swap in a stub so the tests never touch the real library or the model
	// download, and so the fallback path can be asserted on directly.
	service.fallback = &stubFallback{text: "on device transcript"}
	return service, store
}

// stubFallback stands in for the on-device engine.
type stubFallback struct {
	mu     sync.Mutex
	text   string
	err    error
	calls  int
	closed bool
}

func (s *stubFallback) transcribe(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.text, s.err
}

func (s *stubFallback) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *stubFallback) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
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

// TestKeytermsAreSentAsRepeatedParameters guards the query shape: Deepgram
// only boosts terms that arrive as separate keyterm parameters, and treats a
// comma-joined list as a single phrase.
func TestKeytermsAreSentAsRepeatedParameters(t *testing.T) {
	var got url.Values
	service, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Write([]byte(deepgramBody))
	})
	service.deepgram.keyterms = []string{"Smith Hall", "ten four"}

	service.Transcribe(context.Background(), tempAudio(t), nil)

	want := []string{"Smith Hall", "ten four"}
	if !reflect.DeepEqual(got["keyterm"], want) {
		t.Errorf("keyterm = %q, want %q", got["keyterm"], want)
	}
	if got.Get("model") != "nova-3" || got.Get("smart_format") != "true" {
		t.Errorf("model options were dropped from the query: %v", got)
	}
}

func TestNoKeytermsSendsNoKeytermParameter(t *testing.T) {
	var got url.Values
	service, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Write([]byte(deepgramBody))
	})

	service.Transcribe(context.Background(), tempAudio(t), nil)

	if _, ok := got["keyterm"]; ok {
		t.Errorf("keyterm sent with none configured: %v", got["keyterm"])
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

// TestFallbackAfterExhaustedRetries checks the switch to the on-device model.
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
	if got := store.last().transcript; got != "on device transcript" {
		t.Errorf("stored transcript = %q, want the on-device result", got)
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

// TestFallbackErrorStillRecordsTheRecording: if the on-device model is
// unavailable too, the recording must still land in the database with an
// empty transcript rather than being retried forever or dropped.
func TestFallbackErrorStillRecordsTheRecording(t *testing.T) {
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	stub := &stubFallback{err: errors.New("library not found")}
	service.fallback = stub

	service.Transcribe(context.Background(), tempAudio(t), nil)

	if store.count() != 1 {
		t.Fatalf("saved %d transcripts, want 1", store.count())
	}
	if got := store.last().transcript; got != "" {
		t.Errorf("transcript = %q, want empty", got)
	}
	if got := store.last().response; got != moonshineResponse {
		t.Errorf("response = %q, want %q", got, moonshineResponse)
	}
	if stub.callCount() != 1 {
		t.Errorf("fallback called %d times, want 1", stub.callCount())
	}
}

// TestCloseReleasesTheFallback keeps the model from outliving the service.
func TestCloseReleasesTheFallback(t *testing.T) {
	service, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(deepgramBody))
	})
	stub := &stubFallback{}
	service.fallback = stub

	service.Close()

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !stub.closed {
		t.Error("Close did not release the on-device model")
	}
}

// TestFallbackUsedForEveryRecordingWhileDown confirms the on-device engine
// carries the load during an outage rather than being consulted once.
func TestFallbackUsedForEveryRecordingWhileDown(t *testing.T) {
	service, store := newService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	stub := &stubFallback{text: "still listening"}
	service.fallback = stub

	for i := 0; i < 3; i++ {
		service.Transcribe(context.Background(), tempAudio(t), nil)
	}

	if stub.callCount() != 3 {
		t.Errorf("fallback called %d times for 3 recordings, want 3", stub.callCount())
	}
	if store.count() != 3 {
		t.Errorf("saved %d transcripts, want 3", store.count())
	}
}
