// Package moonshine transcribes audio with the Moonshine on-device speech
// model, used as the fallback when Deepgram is unavailable.
//
// Moonshine ships a portable C ABI, and every official binding — including
// the Python one, which is a ctypes wrapper — sits on it. This package calls
// the same ABI from Go through purego, which dlopens the library at runtime
// rather than linking it. That keeps CGO_ENABLED=0 cross-compilation to the
// Raspberry Pi working: the shared library is a file deployed next to the
// binary, not a build-time dependency.
package moonshine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	// headerVersion is the C API version this package was written against
	// (3.0.0). It is passed to the loader so a newer library emulates it, and
	// the library's own version is checked against it before any struct is
	// read.
	headerVersion = 30000

	// archMediumStreaming is MOONSHINE_MODEL_ARCH_MEDIUM_STREAMING, the model
	// the Python fallback used.
	archMediumStreaming = 5

	// sampleRate is what Moonshine works at internally.
	sampleRate = 16000
)

// errorNone is MOONSHINE_ERROR_NONE.
const errorNone = 0

// library holds the resolved entry points.
type library struct {
	getVersion      func() int32
	errorToString   func(int32) string
	loadFromFiles   func(path string, arch uint32, options uintptr, optionCount uint64, version int32) int32
	freeTranscriber func(int32)
	transcribe      func(handle int32, audio *float32, length uint64, rate int32, flags uint32, out *unsafe.Pointer) int32
	getDependencies func(language string, options uintptr, optionCount uint64, out *unsafe.Pointer) int32
	freeBuffer      func(unsafe.Pointer)
}

// Transcriber is a loaded Moonshine model. It is safe for concurrent use;
// the library serializes work on a handle anyway, and the returned transcript
// memory belongs to the transcriber until the next call, so calls are
// serialized here to keep that memory from being read after it is replaced.
type Transcriber struct {
	lib    *library
	handle int32

	mu     sync.Mutex
	closed bool
}

// Options configures where the library and model live.
type Options struct {
	// LibraryPath is the shared library to load. Empty means resolve it from
	// MOONSHINE_LIB, then a lib/ directory beside the binary, then the
	// platform's default search path.
	LibraryPath string

	// ModelDir holds the model files. Empty means DefaultModelDir.
	ModelDir string
}

// DefaultModelDir is where models are cached, relative to the working
// directory.
const DefaultModelDir = "models/moonshine"

// Open loads the library and the model. It is comparatively slow — the model
// is hundreds of megabytes — so callers should keep the result rather than
// opening one per transcription.
func Open(opts Options) (*Transcriber, error) {
	if abiMismatch != nil {
		return nil, abiMismatch
	}

	libPath, err := resolveLibrary(opts.LibraryPath)
	if err != nil {
		return nil, err
	}
	lib, err := openLibrary(libPath)
	if err != nil {
		return nil, err
	}

	// Refuse to touch the structs unless the library is the ABI they describe.
	// Without this a version change reads native memory at the wrong offsets,
	// which crashes rather than failing.
	if version := lib.getVersion(); version/10000 != headerVersion/10000 {
		return nil, fmt.Errorf(
			"moonshine library at %s reports version %s, but this build targets %s; "+
				"refusing to load rather than risk reading its structures wrongly",
			libPath, formatVersion(version), formatVersion(headerVersion))
	}

	modelDir := opts.ModelDir
	if modelDir == "" {
		modelDir = DefaultModelDir
	}
	if err := ensureModel(lib, modelDir); err != nil {
		return nil, err
	}

	handle := lib.loadFromFiles(modelDir, archMediumStreaming, 0, 0, headerVersion)
	if handle < 0 {
		return nil, fmt.Errorf("loading moonshine model from %s: %s",
			modelDir, lib.errorToString(handle))
	}

	slog.Info("moonshine model loaded",
		"library", libPath, "model_dir", modelDir, "version", formatVersion(lib.getVersion()))
	return &Transcriber{lib: lib, handle: handle}, nil
}

