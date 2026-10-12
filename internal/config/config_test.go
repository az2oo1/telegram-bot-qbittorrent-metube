package config

import (
	"os"
	"reflect"
	"testing"
)

func TestParseCategories(t *testing.T) {
	// Test JSON format
	jsonStr := `{"movies":"🎬 Movies","tv":"📺 Series"}`
	got := parseCategories(jsonStr)
	if got["movies"] != "🎬 Movies" || got["tv"] != "📺 Series" {
		t.Fatalf("unexpected categories from JSON: %v", got)
	}

	// Test comma format
	commaStr := "movies:🎬 Movies,tv:📺 Series"
	gotComma := parseCategories(commaStr)
	if gotComma["movies"] != "🎬 Movies" || gotComma["tv"] != "📺 Series" {
		t.Fatalf("unexpected categories from comma-separated: %v", gotComma)
	}

	// Test default fallback
	empty := parseCategories("")
	if len(empty) == 0 || empty["tv"] == "" {
		t.Fatalf("default categories should not be empty")
	}
}

func TestParseCommaStrings(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"a, b , c", []string{"a", "b", "c"}},
		{"/mnt/d1,/mnt/d2", []string{"/mnt/d1", "/mnt/d2"}},
	}

	for _, tt := range tests {
		got := parseCommaStrings(tt.input)
		if !reflect.DeepEqual(got, tt.expected) {
			t.Errorf("parseCommaStrings(%q) = %v; want %v", tt.input, got, tt.expected)
		}
	}
}

func TestParseCommaInt64s(t *testing.T) {
	got := parseCommaInt64s("12345, 67890, invalid, 111")
	expected := []int64{12345, 67890, 111}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("parseCommaInt64s failed: got %v; want %v", got, expected)
	}
}

func TestLoadConfig(t *testing.T) {
	os.Setenv("BOT_TOKEN", "test_token_123")
	os.Setenv("ALLOWED_USER_IDS", "1001,1002")
	os.Setenv("QB_URL", "http://localhost:8080/")
	os.Setenv("METUBE_URL", "http://localhost:8081/")
	os.Setenv("ACTION_ON_COMPLETE", "pause")
	defer func() {
		os.Unsetenv("BOT_TOKEN")
		os.Unsetenv("ALLOWED_USER_IDS")
		os.Unsetenv("QB_URL")
		os.Unsetenv("METUBE_URL")
		os.Unsetenv("ACTION_ON_COMPLETE")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BotToken != "test_token_123" {
		t.Errorf("expected BotToken test_token_123, got %s", cfg.BotToken)
	}
	if len(cfg.AllowedUserIDs) != 2 || cfg.AllowedUserIDs[0] != 1001 {
		t.Errorf("unexpected AllowedUserIDs: %v", cfg.AllowedUserIDs)
	}
	if cfg.QBURL != "http://localhost:8080" {
		t.Errorf("QBURL trailing slash should be stripped, got %s", cfg.QBURL)
	}
	if cfg.MeTubeURL != "http://localhost:8081" {
		t.Errorf("MeTubeURL trailing slash should be stripped, got %s", cfg.MeTubeURL)
	}
	if !cfg.IsUserAllowed(1001) || cfg.IsUserAllowed(9999) {
		t.Errorf("IsUserAllowed failed")
	}
	if cfg.TelegramMaxUploadMB != 50 {
		t.Errorf("expected default TelegramMaxUploadMB 50 for cloud API, got %d", cfg.TelegramMaxUploadMB)
	}

	// Test local API URL sets default upload limit to 2000
	os.Setenv("TELEGRAM_API_URL", "http://127.0.0.1:8082")
	defer os.Unsetenv("TELEGRAM_API_URL")
	cfgLocal, err := Load()
	if err != nil {
		t.Fatalf("Load() with local API error: %v", err)
	}
	if cfgLocal.TelegramMaxUploadMB != 2000 {
		t.Errorf("expected default TelegramMaxUploadMB 2000 for local API, got %d", cfgLocal.TelegramMaxUploadMB)
	}
}
