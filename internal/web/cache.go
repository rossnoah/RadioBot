package web

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// responseCache reuses a rendered page for a short TTL. The index and status
// pages both walk the recordings tree, which is the expensive part.
type responseCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	status  int
	header  http.Header
	body    []byte
	written time.Time
}

func newResponseCache() *responseCache {
	return &responseCache{entries: make(map[string]cacheEntry)}
}

// cached wraps a handler so its response is reused for pageCacheTTL. Only
// successful responses are cached, so an error is retried on the next request.
func (s *Server) cached(key string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if entry, ok := s.cache.get(key); ok {
			writeEntry(w, entry)
			return
		}

		recorder := &responseRecorder{header: make(http.Header), status: http.StatusOK}
		next(recorder, r)

		if recorder.status == http.StatusOK {
			s.cache.put(key, cacheEntry{
				status:  recorder.status,
				header:  recorder.header.Clone(),
				body:    recorder.body.Bytes(),
				written: time.Now(),
			})
		}
		writeEntry(w, cacheEntry{status: recorder.status, header: recorder.header, body: recorder.body.Bytes()})
	}
}

func (c *responseCache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.written) >= pageCacheTTL {
		return cacheEntry{}, false
	}
	return entry, true
}

func (c *responseCache) put(key string, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
}

func writeEntry(w http.ResponseWriter, entry cacheEntry) {
	for name, values := range entry.header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(entry.status)
	_, _ = w.Write(entry.body)
}

// responseRecorder buffers a handler's response so it can be cached.
type responseRecorder struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	r.wroteHeader = true
	return r.body.Write(p)
}
