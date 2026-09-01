package moonshine

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unsafe"
)

// The library knows which files a model needs and where they live, so the
// manifest is asked for rather than hardcoded. Only the fetching is done here.

const (
	modelLanguage   = "en"
	downloadTimeout = 30 * time.Minute
)

// manifest is the JSON returned by moonshine_get_stt_dependencies.
type manifest struct {
	Groups []struct {
		BaseURL string `json:"base_url"`
		Files   []struct {
			Name         string `json:"name"`
			URL          string `json:"url"`
			Size         *int64 `json:"size"`
			Checksum     string `json:"checksum"`
			ChecksumType string `json:"checksum_type"`
		} `json:"files"`
	} `json:"groups"`
}

// ensureModel downloads whatever the model needs that is not already on disk.
func ensureModel(lib *library, dir string) error {
	files, err := modelManifest(lib)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating model directory %s: %w", dir, err)
	}

	var missing []manifestFile
	for _, file := range files {
		if !isPresent(filepath.Join(dir, file.name), file.size) {
			missing = append(missing, file)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	slog.Info("downloading moonshine model files", "count", len(missing), "dir", dir)
	client := &http.Client{Timeout: downloadTimeout}
	for _, file := range missing {
		if err := download(client, file, filepath.Join(dir, file.name)); err != nil {
			return fmt.Errorf("downloading %s: %w", file.name, err)
		}
		slog.Info("downloaded moonshine model file", "name", file.name)
	}
	return nil
}

type manifestFile struct {
	name         string
	url          string
	size         int64
	checksum     string
	checksumType string
}

// modelManifest asks the library which files this model needs.
func modelManifest(lib *library) ([]manifestFile, error) {
	var out unsafe.Pointer
	if code := lib.getDependencies(modelLanguage, 0, 0, &out); code != errorNone {
		return nil, fmt.Errorf("reading the moonshine model manifest: %s", lib.errorToString(code))
	}
	if out == nil {
		return nil, fmt.Errorf("the moonshine model manifest was empty")
	}
	raw := goString(out)
	lib.freeBuffer(out)

	var parsed manifest
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("decoding the moonshine model manifest: %w", err)
	}

	var files []manifestFile
	for _, group := range parsed.Groups {
		for _, file := range group.Files {
			url := file.URL
			if url == "" && group.BaseURL != "" {
				url = group.BaseURL + "/" + file.Name
			}
			entry := manifestFile{
				name:         file.Name,
				url:          url,
				checksum:     file.Checksum,
				checksumType: file.ChecksumType,
			}
			if file.Size != nil {
				entry.size = *file.Size
			}
			files = append(files, entry)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the moonshine model manifest listed no files")
	}
	return files, nil
}

// isPresent reports whether a model file is already downloaded. A known size
// that does not match means a truncated download, which is worth redoing.
func isPresent(path string, size int64) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return size <= 0 || info.Size() == size
}

// download fetches one file, verifies it, and only then moves it into place,
// so an interrupted download never leaves a half-file that looks complete.
func download(client *http.Client, file manifestFile, dest string) error {
	if file.url == "" {
		return fmt.Errorf("no download URL")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	resp, err := client.Get(file.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has happened

	digest := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	written, err := io.Copy(io.MultiWriter(tmp, digest), resp.Body)
	if err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if file.size > 0 && written != file.size {
		return fmt.Errorf("expected %d bytes, got %d", file.size, written)
	}
	if err := verifyChecksum(file, digest.Sum32()); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// verifyChecksum checks a crc32c digest when the manifest supplies one. An
// unrecognised checksum type is skipped rather than treated as a failure, so
// a future manifest format does not block downloads.
func verifyChecksum(file manifestFile, actual uint32) error {
	if file.checksum == "" || file.checksumType != "crc32c" {
		if file.checksum != "" {
			slog.Debug("skipping an unrecognised model checksum type",
				"name", file.name, "type", file.checksumType)
		}
		return nil
	}

	raw, err := base64.StdEncoding.DecodeString(file.checksum)
	if err != nil || len(raw) != 4 {
		slog.Debug("skipping an unreadable model checksum", "name", file.name)
		return nil
	}
	if expected := binary.BigEndian.Uint32(raw); expected != actual {
		return fmt.Errorf("checksum mismatch: expected %08x, got %08x", expected, actual)
	}
	return nil
}
