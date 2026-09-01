// Package wavutil reads and writes the small subset of the WAV format this
// application needs, replacing Python's `wave` module.
package wavutil

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

var errNotWAV = errors.New("not a RIFF/WAVE file")

// Duration returns the length of a PCM WAV file in seconds. Callers that
// treat an unreadable file as zero-length (the file-length filter, the DB
// backfill) can ignore the error.
func Duration(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var riff struct {
		ID   [4]byte
		Size uint32
		Type [4]byte
	}
	if err := binary.Read(f, binary.LittleEndian, &riff); err != nil {
		return 0, err
	}
	if string(riff.ID[:]) != "RIFF" || string(riff.Type[:]) != "WAVE" {
		return 0, errNotWAV
	}

	var byteRate uint32
	for {
		var header struct {
			ID   [4]byte
			Size uint32
		}
		if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return 0, errors.New("wav file has no data chunk")
			}
			return 0, err
		}

		switch string(header.ID[:]) {
		case "fmt ":
			// The fields we need sit at a fixed offset in every fmt variant.
			var fmtChunk struct {
				AudioFormat   uint16
				NumChannels   uint16
				SampleRate    uint32
				ByteRate      uint32
				BlockAlign    uint16
				BitsPerSample uint16
			}
			if err := binary.Read(f, binary.LittleEndian, &fmtChunk); err != nil {
				return 0, err
			}
			byteRate = fmtChunk.ByteRate
			if byteRate == 0 {
				byteRate = fmtChunk.SampleRate * uint32(fmtChunk.BlockAlign)
			}
			// Skip any bytes of this chunk we did not read.
			if remaining := int64(header.Size) - 16; remaining > 0 {
				if _, err := f.Seek(remaining, io.SeekCurrent); err != nil {
					return 0, err
				}
			}
		case "data":
			if byteRate == 0 {
				return 0, errors.New("wav data chunk precedes fmt chunk")
			}
			return float64(header.Size) / float64(byteRate), nil
		default:
			// Chunks are word-aligned, so an odd size carries a pad byte.
			skip := int64(header.Size) + int64(header.Size%2)
			if _, err := f.Seek(skip, io.SeekCurrent); err != nil {
				return 0, err
			}
		}
	}
}

// WriteSilence creates a mono 16-bit PCM WAV file of the given duration,
// used by the test console to stand in for a real transmission.
func WriteSilence(path string, sampleRate int, seconds float64) error {
	frames := int(float64(sampleRate) * seconds)
	if frames < 0 {
		frames = 0
	}
	const (
		channels      = 1
		bitsPerSample = 16
	)
	blockAlign := channels * bitsPerSample / 8
	dataSize := frames * blockAlign

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	write := func(values ...any) error {
		for _, v := range values {
			if err := binary.Write(f, binary.LittleEndian, v); err != nil {
				return err
			}
		}
		return nil
	}

	if err := write(
		[]byte("RIFF"), uint32(36+dataSize), []byte("WAVE"),
		[]byte("fmt "), uint32(16),
		uint16(1), uint16(channels), uint32(sampleRate),
		uint32(sampleRate*blockAlign), uint16(blockAlign), uint16(bitsPerSample),
		[]byte("data"), uint32(dataSize),
	); err != nil {
		return fmt.Errorf("writing wav header: %w", err)
	}

	// Silence is just zeroed samples; write them in chunks rather than
	// allocating the whole buffer.
	const chunk = 32 * 1024
	zeros := make([]byte, chunk)
	for remaining := dataSize; remaining > 0; {
		n := min(remaining, chunk)
		if _, err := f.Write(zeros[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}
