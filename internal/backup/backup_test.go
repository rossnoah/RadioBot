package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
)

// fakeStore records backup bookkeeping in memory.
type fakeStore struct {
	mu       sync.Mutex
	uploaded map[string]int64
	markers  map[string]time.Time
	snapshot func(path string) error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		uploaded: map[string]int64{},
		markers:  map[string]time.Time{},
		snapshot: func(path string) error { return os.WriteFile(path, []byte("snapshot"), 0o600) },
	}
}

func (f *fakeStore) UploadedPaths() (map[string]struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]struct{}, len(f.uploaded))
	for path := range f.uploaded {
		out[path] = struct{}{}
	}
	return out, nil
}

func (f *fakeStore) MarkUploaded(path string, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploaded[path] = size
	f.markers[path] = time.Now()
	return nil
}

func (f *fakeStore) LastMarkerTime(marker string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.markers[marker]
}

func (f *fakeStore) SnapshotTo(path string) error { return f.snapshot(path) }

// stubBackend stands in for the backup Lambda plus the S3 bucket it presigns.
type stubBackend struct {
	mu sync.Mutex

	server *httptest.Server
	secret string

	presignRequests []map[string]any
	uploadedKeys    []string
	quotaExceeded   bool
	unauthorized    bool
}

func newStubBackend(t *testing.T, secret string) *stubBackend {
	t.Helper()
	backend := &stubBackend{secret: secret}

	mux := http.NewServeMux()
	// The presign endpoint (the Lambda's function URL).
	mux.HandleFunc("/presign", func(w http.ResponseWriter, r *http.Request) {
		backend.mu.Lock()
		defer backend.mu.Unlock()

		if r.Header.Get("x-backup-secret") != backend.secret || backend.unauthorized {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if backend.quotaExceeded {
			http.Error(w, "quota exceeded", http.StatusTooManyRequests)
			return
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		backend.presignRequests = append(backend.presignRequests, body)

		json.NewEncoder(w).Encode(presignResponse{
			URL:    backend.server.URL + "/s3",
			Fields: map[string]string{"key": body["key"].(string), "policy": "abc"},
		})
	})
	// The presigned S3 POST target.
	mux.HandleFunc("/s3", func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var key string
		var sawFile bool
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			switch {
			case part.FormName() == "key":
				value, _ := io.ReadAll(part)
				key = string(value)
			case part.FormName() == "file":
				sawFile = true
				io.Copy(io.Discard, part)
			}
		}
		if !sawFile {
			http.Error(w, "no file part", http.StatusBadRequest)
			return
		}

		backend.mu.Lock()
		backend.uploadedKeys = append(backend.uploadedKeys, key)
		backend.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	backend.server = httptest.NewServer(mux)
	t.Cleanup(backend.server.Close)
	return backend
}

func (b *stubBackend) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.uploadedKeys...)
}

// inTempDir runs the test in a scratch working directory, since the backup
// service resolves files/ and config.yaml relative to it.
func inTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(original) })
	return dir
}

