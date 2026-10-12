package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TorrentInfo represents a torrent entry from /api/v2/torrents/info.
type TorrentInfo struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
	State    string  `json:"state"`
	DLSpeed  int64   `json:"dlspeed"`
	UPSpeed  int64   `json:"upspeed"`
	Size       int64   `json:"size"`
	Category   string  `json:"category"`
	SavePath   string  `json:"save_path"`
	ETA        int64   `json:"eta"`
	Downloaded int64   `json:"downloaded"`
}

// Client communicates with qBittorrent Web API v2.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	mu         sync.Mutex
	isLoggedIn bool
}

// NewClient initializes a qBittorrent client.
func NewClient(baseURL, username, password string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Login authenticates with qBittorrent via POST /api/v2/auth/login.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data := url.Values{}
	data.Set("username", c.username)
	data.Set("password", c.password)

	loginURL := c.baseURL + "/api/v2/auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent login failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := strings.TrimSpace(string(body))

	if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent) || bodyStr == "Fails." {
		return fmt.Errorf("qBittorrent login rejected (HTTP %d): %s", resp.StatusCode, bodyStr)
	}

	c.isLoggedIn = true
	return nil
}

func (c *Client) request(ctx context.Context, method, endpoint string, values url.Values, hasRetried bool) (*http.Response, error) {
	reqURL := c.baseURL + endpoint

	var bodyReader io.Reader
	contentType := ""
	if method == http.MethodPost && values != nil {
		bodyReader = strings.NewReader(values.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else if method == http.MethodGet && values != nil {
		reqURL += "?" + values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Session expired or forbidden
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) && !hasRetried {
		resp.Body.Close()
		if err := c.Login(ctx); err != nil {
			return nil, fmt.Errorf("re-login failed: %w", err)
		}
		return c.request(ctx, method, endpoint, values, true)
	}

	return resp, nil
}

// AddMagnet adds a torrent via magnet URL to qBittorrent.
func (c *Client) AddMagnet(ctx context.Context, magnetURL, savePath, category string) error {
	values := url.Values{}
	values.Set("urls", magnetURL)
	if savePath != "" {
		values.Set("savepath", savePath)
	}
	if category != "" {
		values.Set("category", category)
	}

	resp, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/add", values, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qBittorrent add torrent failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// AddTorrentFile uploads a .torrent file to qBittorrent.
func (c *Client) AddTorrentFile(ctx context.Context, fileName string, fileData []byte, savePath, category string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("torrents", fileName)
	if err != nil {
		return err
	}
	if _, err := part.Write(fileData); err != nil {
		return err
	}

	if savePath != "" {
		if err := writer.WriteField("savepath", savePath); err != nil {
			return err
		}
	}
	if category != "" {
		if err := writer.WriteField("category", category); err != nil {
			return err
		}
	}

	if err := writer.Close(); err != nil {
		return err
	}

	reqURL := c.baseURL + "/api/v2/torrents/add"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		if err := c.Login(ctx); err != nil {
			return fmt.Errorf("re-login failed: %w", err)
		}
		// Retry upload once
		return c.AddTorrentFile(ctx, fileName, fileData, savePath, category)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qBittorrent add torrent file failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetTorrents returns torrents matching the filter (e.g. "all", "completed", "downloading").
func (c *Client) GetTorrents(ctx context.Context, filter, hash string) ([]TorrentInfo, error) {
	values := url.Values{}
	if filter != "" {
		values.Set("filter", filter)
	}
	if hash != "" {
		values.Set("hashes", hash)
	}

	resp, err := c.request(ctx, http.MethodGet, "/api/v2/torrents/info", values, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qBittorrent torrents info failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var list []TorrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// Pause pauses one or more torrents.
func (c *Client) Pause(ctx context.Context, hashes string) error {
	values := url.Values{}
	values.Set("hashes", hashes)
	resp, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/pause", values, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Resume resumes one or more torrents.
func (c *Client) Resume(ctx context.Context, hashes string) error {
	values := url.Values{}
	values.Set("hashes", hashes)
	resp, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/resume", values, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Delete removes torrents, optionally deleting downloaded files.
func (c *Client) Delete(ctx context.Context, hashes string, deleteFiles bool) error {
	values := url.Values{}
	values.Set("hashes", hashes)
	if deleteFiles {
		values.Set("deleteFiles", "true")
	} else {
		values.Set("deleteFiles", "false")
	}

	resp, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/delete", values, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// SetDownloadLimit sets the download speed limit in bytes per second for a torrent.
func (c *Client) SetDownloadLimit(ctx context.Context, hash string, limitBytes int64) error {
	values := url.Values{}
	values.Set("hashes", hash)
	values.Set("limit", fmt.Sprintf("%d", limitBytes))

	resp, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/setDownloadLimit", values, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