// Transcribe converts mono PCM samples in [-1, 1] into text. rate is the
// sample rate of the audio; Moonshine resamples internally, but capturing at
// 16 kHz avoids the work.
func (t *Transcriber) Transcribe(samples []float32, rate int) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("moonshine transcriber is closed")
	}

	var transcript unsafe.Pointer
	code := t.lib.transcribe(t.handle, &samples[0], uint64(len(samples)), int32(rate), 0, &transcript)
	// The samples must outlive the call; the library reads them directly.
	runtime.KeepAlive(samples)

	if code != errorNone {
		return "", fmt.Errorf("moonshine transcription failed: %s", t.lib.errorToString(code))
	}
	if transcript == nil {
		return "", nil
	}
	return readTranscript(transcript), nil
}

// Close releases the model.
func (t *Transcriber) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	t.lib.freeTranscriber(t.handle)
}

// readTranscript walks the returned transcript and joins its lines. The
// memory belongs to the transcriber and is only valid until its next call,
// so every string is copied out here.
func readTranscript(addr unsafe.Pointer) string {
	transcript := (*transcriptC)(addr)
	if transcript.lines == nil || transcript.lineCount == 0 {
		return ""
	}

	var out []byte
	for i := uint64(0); i < transcript.lineCount; i++ {
		line := (*transcriptLineC)(unsafe.Add(transcript.lines, uintptr(i)*sizeofTranscriptLine))
		text := trimSpace(goString(line.text))
		if text == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, ' ')
		}
		out = append(out, text...)
	}
	return string(out)
}

// goString copies a NUL-terminated C string into Go memory.
func goString(addr unsafe.Pointer) string {
	if addr == nil {
		return ""
	}
	var length int
	for *(*byte)(unsafe.Add(addr, length)) != 0 {
		length++
	}
	if length == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(addr), length))
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && isSpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func formatVersion(v int32) string {
	return fmt.Sprintf("%d.%d.%d", v/10000, v/100%100, v%100)
}

// openLibrary dlopens the shared library and binds the entry points used here.
func openLibrary(path string) (*library, error) {
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}

	lib := &library{}
	// A missing symbol panics inside purego, so a library that is the wrong
	// thing entirely surfaces here as an error rather than a crash.
	if err := bind(handle, lib); err != nil {
		return nil, err
	}
	return lib, nil
}

func bind(handle uintptr, lib *library) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("moonshine library is missing an expected symbol: %v", r)
		}
	}()

	purego.RegisterLibFunc(&lib.getVersion, handle, "moonshine_get_version")
	purego.RegisterLibFunc(&lib.errorToString, handle, "moonshine_error_to_string")
	purego.RegisterLibFunc(&lib.loadFromFiles, handle, "moonshine_load_transcriber_from_files")
	purego.RegisterLibFunc(&lib.freeTranscriber, handle, "moonshine_free_transcriber")
	purego.RegisterLibFunc(&lib.transcribe, handle, "moonshine_transcribe_without_streaming")
	purego.RegisterLibFunc(&lib.getDependencies, handle, "moonshine_get_stt_dependencies")
	purego.RegisterLibFunc(&lib.freeBuffer, handle, "moonshine_free_buffer")
	return nil
}

// resolveLibrary finds libmoonshine, preferring an explicit path, then an
// override, then a lib/ directory beside the binary, then the loader's own
// search path.
func resolveLibrary(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if fromEnv := os.Getenv("MOONSHINE_LIB"); fromEnv != "" {
		return fromEnv, nil
	}

	name := "libmoonshine.so"
	if runtime.GOOS == "darwin" {
		name = "libmoonshine.dylib"
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "lib", name),
			filepath.Join(dir, name))
	}
	candidates = append(candidates, filepath.Join("lib", name))

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	// Fall back to the bare name so the system loader gets a chance.
	return name, nil
}
