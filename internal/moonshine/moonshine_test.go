package moonshine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// TestABIMatchesUpstream is the guard that makes this package safe to write.
// A wrong stride does not produce an error, it walks native memory at the
// wrong offset and crashes, so these sizes are asserted against the same
// constants the upstream Python binding asserts.
func TestABIMatchesUpstream(t *testing.T) {
	if abiMismatch != nil {
		t.Fatalf("ABI check failed at init: %v", abiMismatch)
	}

	tests := []struct {
		name     string
		actual   uintptr
		expected uintptr
	}{
		{"transcript_t", unsafe.Sizeof(transcriptC{}), 16},
		{"transcript_line_t", unsafe.Sizeof(transcriptLineC{}), 88},
		{"speaker_span_t", unsafe.Sizeof(speakerSpanC{}), 40},
		{"transcript_word_t", unsafe.Sizeof(transcriptWordC{}), 24},
	}
	for _, tt := range tests {
		if tt.actual != tt.expected {
			t.Errorf("sizeof(%s) = %d, want %d", tt.name, tt.actual, tt.expected)
		}
	}

	// Every field offset that matters for walking the transcript.
	if got := unsafe.Offsetof(transcriptC{}.lines); got != 0 {
		t.Errorf("offsetof(transcript_t.lines) = %d, want 0", got)
	}
	if got := unsafe.Offsetof(transcriptC{}.lineCount); got != 8 {
		t.Errorf("offsetof(transcript_t.line_count) = %d, want 8", got)
	}
	if got := unsafe.Offsetof(transcriptLineC{}.text); got != 0 {
		t.Errorf("offsetof(transcript_line_t.text) = %d, want 0", got)
	}
}

func TestFormatVersion(t *testing.T) {
	tests := []struct {
		version int32
		want    string
	}{
		{30000, "3.0.0"},
		{30105, "3.1.5"},
		{0, "0.0.0"},
	}
	for _, tt := range tests {
		if got := formatVersion(tt.version); got != tt.want {
			t.Errorf("formatVersion(%d) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

// TestOpenWithMissingLibraryFails must be a clean error, not a crash: this is
// the path a device without the library takes on every fallback attempt.
func TestOpenWithMissingLibraryFails(t *testing.T) {
	_, err := Open(Options{
		LibraryPath: filepath.Join(t.TempDir(), "libmoonshine-does-not-exist.so"),
		ModelDir:    t.TempDir(),
	})
	if err == nil {
		t.Fatal("Open succeeded with no library present")
	}
	if !strings.Contains(err.Error(), "libmoonshine-does-not-exist") {
		t.Errorf("error = %v, want it to name the library it could not load", err)
	}
}

// TestOpenRejectsSomethingThatIsNotMoonshine covers a library that loads but
// has none of the expected symbols; purego panics on a missing symbol, and
// that has to surface as an error.
func TestOpenRejectsSomethingThatIsNotMoonshine(t *testing.T) {
	// libc is present on both targets and certainly exports none of these.
	candidates := []string{"libSystem.B.dylib", "libc.so.6"}
	if runtime.GOOS != "darwin" {
		candidates = []string{"libc.so.6", "libSystem.B.dylib"}
	}

	var lastErr error
	for _, candidate := range candidates {
		_, err := Open(Options{LibraryPath: candidate, ModelDir: t.TempDir()})
		if err == nil {
			t.Fatalf("Open succeeded against %s, which is not moonshine", candidate)
		}
		lastErr = err
		if strings.Contains(err.Error(), "missing an expected symbol") {
			return // the case we are checking for
		}
	}
	t.Logf("no system library loaded to exercise the missing-symbol path (last error: %v)", lastErr)
}

func TestResolveLibraryPrefersExplicitPath(t *testing.T) {
	if got, err := resolveLibrary("/custom/libmoonshine.so"); err != nil || got != "/custom/libmoonshine.so" {
		t.Errorf("resolveLibrary = (%q, %v), want the explicit path", got, err)
	}
}

func TestResolveLibraryUsesEnvironment(t *testing.T) {
	t.Setenv("MOONSHINE_LIB", "/from/env/libmoonshine.so")
	if got, err := resolveLibrary(""); err != nil || got != "/from/env/libmoonshine.so" {
		t.Errorf("resolveLibrary = (%q, %v), want the MOONSHINE_LIB path", got, err)
	}
}

func TestResolveLibraryFallsBackToPlatformName(t *testing.T) {
	t.Setenv("MOONSHINE_LIB", "")

	// Run from a directory with no lib/ so only the bare name is left.
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)

	got, err := resolveLibrary("")
	if err != nil {
		t.Fatal(err)
	}
	want := "libmoonshine.so"
	if runtime.GOOS == "darwin" {
		want = "libmoonshine.dylib"
	}
	if filepath.Base(got) != want {
		t.Errorf("resolveLibrary = %q, want it to end in %q", got, want)
	}
}

func TestGoStringHandlesNil(t *testing.T) {
	if got := goString(nil); got != "" {
		t.Errorf("goString(nil) = %q, want empty", got)
	}
}

func TestGoStringReadsCString(t *testing.T) {
	buf := append([]byte("engine one responding"), 0)
	if got := goString(unsafe.Pointer(&buf[0])); got != "engine one responding" {
		t.Errorf("goString = %q", got)
	}
}

func TestTrimSpace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  hello  ", "hello"},
		{"\n\thello\r\n", "hello"},
		{"", ""},
		{"   ", ""},
		{"no-space", "no-space"},
	}
	for _, tt := range tests {
		if got := trimSpace(tt.in); got != tt.want {
			t.Errorf("trimSpace(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestReadTranscriptJoinsLines builds a transcript in Go memory laid out the
// way the C library lays one out, and walks it with the real reader.
func TestReadTranscriptJoinsLines(t *testing.T) {
	first := append([]byte("engine one responding"), 0)
	second := append([]byte("  to the structure fire  "), 0)
	blank := append([]byte("   "), 0)

	lines := make([]transcriptLineC, 3)
	lines[0].text = unsafe.Pointer(&first[0])
	lines[1].text = unsafe.Pointer(&blank[0]) // whitespace-only lines are dropped
	lines[2].text = unsafe.Pointer(&second[0])

	transcript := transcriptC{lines: unsafe.Pointer(&lines[0]), lineCount: 3}

	got := readTranscript(unsafe.Pointer(&transcript))
	want := "engine one responding to the structure fire"
	if got != want {
		t.Errorf("readTranscript = %q, want %q", got, want)
	}
}

func TestReadTranscriptEmpty(t *testing.T) {
	empty := transcriptC{}
	if got := readTranscript(unsafe.Pointer(&empty)); got != "" {
		t.Errorf("readTranscript on an empty transcript = %q", got)
	}
}

// TestOpenRefusesWhenTheABIIsUnknown covers the guard directly: whatever the
// reason a layout cannot be trusted, Open must decline rather than read
// native memory at guessed offsets.
func TestOpenRefusesWhenTheABIIsUnknown(t *testing.T) {
	original := abiMismatch
	abiMismatch = errors.New("simulated layout mismatch")
	defer func() { abiMismatch = original }()

	_, err := Open(Options{LibraryPath: "libmoonshine.so", ModelDir: t.TempDir()})
	if err == nil {
		t.Fatal("Open proceeded despite an ABI mismatch")
	}
	if !strings.Contains(err.Error(), "simulated layout mismatch") {
		t.Errorf("error = %v, want the ABI mismatch", err)
	}
}
