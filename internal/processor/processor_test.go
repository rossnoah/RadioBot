package processor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rossnoah/radiobot/internal/config"
	"github.com/rossnoah/radiobot/internal/wavutil"
)

type fakeStore struct {
	mu          sync.Mutex
	transcripts map[string]string
	saved       []string
	fakeFlags   map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{transcripts: map[string]string{}, fakeFlags: map[string]bool{}}
}

func (f *fakeStore) Transcript(filename string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transcripts[filename], nil
}

func (f *fakeStore) SaveTranscript(filename, transcript, _ string, isFake bool, _ *float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcripts[filename] = transcript
	f.fakeFlags[filename] = isFake
	f.saved = append(f.saved, filename)
	return nil
}

// stubTranscriber writes a fixed transcript, standing in for Deepgram.
type stubTranscriber struct {
	store  *fakeStore
	text   string
	called int
}

func (s *stubTranscriber) Transcribe(_ context.Context, filePath string, duration *float64) {
	s.called++
	s.store.SaveTranscript(filePath, s.text, "{}", false, duration)
}

type stubNotifier struct {
	messages []string
	units    []string
}

func (s *stubNotifier) Check(message, unitName string) {
	s.messages = append(s.messages, message)
	s.units = append(s.units, unitName)
}

type stubHub struct {
	events []string
	data   []any
}

func (s *stubHub) Broadcast(event string, data any) {
	s.events = append(s.events, event)
	s.data = append(s.data, data)
}

type stubRadio struct{ recorded int }

func (s *stubRadio) RecordMessage() { s.recorded++ }

func testConfig() *config.Config {
	gain := 32
	return &config.Config{
		Radio: config.Radio{Frequency: 461.375, Gain: &gain},
		Units: map[int]string{1001: "Unit 1: John Doe"},
	}
}

func TestProcessTranscribesBroadcastsAndAlerts(t *testing.T) {
	dir := t.TempDir()
	name := "20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1001.wav"
	path := filepath.Join(dir, "20251113", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := wavutil.WriteSilence(path, 8000, 3); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	transcriber := &stubTranscriber{store: store, text: "unit one en route"}
	notifier := &stubNotifier{}
	hub := &stubHub{}
	radio := &stubRadio{}

	proc := New(testConfig(), store, transcriber, notifier, hub, radio)

	if !proc.Process(context.Background(), path, true) {
		t.Fatal("Process returned false")
	}
	if transcriber.called != 1 {
		t.Errorf("transcriber called %d times, want 1", transcriber.called)
	}
	if radio.recorded != 1 {
		t.Errorf("radio.RecordMessage called %d times, want 1", radio.recorded)
	}
	if len(hub.events) != 1 || hub.events[0] != "file_added" {
		t.Fatalf("broadcast events = %v", hub.events)
	}

	event, ok := hub.data[0].(fileEvent)
	if !ok {
		t.Fatalf("broadcast payload has type %T", hub.data[0])
	}
	if event.Filename != name {
		t.Errorf("event filename = %q", event.Filename)
	}
	if event.FolderName != "20251113" {
		t.Errorf("event folder = %q, want 20251113", event.FolderName)
	}
	if event.UnitName != "Unit 1: John Doe" {
		t.Errorf("event unit = %q", event.UnitName)
	}
	if event.Transcript != "unit one en route" {
		t.Errorf("event transcript = %q", event.Transcript)
	}
	if event.Duration != "0:00:03" {
		t.Errorf("event duration = %q, want 0:00:03", event.Duration)
	}

	if len(notifier.messages) != 1 || notifier.messages[0] != "unit one en route" {
		t.Errorf("notifier saw %v", notifier.messages)
	}
	if notifier.units[0] != "Unit 1: John Doe" {
		t.Errorf("notifier unit = %q", notifier.units[0])
	}
}

func TestProcessSkipsShortAndNonWAV(t *testing.T) {
	dir := t.TempDir()

	short := filepath.Join(dir, "20251113_200214_1_SRC_1.wav")
	if err := wavutil.WriteSilence(short, 8000, 0.2); err != nil {
		t.Fatal(err)
	}
	notAudio := filepath.Join(dir, "20251113_200214_1_SRC_1.txt")
	if err := os.WriteFile(notAudio, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	transcriber := &stubTranscriber{store: store, text: "should not happen"}
	proc := New(testConfig(), store, transcriber, &stubNotifier{}, &stubHub{}, &stubRadio{})

	for _, path := range []string{short, notAudio, filepath.Join(dir, "absent.wav")} {
		if proc.Process(context.Background(), path, true) {
			t.Errorf("Process accepted %s", path)
		}
	}
	if transcriber.called != 0 {
		t.Errorf("transcriber ran %d times on skipped files", transcriber.called)
	}
}

// TestProcessWithoutEventDoesNotBroadcast covers batch and CLI processing.
func TestProcessWithoutEventDoesNotBroadcast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20251113_200214_1_SRC_1001.wav")
	if err := wavutil.WriteSilence(path, 8000, 2); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	hub := &stubHub{}
	proc := New(testConfig(), store, &stubTranscriber{store: store, text: "text"}, &stubNotifier{}, hub, &stubRadio{})

	proc.Process(context.Background(), path, false)
	if len(hub.events) != 0 {
		t.Errorf("broadcast %d events with emitEvent=false", len(hub.events))
	}
}

// TestProcessSkipsAlertsForEmptyTranscript keeps a silent recording from
// triggering the notifier with an empty message.
func TestProcessSkipsAlertsForEmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20251113_200214_1_SRC_1001.wav")
	if err := wavutil.WriteSilence(path, 8000, 2); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	notifier := &stubNotifier{}
	proc := New(testConfig(), store, &stubTranscriber{store: store, text: ""}, notifier, &stubHub{}, &stubRadio{})

	proc.Process(context.Background(), path, true)
	if len(notifier.messages) != 0 {
		t.Errorf("notifier ran on an empty transcript: %v", notifier.messages)
	}
}

