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
	DownloadType     string `json:"download_type,omitempty"`
	Quality          string `json:"quality,omitempty"`
	Format           string `json:"format,omitempty"`
	Codec            string `json:"codec,omitempty"`
	VideoCodec       string `json:"video_codec,omitempty"`
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
	Msg          string  `json:"msg"`
	Error        string  `json:"error"`
}

// ErrorMessage returns the error description if present.
func (h HistoryItem) ErrorMessage() string {
	if strings.TrimSpace(h.Msg) != "" {
		return strings.TrimSpace(h.Msg)
	}
	if strings.TrimSpace(h.Error) != "" {
		return strings.TrimSpace(h.Error)
	}
	return ""
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

// DeleteRequest represents the payload expected by MeTube's /delete endpoint.
type DeleteRequest struct {
	IDs         []string `json:"ids"`
	Where       string   `json:"where"` // "done" or "queue"
	DeleteFiles bool     `json:"delete_files"`
}

// Delete removes items from MeTube queue or completed list via POST /delete.
func (c *Client) Delete(ctx context.Context, ids []string, where string, deleteFiles bool) error {
	if c.baseURL == "" {
		return fmt.Errorf("MeTube URL is not configured")
	}
	if len(ids) == 0 {
		return nil
	}
	if where != "queue" && where != "done" {
		where = "done"
	}

	reqBody := DeleteRequest{
		IDs:         ids,
		Where:       where,
		DeleteFiles: deleteFiles,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal MeTube delete request: %w", err)
	}

	delURL := c.baseURL + "/delete"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, delURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create MeTube delete request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send delete request to MeTube: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("MeTube /delete returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func addUnique(list []string, val string) []string {
	val = strings.TrimSpace(val)
	if val == "" {
		return list
	}
	for _, item := range list {
		if item == val {
			return list
		}
	}
	return append(list, val)
}

// DeleteByItemOrURL attempts to find and delete matching items from MeTube history (both done and queue).
// MeTube indexes downloads internally by their media URL (and in some contexts by video ID),
// so both matching URLs and IDs are gathered and submitted to ensure complete removal from WebUI and disk.
func (c *Client) DeleteByItemOrURL(ctx context.Context, id, targetURL string, deleteFiles bool, extraURLs ...string) error {
	idsDone := make([]string, 0)
	idsQueue := make([]string, 0)

	allInputURLs := make([]string, 0, len(extraURLs)+2)
	if targetURL != "" {
		allInputURLs = append(allInputURLs, targetURL)
	}
	for _, u := range extraURLs {
		if u != "" {
			allInputURLs = append(allInputURLs, u)
		}
	}

	matchesItem := func(item HistoryItem) bool {
		if id != "" && (item.ID == id || item.URL == id || (item.URL != "" && strings.Contains(item.URL, id))) {
			return true
		}
		for _, u := range allInputURLs {
			if u == "" {
				continue
			}
			if item.URL == u || strings.EqualFold(item.URL, u) {
				return true
			}
			if item.ID != "" && strings.Contains(u, item.ID) {
				return true
			}
			if item.URL != "" && (strings.Contains(u, item.URL) || strings.Contains(item.URL, u)) {
				return true
			}
		}
		return false
	}

	foundInDone := false
	foundInQueue := false

	hist, err := c.GetHistory(ctx)
	if err == nil && hist != nil {
		for _, item := range hist.Done {
			if matchesItem(item) {
				foundInDone = true
				if item.URL != "" {
					idsDone = addUnique(idsDone, item.URL)
				}
				if item.ID != "" {
					idsDone = addUnique(idsDone, item.ID)
				}
			}
		}
		for _, item := range hist.Queue {
			if matchesItem(item) {
				foundInQueue = true
				if item.URL != "" {
					idsQueue = addUnique(idsQueue, item.URL)
				}
				if item.ID != "" {
					idsQueue = addUnique(idsQueue, item.ID)
				}
			}
		}
		for _, item := range hist.Pending {
			if matchesItem(item) {
				foundInQueue = true
				if item.URL != "" {
					idsQueue = addUnique(idsQueue, item.URL)
				}
				if item.ID != "" {
					idsQueue = addUnique(idsQueue, item.ID)
				}
			}
		}
	}

	// Always ensure input URL and ID are included in the relevant delete lists
	if foundInDone {
		if id != "" {
			idsDone = addUnique(idsDone, id)
		}
		for _, u := range allInputURLs {
			idsDone = addUnique(idsDone, u)
		}
	}
	if foundInQueue {
		if id != "" {
			idsQueue = addUnique(idsQueue, id)
		}
		for _, u := range allInputURLs {
			idsQueue = addUnique(idsQueue, u)
		}
	}

	// If neither was explicitly matched in history, fallback to trying both
	if !foundInDone && !foundInQueue {
		if id != "" {
			idsDone = addUnique(idsDone, id)
			idsQueue = addUnique(idsQueue, id)
		}
		for _, u := range allInputURLs {
			idsDone = addUnique(idsDone, u)
			idsQueue = addUnique(idsQueue, u)
		}
	}

	var lastErr error
	if len(idsDone) > 0 {
		if err := c.Delete(ctx, idsDone, "done", deleteFiles); err != nil {
			lastErr = err
		}
	}
	if len(idsQueue) > 0 {
		if err := c.Delete(ctx, idsQueue, "queue", deleteFiles); err != nil && lastErr == nil {
			lastErr = err
		}
	}
	return lastErr
}



