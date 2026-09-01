package moonshine

import (
	"errors"
	"unsafe"
)

// The layouts below mirror moonshine-c-api.h for the 64-bit ABI, and match
// the ctypes definitions the upstream Python binding uses. Getting a stride
// wrong here does not produce an error — it walks native memory at the wrong
// offset and segfaults — so the sizes are asserted at init against the same
// constants upstream asserts, and the library's own version is checked before
// any of these are read.
//
// See https://github.com/moonshine-ai/moonshine/issues/158.
const (
	sizeofTranscript     = 16
	sizeofTranscriptLine = 88
	sizeofSpeakerSpan    = 40
	sizeofTranscriptWord = 24
)

// transcriptC is struct transcript_t.
type transcriptC struct {
	lines     unsafe.Pointer // *transcript_line_t
	lineCount uint64
}

// transcriptLineC is struct transcript_line_t. Only text is read, but the
// whole layout is spelled out so the size assertion is meaningful: a struct
// that stops at the field of interest would keep its offset while silently
// getting the stride wrong.
type transcriptLineC struct {
	text                unsafe.Pointer // const char *
	audioData           unsafe.Pointer // const float *
	audioDataCount      uint64
	startTime           float32
	duration            float32
	id                  uint64
	isComplete          int8
	isUpdated           int8
	isNew               int8
	hasTextChanged      int8
	haveSpeakersChanged int8
	// Go inserts the same padding C does here.
	speakerSpans     unsafe.Pointer // const struct speaker_span_t *
	speakerSpanCount uint64
	lastLatencyMs    uint32
	words            unsafe.Pointer // const struct transcript_word_t *
	wordCount        uint64
}

// speakerSpanC is struct speaker_span_t, present for the size check only.
type speakerSpanC struct {
	startTime    float32
	duration     float32
	speakerID    uint64
	speakerIndex uint32
	startChar    uint64
	endChar      uint64
}

// transcriptWordC is struct transcript_word_t, present for the size check only.
type transcriptWordC struct {
	text       unsafe.Pointer
	start      float32
	end        float32
	confidence float32
}

// abiMismatch is set at init when a struct does not match the compiled C ABI,
// and turns every call into a clean error instead of a crash.
var abiMismatch error

func init() {
	// These sizes are for the LP64 layout. On a platform with narrower
	// pointers the real layout differs, and since the sizes below would not
	// catch that, the safe answer is to refuse rather than walk native memory
	// with strides derived for a different ABI. (32-bit ARM reaches here.)
	if unsafe.Sizeof(uintptr(0)) != 8 {
		abiMismatch = errors.New(
			"moonshine bindings support 64-bit platforms only; this build has " +
				itoa(uint64(unsafe.Sizeof(uintptr(0)))) + "-byte pointers")
		return
	}

	for _, check := range []struct {
		name     string
		actual   uintptr
		expected uintptr
	}{
		{"transcript_t", unsafe.Sizeof(transcriptC{}), sizeofTranscript},
		{"transcript_line_t", unsafe.Sizeof(transcriptLineC{}), sizeofTranscriptLine},
		{"speaker_span_t", unsafe.Sizeof(speakerSpanC{}), sizeofSpeakerSpan},
		{"transcript_word_t", unsafe.Sizeof(transcriptWordC{}), sizeofTranscriptWord},
	} {
		if check.actual != check.expected {
			abiMismatch = &abiError{check.name, uint64(check.actual), uint64(check.expected)}
			return
		}
	}

	if offset := unsafe.Offsetof(transcriptLineC{}.text); offset != 0 {
		abiMismatch = &abiError{"transcript_line_t.text offset", uint64(offset), 0}
	}
}

type abiError struct {
	what     string
	actual   uint64
	expected uint64
}

func (e *abiError) Error() string {
	return "moonshine ABI mismatch: " + e.what + " is " +
		itoa(e.actual) + " bytes, expected " + itoa(e.expected)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
