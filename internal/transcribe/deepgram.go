// Package transcribe turns recordings into text, using Deepgram with an
// on-device Moonshine fallback.
package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	deepgramURL     = "https://api.deepgram.com/v1/listen?model=nova-3&smart_format=true"
	deepgramTimeout = 300 * time.Second
	connectTimeout  = 10 * time.Second
)

// deepgramClient posts audio to Deepgram's prerecorded endpoint. The Python
// version used the SDK; a single POST is all it did, so this drops the
// dependency.
type deepgramClient struct {
	apiKey string
	url    string
	http   *http.Client
}

func newDeepgramClient(apiKey string) *deepgramClient {
	return &deepgramClient{
		apiKey: apiKey,
		url:    deepgramURL,
		http: &http.Client{
			Timeout: deepgramTimeout,
			Transport: &http.Transport{
				// Matches httpx.Timeout(300.0, connect=10.0).
				TLSHandshakeTimeout:   connectTimeout,
				ResponseHeaderTimeout: deepgramTimeout,
			},
		},
	}
}

// deepgramResponse is the slice of the response we read; the full body is
// stored in the database verbatim.
type deepgramResponse struct {
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string `json:"transcript"`
			} `json:"alternatives"`
		} `json:"channels"`
	} `json:"results"`
}

// transcribe returns the transcript text and the raw JSON response body.
func (c *deepgramClient) transcribe(ctx context.Context, filePath string) (string, string, error) {
	audio, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(audio))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Token "+c.apiKey)
	req.Header.Set("Content-Type", "audio/wav")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("deepgram returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var parsed deepgramResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("decoding deepgram response: %w", err)
	}
	if len(parsed.Results.Channels) == 0 || len(parsed.Results.Channels[0].Alternatives) == 0 {
		return "", "", fmt.Errorf("deepgram response contained no alternatives")
	}
	return parsed.Results.Channels[0].Alternatives[0].Transcript, string(body), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
