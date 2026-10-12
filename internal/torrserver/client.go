package torrserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TorrentFile represents a file inside a TorrServer torrent.
type TorrentFile struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// TorrentData represents the inner JSON data string of a TorrServer item.
type TorrentData struct {
	TorrServer struct {
		Files []TorrentFile `json:"Files"`
	} `json:"TorrServer"`
}

// Torrent represents an item returned from TorrServer.
type Torrent struct {
	Hash        string `json:"hash"`
	Title       string `json:"title"`
	Poster      string `json:"poster"`
	TorrentSize int64  `json:"torrent_size"`
	Size        int64  `json:"size"`
	Length      int64  `json:"length"`
	Data        string `json:"data"`
	Magnet      string `json:"magnet"`
}

// GetTotalSize returns the size in bytes of the torrent.
func (t *Torrent) GetTotalSize() int64 {
	if t.TorrentSize > 0 {
		return t.TorrentSize
	}
	if t.Size > 0 {
		return t.Size
	}
	return t.Length
}

// ParseFiles extracts file details from the embedded JSON in `Data`.
func (t *Torrent) ParseFiles() []TorrentFile {
	if t.Data == "" {
		return nil
	}
	var td TorrentData
	if err := json.Unmarshal([]byte(t.Data), &td); err == nil {
		return td.TorrServer.Files
	}
	return nil
}

// Client interacts with TorrServer.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// NewClient creates a new TorrServer client.
func NewClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// ListTorrents returns all active torrents in TorrServer.
func (c *Client) ListTorrents(ctx context.Context) ([]Torrent, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("TorrServer URL is not set")
	}

	payload := map[string]string{"action": "list"}
	bodyBytes, _ := json.Marshal(payload)

	reqURL := c.baseURL + "/torrents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TorrServer HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var torrents []Torrent
	if err := json.NewDecoder(resp.Body).Decode(&torrents); err != nil {
		return nil, fmt.Errorf("failed to decode TorrServer response: %w", err)
	}

	return torrents, nil
}
