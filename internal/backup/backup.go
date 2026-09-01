// Package backup uploads recordings, database snapshots, and the config file
// to S3 via presigned URLs issued by the backup Lambda (see infra/). The
// device holds only a shared secret, never AWS credentials.
//
// Design constraints:
//   - Must never block or break primary operation: everything runs in one
//     goroutine, every failure is logged and swallowed, and a failed upload is
//     simply retried on a later scan.
//   - A periodic scanner (rather than a hook in the recording pipeline)
//     handles backfill of historical files, new files, and retries through the
//     same code path.
package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
)

const (
	filesFolder = "files"
	configFile  = "config.yaml"

	// Marker rows stand in for uploads that recur on a fixed key, where the
	// per-path upload table cannot track "when did this last happen".
	dbSnapshotMarker   = "__db_snapshot__"
	configBackupMarker = "__config_backup__"

	requestTimeout = 30 * time.Second
	uploadTimeout  = 300 * time.Second
	maxBackoff     = 15 * time.Minute
)

// errQuotaExceeded reports that the backend has exhausted its daily upload
// quota, so the rest of this scan should be abandoned.
var errQuotaExceeded = errors.New("daily upload quota exceeded")

// Store is the subset of the database the backup service uses.
type Store interface {
	UploadedPaths() (map[string]struct{}, error)
	MarkUploaded(path string, size int64) error
	LastMarkerTime(marker string) time.Time
	SnapshotTo(path string) error
}

// Service periodically mirrors local state to S3.
type Service struct {
	endpointURL        string
	secret             string
	scanInterval       time.Duration
	dbSnapshotInterval time.Duration
	store              Store
	presignClient      *http.Client
	uploadClient       *http.Client
}

// New returns a backup service, or nil if backup is disabled or misconfigured.
// A nil service is not an error — backup is opt-in.
func New(cfg config.Backup, store Store) *Service {
	if !cfg.Enabled {
		slog.Info("S3 backup disabled")
		return nil
	}
	if cfg.EndpointURL == "" || cfg.Secret == "" {
		slog.Warn("backup is enabled but endpoint_url/secret are not set; backup disabled")
		return nil
	}
	return &Service{
		endpointURL:        cfg.EndpointURL,
		secret:             cfg.Secret,
		scanInterval:       time.Duration(cfg.ScanIntervalSeconds) * time.Second,
		dbSnapshotInterval: time.Duration(cfg.DBSnapshotIntervalHours) * time.Hour,
		store:              store,
		presignClient:      &http.Client{Timeout: requestTimeout},
		uploadClient:       &http.Client{Timeout: uploadTimeout},
	}
}

// Run drives the backup loop until ctx is cancelled. It is safe to call on a
// nil service, which does nothing.
func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	slog.Info("S3 backup started",
		"scan_interval", s.scanInterval, "db_snapshot_interval", s.dbSnapshotInterval)

	consecutiveFailures := 0
	for {
		if err := s.scanOnce(); err != nil {
			consecutiveFailures++
			slog.Warn("backup scan failed", "consecutive_failures", consecutiveFailures, "error", err)
		} else {
			consecutiveFailures = 0
		}

		// Back off when the backend is unreachable, up to maxBackoff.
		delay := s.scanInterval << min(consecutiveFailures, 4)
		if delay > maxBackoff {
			delay = maxBackoff
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) scanOnce() error {
	if err := s.scanRecordings(); err != nil {
		return err
	}
	if err := s.snapshotDB(); err != nil {
		return err
	}
	return s.backupConfig()
}

// scanRecordings uploads any recordings not yet backed up, historical or new.
func (s *Service) scanRecordings() error {
	info, err := os.Stat(filesFolder)
	if err != nil || !info.IsDir() {
		return nil
	}

	uploaded, err := s.store.UploadedPaths()
	if err != nil {
		return fmt.Errorf("reading uploaded paths: %w", err)
	}

	type pendingUpload struct{ localPath, remoteKey string }
	var pending []pendingUpload

	dateFolders, err := os.ReadDir(filesFolder)
	if err != nil {
		return err
	}
	sort.Slice(dateFolders, func(i, j int) bool { return dateFolders[i].Name() < dateFolders[j].Name() })

	for _, dateFolder := range dateFolders {
		name := dateFolder.Name()
		if !dateFolder.IsDir() || !isDateFolder(name) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(filesFolder, name))
		if err != nil {
			slog.Warn("could not read recordings folder", "folder", name, "error", err)
			continue
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })

		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".wav") {
				continue
			}
			localPath := filepath.Join(filesFolder, name, file.Name())
			if _, done := uploaded[localPath]; done {
				continue
			}
			pending = append(pending, pendingUpload{
				localPath: localPath,
				remoteKey: fmt.Sprintf("recordings/%s/%s", name, file.Name()),
			})
		}
	}

	if len(pending) == 0 {
		return nil
	}
	slog.Info("backup: recordings to upload", "count", len(pending))

	count := 0
	for _, item := range pending {
		err := s.upload(item.localPath, item.remoteKey, true)
		if errors.Is(err, errQuotaExceeded) {
			slog.Warn("backup: daily upload quota exceeded, pausing until next scan")
			break
		}
		if err != nil {
			slog.Warn("backup: error uploading recording", "path", item.localPath, "error", err)
			continue
		}
		count++
	}
	if count > 0 {
		slog.Info("backup: uploaded recordings", "count", count)
	}
	return nil
}

