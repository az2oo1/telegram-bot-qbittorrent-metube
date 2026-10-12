package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsTorrentURL(t *testing.T) {
	validURLs := []string{
		"https://releases.ubuntu.com/24.04/ubuntu-24.04-desktop-amd64.iso.torrent",
		"http://tracker.example.com/download.php?id=123&file=test.torrent",
		"https://site.org/torrents/download/sample.torrent?token=abc",
		"https://example.com/file.TORRENT",
		"<https://example.com/file.torrent>",
		"https://site.com/get.php?torrent=123",
		"https://tracker.org/dl?file=movie&type=torrent",
	}

	for _, u := range validURLs {
		if !isTorrentURL(u) {
			t.Errorf("expected %q to be recognized as a torrent URL", u)
		}
	}

	invalidURLs := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://google.com",
		"https://example.com/image.png",
		"not_a_url",
		"magnet:?xt=urn:btih:12345",
		"",
	}

	for _, u := range invalidURLs {
		if isTorrentURL(u) {
			t.Errorf("expected %q NOT to be recognized as a torrent URL", u)
		}
	}
}

func TestIsTorrentData(t *testing.T) {
	validData := []byte("d8:announce28:http://tracker.example.com/a4:infod4:name4:testee")
	if !isTorrentData(validData) {
		t.Errorf("expected valid torrent data to return true")
	}

	invalidData := []byte("<html><body>Not a torrent</body></html>")
	if isTorrentData(invalidData) {
		t.Errorf("expected HTML data to return false")
	}

	emptyData := []byte{}
	if isTorrentData(emptyData) {
		t.Errorf("expected empty data to return false")
	}
}

func TestDownloadTorrentBytes(t *testing.T) {
	mockTorrent := []byte("d8:announce18:http://tracker.com4:infod4:name4:testee")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="ubuntu.torrent"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockTorrent)
	}))
	defer srv.Close()

	data, fileName, err := downloadTorrentBytes(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("downloadTorrentBytes failed: %v", err)
	}

	if string(data) != string(mockTorrent) {
		t.Errorf("unexpected data received: %s", string(data))
	}
	if fileName != "ubuntu.torrent" {
		t.Errorf("unexpected filename: %s, expected ubuntu.torrent", fileName)
	}
}
