package metube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AddRequest represents the JSON payload expected by MeTube's /add endpoint.
type AddRequest struct {
	URL              string `json:"url"`
	Quality          string `json:"quality,omitempty"`
	Format           string `json:"format,omitempty"`
	Folder           string `json:"folder,omitempty"`
	CustomNamePrefix string `json:"custom_name_prefix,omitempty"`
	AutoStart        bool   `json:"auto_start"`
}

// Client interacts with the MeTube web server API.
type Client struct {
	baseURL    string
	publicURL  string
	httpClient *http.Client
	domains    []string
}

// NewClient creates a new MeTube client.
func NewClient(baseURL, publicURL string, domains []string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		publicURL: strings.TrimRight(publicURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		domains: domains,
	}
}

// AddDownload submits a download request to MeTube via POST /add.
func (c *Client) AddDownload(ctx context.Context, req AddRequest) error {
	if c.baseURL == "" {
		return fmt.Errorf("MeTube URL is not configured")
	}

	if req.AutoStart == false {
		req.AutoStart = true
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal MeTube request: %w", err)
	}

	addURL := c.baseURL + "/add"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, addURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create MeTube request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send request to MeTube (%s): %w", addURL, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MeTube returned error HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// Verify response body
	var res map[string]interface{}
	if err := json.Unmarshal(respBody, &res); err == nil {
		if status, ok := res["status"].(string); ok && strings.ToLower(status) == "error" {
			msg, _ := res["msg"].(string)
			return fmt.Errorf("MeTube error: %s", msg)
		}
	}

	return nil
}

// IsVideoURL checks whether the given string is a valid URL belonging to one of the configured video domains.
func (c *Client) IsVideoURL(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	u, err := url.Parse(text)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}

	host := strings.ToLower(u.Hostname())
	if len(c.domains) == 0 {
		// If no domain list specified, treat any valid HTTP/HTTPS URL as eligible
		return true
	}

	for _, d := range c.domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}

	return false
}

// HistoryItem represents a single download task in MeTube history.
type HistoryItem struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	URL          string  `json:"url"`
	Quality      string  `json:"quality"`
	DownloadType string  `json:"download_type"`
	Format       string  `json:"format"`
	Folder       string  `json:"folder"`
	Percent      float64 `json:"percent"`
	Status       string  `json:"status"` // "finished", "downloading", "error"
	Size         int64   `json:"size"`
	Filename     string  `json:"filename"`
	Timestamp    int64   `json:"timestamp"`
}

// HistoryResponse represents the output of GET /history from MeTube.
type HistoryResponse struct {
	Done    []HistoryItem `json:"done"`
	Queue   []HistoryItem `json:"queue"`
	Pending []HistoryItem `json:"pending"`
}

// GetHistory retrieves the list of current and finished downloads from MeTube.
func (c *Client) GetHistory(ctx context.Context) (*HistoryResponse, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("MeTube URL is not configured")
	}

	reqURL := c.baseURL + "/history"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MeTube /history returned HTTP %d", resp.StatusCode)
	}

	var hist HistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&hist); err != nil {
		return nil, fmt.Errorf("failed to decode MeTube history: %w", err)
	}

	return &hist, nil
}

// GetDownloadURL returns the direct HTTP URL to download a finished file from MeTube (private/base URL).
func (c *Client) GetDownloadURL(folder, filename, downloadType string) string {
	return c.buildDownloadURL(c.baseURL, folder, filename, downloadType)
}

// GetPublicDownloadURL returns the public HTTP URL to download a finished file from MeTube if configured.
func (c *Client) GetPublicDownloadURL(folder, filename, downloadType string) string {
	if c.publicURL == "" {
		return ""
	}
	return c.buildDownloadURL(c.publicURL, folder, filename, downloadType)
}

// HasPublicURL returns true if a public MeTube URL is configured.
func (c *Client) HasPublicURL() bool {
	return c.publicURL != ""
}

// PublicBaseURL returns the public base URL if configured.
func (c *Client) PublicBaseURL() string {
	return c.publicURL
}

func (c *Client) buildDownloadURL(base, folder, filename, downloadType string) string {
	prefix := "download"
	if strings.ToLower(downloadType) == "audio" {
		prefix = "audio_download"
	}

	folder = strings.Trim(folder, "/")
	filename = url.PathEscape(filename)

	if folder == "" {
		return fmt.Sprintf("%s/%s/%s", base, prefix, filename)
	}
	folderEscaped := url.PathEscape(folder)
	return fmt.Sprintf("%s/%s/%s/%s", base, prefix, folderEscaped, filename)
}

// DownloadFile downloads binary data of a finished file from MeTube.
func (c *Client) DownloadFile(ctx context.Context, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}

	dlClient := &http.Client{Timeout: 300 * time.Second}
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download from MeTube failed (HTTP %d)", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