// snapshotDB takes a consistent database copy, gzips it, and uploads it to a
// fixed key; bucket versioning preserves history.
func (s *Service) snapshotDB() error {
	if time.Since(s.store.LastMarkerTime(dbSnapshotMarker)) < s.dbSnapshotInterval {
		return nil
	}

	if err := os.MkdirAll("temp", 0o755); err != nil {
		return err
	}
	snapshotPath := filepath.Join("temp", "db_snapshot.db")
	gzipPath := snapshotPath + ".gz"
	defer func() {
		for _, path := range []string{snapshotPath, gzipPath} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Warn("could not clean up snapshot file", "path", path, "error", err)
			}
		}
	}()

	// VACUUM INTO needs the destination not to exist.
	if err := os.Remove(snapshotPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.store.SnapshotTo(snapshotPath); err != nil {
		return fmt.Errorf("taking database snapshot: %w", err)
	}
	if err := gzipFile(snapshotPath, gzipPath); err != nil {
		return fmt.Errorf("compressing database snapshot: %w", err)
	}

	// track=false: snapshots recur on a fixed key, tracked via the marker row.
	if err := s.upload(gzipPath, "db/transcripts.db.gz", false); err != nil {
		return err
	}
	info, err := os.Stat(gzipPath)
	if err != nil {
		return err
	}
	if err := s.store.MarkUploaded(dbSnapshotMarker, info.Size()); err != nil {
		return err
	}
	slog.Info("backup: uploaded database snapshot")
	return nil
}

// backupConfig uploads config.yaml whenever it has changed since the last
// upload, to a fixed key; bucket versioning preserves history.
func (s *Service) backupConfig() error {
	info, err := os.Stat(configFile)
	if err != nil {
		return nil
	}
	if !info.ModTime().After(s.store.LastMarkerTime(configBackupMarker)) {
		return nil
	}

	if err := s.upload(configFile, "config/config.yaml", false); err != nil {
		return err
	}
	if err := s.store.MarkUploaded(configBackupMarker, info.Size()); err != nil {
		return err
	}
	slog.Info("backup: uploaded config.yaml")
	return nil
}

// presignResponse is what the backup Lambda returns: an S3 presigned POST.
type presignResponse struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

// upload sends one file through a presigned POST. track records the upload in
// the per-path table; callers using a fixed remote key pass false and use a
// marker row instead.
func (s *Service) upload(localPath, remoteKey string, track bool) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	size := info.Size()

	presigned, err := s.presign(remoteKey, size)
	if err != nil {
		return err
	}

	if err := s.postFile(presigned, localPath); err != nil {
		return err
	}
	if track {
		return s.store.MarkUploaded(localPath, size)
	}
	return nil
}

func (s *Service) presign(remoteKey string, size int64) (*presignResponse, error) {
	body, err := json.Marshal(map[string]any{"key": remoteKey, "size": size})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, s.endpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-backup-secret", s.secret)

	resp, err := s.presignClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errQuotaExceeded
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("presign failed (%d) for %s: %s",
			resp.StatusCode, remoteKey, truncate(string(respBody), 200))
	}

	var presigned presignResponse
	if err := json.Unmarshal(respBody, &presigned); err != nil {
		return nil, fmt.Errorf("decoding presign response: %w", err)
	}
	return &presigned, nil
}

// postFile performs the presigned S3 POST. The form fields must precede the
// file part, which is what S3 requires.
func (s *Service) postFile(presigned *presignResponse, localPath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Stream the body rather than buffering the whole recording in memory.
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		var err error
		defer func() { pw.CloseWithError(err) }()

		for name, value := range presigned.Fields {
			if err = writer.WriteField(name, value); err != nil {
				return
			}
		}
		var part io.Writer
		if part, err = writer.CreateFormFile("file", filepath.Base(localPath)); err != nil {
			return
		}
		if _, err = io.Copy(part, file); err != nil {
			return
		}
		err = writer.Close()
	}()

	req, err := http.NewRequest(http.MethodPost, presigned.URL, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}

func isDateFolder(name string) bool {
	if len(name) != 8 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
