package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client handles refreshing Jellyfin media libraries.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Jellyfin client.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// RefreshLibrary sends a library scan refresh command to Jellyfin.
func (c *Client) RefreshLibrary(ctx context.Context) error {
	if c.baseURL == "" {
		return fmt.Errorf("Jellyfin URL not set")
	}

	endpoint := fmt.Sprintf("%s/Library/Refresh?api_key=%s", c.baseURL, url.QueryEscape(c.apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("X-Emby-Token", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Jellyfin returned HTTP %d", resp.StatusCode)
	}

	return nil
}