func TestInjectFake(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStore()
	notifier := &stubNotifier{}
	hub := &stubHub{}
	transcriber := &stubTranscriber{store: store, text: "unreachable"}

	proc := New(testConfig(), store, transcriber, notifier, hub, &stubRadio{})

	result, err := proc.InjectFake("this is a test transmission", 9999, "Test Unit", dir)
	if err != nil {
		t.Fatalf("InjectFake: %v", err)
	}

	path := filepath.Join(dir, result.Date, result.Filename)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fake recording not created at %s: %v", path, err)
	}
	if !strings.Contains(result.Filename, "SRC_9999") {
		t.Errorf("filename %q does not carry the unit ID", result.Filename)
	}

	// A five-word transcript is under the 2s floor.
	duration, err := wavutil.Duration(path)
	if err != nil {
		t.Fatal(err)
	}
	if duration != 2 {
		t.Errorf("duration = %v, want the 2s floor", duration)
	}

	if transcriber.called != 0 {
		t.Error("InjectFake called the transcriber; it should never hit Deepgram")
	}
	if got := store.transcripts[path]; got != "this is a test transmission" {
		t.Errorf("stored transcript = %q", got)
	}
	if !store.fakeFlags[path] {
		t.Error("injected transcript was not flagged as fake")
	}
	if len(hub.events) != 1 {
		t.Errorf("broadcast %d events, want 1", len(hub.events))
	}
	if len(notifier.messages) != 1 || notifier.units[0] != "Test Unit" {
		t.Errorf("notifier saw messages=%v units=%v", notifier.messages, notifier.units)
	}
}

// TestInjectFakeLongTranscript checks the ~2.5 words/sec duration estimate.
func TestInjectFakeLongTranscript(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStore()
	proc := New(testConfig(), store, &stubTranscriber{store: store}, &stubNotifier{}, &stubHub{}, &stubRadio{})

	words := strings.Repeat("word ", 25) // 25 words -> 10 seconds
	result, err := proc.InjectFake(words, 1001, "", dir)
	if err != nil {
		t.Fatal(err)
	}

	duration, err := wavutil.Duration(filepath.Join(dir, result.Date, result.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if duration != 10 {
		t.Errorf("duration = %v, want 10", duration)
	}
}

func TestDescribeUnknownUnit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20251113_200214_1_SRC_4242.wav")
	if err := wavutil.WriteSilence(path, 8000, 2); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	proc := New(testConfig(), store, &stubTranscriber{store: store}, nil, nil, nil)

	data, ok := proc.Describe(path)
	if !ok {
		t.Fatal("Describe returned false")
	}
	if data.UnitName != "Unknown. Radio ID: 4242" {
		t.Errorf("unit name = %q", data.UnitName)
	}
	if data.RadioUID != 4242 || !data.HasRadioUID {
		t.Errorf("radio uid = %d (%v)", data.RadioUID, data.HasRadioUID)
	}
}