func writeRecording(t *testing.T, date, name string) string {
	t.Helper()
	dir := filepath.Join(filesFolder, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("audio bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newService(backend *stubBackend, store Store) *Service {
	return New(config.Backup{
		Enabled:                 true,
		EndpointURL:             backend.server.URL + "/presign",
		Secret:                  backend.secret,
		ScanIntervalSeconds:     60,
		DBSnapshotIntervalHours: 24,
	}, store)
}

func TestNewReturnsNilWhenDisabled(t *testing.T) {
	if service := New(config.Backup{Enabled: false}, newFakeStore()); service != nil {
		t.Error("backup service was created while disabled")
	}
	if service := New(config.Backup{Enabled: true}, newFakeStore()); service != nil {
		t.Error("backup service was created without an endpoint or secret")
	}
	// Run on a nil service must be safe; the server calls it unconditionally.
	var nilService *Service
	nilService.Run(context.Background())
}

func TestScanUploadsRecordingsOnce(t *testing.T) {
	inTempDir(t)
	first := writeRecording(t, "20260408", "20260408_120000_1_SRC_1.wav")
	writeRecording(t, "20260409", "20260409_130000_2_SRC_2.wav")

	backend := newStubBackend(t, "s3cret")
	store := newFakeStore()
	service := newService(backend, store)

	if err := service.scanRecordings(); err != nil {
		t.Fatalf("scanRecordings: %v", err)
	}

	keys := backend.keys()
	if len(keys) != 2 {
		t.Fatalf("uploaded %v, want 2 recordings", keys)
	}
	if keys[0] != "recordings/20260408/20260408_120000_1_SRC_1.wav" {
		t.Errorf("remote key = %q", keys[0])
	}
	if _, ok := store.uploaded[first]; !ok {
		t.Error("upload was not recorded in the store")
	}

	// A second scan must not re-upload anything.
	if err := service.scanRecordings(); err != nil {
		t.Fatal(err)
	}
	if len(backend.keys()) != 2 {
		t.Errorf("second scan uploaded again: %v", backend.keys())
	}
}

// TestScanStopsOnQuotaExceeded checks that a 429 ends the round without
// marking anything uploaded, so the files are retried next scan.
func TestScanStopsOnQuotaExceeded(t *testing.T) {
	inTempDir(t)
	for i := 0; i < 3; i++ {
		writeRecording(t, "20260408", string(rune('a'+i))+"_20260408_1_SRC_1.wav")
	}

	backend := newStubBackend(t, "s3cret")
	backend.quotaExceeded = true
	store := newFakeStore()
	service := newService(backend, store)

	if err := service.scanRecordings(); err != nil {
		t.Fatalf("scanRecordings returned an error on quota exhaustion: %v", err)
	}
	if len(backend.keys()) != 0 {
		t.Errorf("uploaded %v despite the quota being exhausted", backend.keys())
	}
	if len(store.uploaded) != 0 {
		t.Errorf("marked %d uploads despite the quota being exhausted", len(store.uploaded))
	}

	// Once the quota resets, the same files go up.
	backend.mu.Lock()
	backend.quotaExceeded = false
	backend.mu.Unlock()

	if err := service.scanRecordings(); err != nil {
		t.Fatal(err)
	}
	if len(backend.keys()) != 3 {
		t.Errorf("uploaded %v after the quota reset, want 3", backend.keys())
	}
}

// TestScanContinuesPastAFailedUpload keeps one bad file from blocking the rest.
func TestScanContinuesPastAFailedUpload(t *testing.T) {
	inTempDir(t)
	writeRecording(t, "20260408", "a_20260408_1_SRC_1.wav")
	writeRecording(t, "20260408", "b_20260408_1_SRC_2.wav")

	backend := newStubBackend(t, "s3cret")
	store := newFakeStore()
	service := newService(backend, store)

	// Reject the first presign only.
	var rejected bool
	original := service.presignClient.Transport
	service.presignClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !rejected {
			rejected = true
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("boom")),
				Header:     make(http.Header),
			}, nil
		}
		if original != nil {
			return original.RoundTrip(r)
		}
		return http.DefaultTransport.RoundTrip(r)
	})

	if err := service.scanRecordings(); err != nil {
		t.Fatal(err)
	}
	if len(backend.keys()) != 1 {
		t.Errorf("uploaded %v, want the one file that did not fail", backend.keys())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestScanSkipsNonDateFoldersAndNonWAV(t *testing.T) {
	inTempDir(t)
	writeRecording(t, "20260408", "keep_20260408_1_SRC_1.wav")
	writeRecording(t, "notadate", "skip_1_SRC_1.wav")
	writeRecording(t, "20260408", "notes.txt")

	backend := newStubBackend(t, "s3cret")
	service := newService(backend, newFakeStore())

	if err := service.scanRecordings(); err != nil {
		t.Fatal(err)
	}
	keys := backend.keys()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], "keep_20260408_1_SRC_1.wav") {
		t.Errorf("uploaded %v, want only the WAV in a date folder", keys)
	}
}

func TestSnapshotDBRespectsInterval(t *testing.T) {
	inTempDir(t)
	backend := newStubBackend(t, "s3cret")
	store := newFakeStore()
	service := newService(backend, store)

	if err := service.snapshotDB(); err != nil {
		t.Fatalf("snapshotDB: %v", err)
	}
	keys := backend.keys()
	if len(keys) != 1 || keys[0] != "db/transcripts.db.gz" {
		t.Fatalf("uploaded %v, want the database snapshot", keys)
	}
	if store.markers[dbSnapshotMarker].IsZero() {
		t.Error("snapshot marker was not recorded")
	}
	// The temporary snapshot files must be cleaned up.
	for _, name := range []string{"db_snapshot.db", "db_snapshot.db.gz"} {
		if _, err := os.Stat(filepath.Join("temp", name)); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", name)
		}
	}

	// A second call inside the interval must be a no-op.
	if err := service.snapshotDB(); err != nil {
		t.Fatal(err)
	}
	if len(backend.keys()) != 1 {
		t.Errorf("snapshotted again inside the interval: %v", backend.keys())
	}
}

