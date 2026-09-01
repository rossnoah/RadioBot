package organizer

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rossnoah/radiobot/internal/wavutil"
)

// recorder captures the paths handed to the processor.
type recorder struct {
	mu      sync.Mutex
	handled []string
	done    chan struct{}
	want    int
}

func newRecorder(want int) *recorder {
	return &recorder{done: make(chan struct{}), want: want}
}

func (r *recorder) Process(_ context.Context, filePath string, _ bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handled = append(r.handled, filePath)
	if len(r.handled) == r.want {
		close(r.done)
	}
	return true
}

func (r *recorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.handled...)
}

// inTempDir runs the test in a scratch working directory, since the organizer
// resolves temp/ and files/ relative to it.
func inTempDir(t *testing.T) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(original) })
}

func writeRecording(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := wavutil.WriteSilence(path, 8000, 1); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizeMovesRecordingIntoDateFolder(t *testing.T) {
	inTempDir(t)

	name := "20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1001.wav"
	source := filepath.Join(TempFolder, name)
	writeRecording(t, source)

	proc := newRecorder(1)
	org := New(proc)

	if !org.organize(source) {
		t.Fatal("organize returned false")
	}

	target := filepath.Join(FilesFolder, "20251113", name)
	if _, err := os.Stat(target); err != nil {
		t.Errorf("recording was not moved to %s: %v", target, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Error("recording is still in the temp folder")
	}

	// The move queues the file; the processor runs on its own goroutine.
	go org.processLoop(context.Background())
	select {
	case <-proc.done:
	case <-time.After(2 * time.Second):
		t.Fatal("processor was never handed the recording")
	}
	if got := proc.paths(); len(got) != 1 || got[0] != target {
		t.Errorf("processor received %v, want [%s]", got, target)
	}
}

// TestOrganizeSkipsDuplicates covers the dedup window that keeps a create plus
// a rename from processing the same recording twice.
func TestOrganizeSkipsDuplicates(t *testing.T) {
	inTempDir(t)

	name := "20251113_200214_1_SRC_1.wav"
	source := filepath.Join(TempFolder, name)
	writeRecording(t, source)

	org := New(newRecorder(1))
	if !org.organize(source) {
		t.Fatal("first organize returned false")
	}
	if org.organize(source) {
		t.Error("a repeat organize inside the dedup window was not skipped")
	}
}

func TestOrganizeRejectsUnparseableName(t *testing.T) {
	inTempDir(t)

	source := filepath.Join(TempFolder, "not-a-recording.wav")
	writeRecording(t, source)

	if New(newRecorder(1)).organize(source) {
		t.Error("organized a file with no date in its name")
	}
	if _, err := os.Stat(source); err != nil {
		t.Error("a file with an unparseable name was moved anyway")
	}
}

func TestOrganizeMissingFile(t *testing.T) {
	inTempDir(t)
	if New(newRecorder(1)).organize(filepath.Join(TempFolder, "20251113_200214_1_SRC_1.wav")) {
		t.Error("organized a file that does not exist")
	}
}

// TestStartFilesExistingRecordings covers the startup backfill of anything
// left in temp/ by a previous run.
func TestStartFilesExistingRecordings(t *testing.T) {
	inTempDir(t)

	names := []string{
		"20251113_200214_1_SRC_1.wav",
		"20251114_090000_2_SRC_2.wav",
	}
	for _, name := range names {
		writeRecording(t, filepath.Join(TempFolder, name))
	}
	// A non-WAV file must be left alone.
	if err := os.WriteFile(filepath.Join(TempFolder, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proc := newRecorder(2)
	if err := New(proc).Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-proc.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("processor received %v, want both recordings", proc.paths())
	}

	for _, expected := range []string{
		filepath.Join(FilesFolder, "20251113", names[0]),
		filepath.Join(FilesFolder, "20251114", names[1]),
	} {
		if _, err := os.Stat(expected); err != nil {
			t.Errorf("missing %s: %v", expected, err)
		}
	}
	if _, err := os.Stat(filepath.Join(TempFolder, "notes.txt")); err != nil {
		t.Error("a non-WAV file was moved out of temp")
	}
}

// TestWatchPicksUpNewRecordings exercises the fsnotify path end to end.
func TestWatchPicksUpNewRecordings(t *testing.T) {
	inTempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proc := newRecorder(1)
	if err := New(proc).Start(ctx); err != nil {
		t.Fatal(err)
	}

	name := "20251115_101112_7_SRC_1003.wav"
	writeRecording(t, filepath.Join(TempFolder, name))

	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher never picked up the new recording")
	}

	want := filepath.Join(FilesFolder, "20251115", name)
	if got := proc.paths(); len(got) != 1 || got[0] != want {
		t.Errorf("processor received %v, want [%s]", got, want)
	}
}
