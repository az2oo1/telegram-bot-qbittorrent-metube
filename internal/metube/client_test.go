package metube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsVideoURL(t *testing.T) {
	c := NewClient("http://localhost:8081", "", []string{"youtube.com", "youtu.be", "vimeo.com"})

	valid := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"http://vimeo.com/12345678",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ",
	}

	for _, u := range valid {
		if !c.IsVideoURL(u) {
			t.Errorf("expected %s to be recognized as video URL", u)
		}
	}

	invalid := []string{
		"https://google.com",
		"https://github.com",
		"not a url",
		"ftp://youtube.com/video",
		"",
	}

	for _, u := range invalid {
		if c.IsVideoURL(u) {
			t.Errorf("expected %s NOT to be recognized as video URL", u)
		}
	}
}

func TestAddDownload(t *testing.T) {
	var received AddRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/add" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", nil)
	err := c.AddDownload(context.Background(), AddRequest{
		URL:       "https://www.youtube.com/watch?v=test",
		Quality:   "best",
		Format:    "mp4",
		Folder:    "movies",
		AutoStart: true,
	})

	if err != nil {
		t.Fatalf("AddDownload failed: %v", err)
	}

	if received.URL != "https://www.youtube.com/watch?v=test" || received.Folder != "movies" || received.Format != "mp4" {
		t.Fatalf("unexpected request received by MeTube server: %+v", received)
	}
}

func TestHistoryItemErrorMessage(t *testing.T) {
	item1 := HistoryItem{Msg: "ERROR: unable to download video data: HTTP Error 403: Forbidden"}
	if item1.ErrorMessage() != "ERROR: unable to download video data: HTTP Error 403: Forbidden" {
		t.Errorf("expected msg error, got %s", item1.ErrorMessage())
	}

	item2 := HistoryItem{Error: "Some other error"}
	if item2.ErrorMessage() != "Some other error" {
		t.Errorf("expected error field, got %s", item2.ErrorMessage())
	}

	item3 := HistoryItem{}
	if item3.ErrorMessage() != "" {
		t.Errorf("expected empty string, got %s", item3.ErrorMessage())
	}
}

func TestDelete(t *testing.T) {
	var received DeleteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/delete" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", nil)
	err := c.Delete(context.Background(), []string{"test_id_1"}, "done", false)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if len(received.IDs) != 1 || received.IDs[0] != "test_id_1" || received.Where != "done" {
		t.Fatalf("unexpected delete payload: %+v", received)
	}
}

func TestDeleteByItemOrURL(t *testing.T) {
	var receivedDel DeleteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/history" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"done": [{"id": "item123", "url": "https://youtube.com/watch?v=123", "filename": "test.mp4"}],
				"queue": []
			}`))
			return
		}
		if r.URL.Path == "/delete" {
			if err := json.NewDecoder(r.Body).Decode(&receivedDel); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", nil)
	err := c.DeleteByItemOrURL(context.Background(), "item123", "https://youtube.com/watch?v=123", true)
	if err != nil {
		t.Fatalf("DeleteByItemOrURL failed: %v", err)
	}

	if !receivedDel.DeleteFiles {
		t.Errorf("expected DeleteFiles to be true, got false")
	}
	if len(receivedDel.IDs) == 0 || receivedDel.IDs[0] != "item123" {
		t.Errorf("expected ID item123 to be deleted, got %+v", receivedDel.IDs)
	}
}