func TestBackupConfigOnlyWhenChanged(t *testing.T) {
	inTempDir(t)
	if err := os.WriteFile(configFile, []byte("application:\n  password: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := newStubBackend(t, "s3cret")
	store := newFakeStore()
	service := newService(backend, store)

	if err := service.backupConfig(); err != nil {
		t.Fatalf("backupConfig: %v", err)
	}
	if keys := backend.keys(); len(keys) != 1 || keys[0] != "config/config.yaml" {
		t.Fatalf("uploaded %v, want the config", keys)
	}

	// Unchanged config: no second upload.
	if err := service.backupConfig(); err != nil {
		t.Fatal(err)
	}
	if len(backend.keys()) != 1 {
		t.Errorf("re-uploaded an unchanged config: %v", backend.keys())
	}

	// Touch the file into the future and it goes up again.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(configFile, future, future); err != nil {
		t.Fatal(err)
	}
	if err := service.backupConfig(); err != nil {
		t.Fatal(err)
	}
	if len(backend.keys()) != 2 {
		t.Errorf("a changed config was not re-uploaded: %v", backend.keys())
	}
}

func TestBackupConfigMissingFileIsNotAnError(t *testing.T) {
	inTempDir(t)
	backend := newStubBackend(t, "s3cret")
	if err := newService(backend, newFakeStore()).backupConfig(); err != nil {
		t.Errorf("backupConfig with no config.yaml: %v", err)
	}
}

// TestPresignSendsSecretAndSize covers the contract with the Lambda.
func TestPresignSendsSecretAndSize(t *testing.T) {
	inTempDir(t)
	writeRecording(t, "20260408", "a_20260408_1_SRC_1.wav") // 11 bytes

	backend := newStubBackend(t, "s3cret")
	if err := newService(backend, newFakeStore()).scanRecordings(); err != nil {
		t.Fatal(err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.presignRequests) != 1 {
		t.Fatalf("made %d presign requests, want 1", len(backend.presignRequests))
	}
	request := backend.presignRequests[0]
	if size, ok := request["size"].(float64); !ok || int64(size) != 11 {
		t.Errorf("presign size = %v, want 11", request["size"])
	}
}

func TestWrongSecretIsRejected(t *testing.T) {
	inTempDir(t)
	writeRecording(t, "20260408", "a_20260408_1_SRC_1.wav")

	backend := newStubBackend(t, "s3cret")
	store := newFakeStore()
	service := New(config.Backup{
		Enabled: true, EndpointURL: backend.server.URL + "/presign", Secret: "wrong",
		ScanIntervalSeconds: 60, DBSnapshotIntervalHours: 24,
	}, store)

	if err := service.scanRecordings(); err != nil {
		t.Fatalf("scanRecordings: %v", err)
	}
	if len(store.uploaded) != 0 {
		t.Error("an upload was recorded despite the wrong secret")
	}
}

// TestMultipartOrdering checks that the presigned form fields precede the file
// part, which S3 requires.
func TestMultipartOrdering(t *testing.T) {
	inTempDir(t)
	path := writeRecording(t, "20260408", "a_20260408_1_SRC_1.wav")

	var order []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Errorf("MultipartReader: %v", err)
			return
		}
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			order = append(order, part.FormName())
			io.Copy(io.Discard, part)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service := New(config.Backup{
		Enabled: true, EndpointURL: "unused", Secret: "x",
		ScanIntervalSeconds: 60, DBSnapshotIntervalHours: 24,
	}, newFakeStore())

	presigned := &presignResponse{URL: server.URL, Fields: map[string]string{"key": "k", "policy": "p"}}
	if err := service.postFile(presigned, path); err != nil {
		t.Fatalf("postFile: %v", err)
	}
	if len(order) == 0 || order[len(order)-1] != "file" {
		t.Errorf("multipart part order = %v, want the file part last", order)
	}
}
