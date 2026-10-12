package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config represents all application settings loaded from environment or .env file.
type Config struct {
	// Telegram
	BotToken            string
	AllowedUserIDs      []int64
	TelegramAPIURL      string // e.g. "http://telegram-bot-api:8081" (defaults to https://api.telegram.org)
	TelegramMaxUploadMB int    // max upload size in MB (defaults to 50 for cloud API, 2000 for local Bot API)

	// Storage & State
	StateFile string
	LogFile   string
	LogLevel  string

	// Disks & Categories
	Disks          []string
	DefaultDiskIdx int
	Categories     map[string]string

	// qBittorrent
	QBURL            string
	QBUser           string
	QBPass           string
	ActionOnComplete string // "stop", "remove", "remove_data"

	// MeTube
	MeTubeEnabled        bool
	MeTubeURL            string
	MeTubePublicURL      string
	MeTubeDefaultQuality string
	MeTubeDomains        []string

	// Jellyfin
	JellyfinEnabled bool
	JellyfinURL     string
	JellyfinAPIKey  string

	// Timings & display
	PollTimeout             int           // in seconds for Telegram getUpdates
	CheckInterval           time.Duration // interval to check torrent completions
	StateSaveInterval       time.Duration // interval to save state
	NotificationCleanupTime time.Duration // auto-delete confirmation notification
	StatusShowLimit         int
	StatusFilter            string // "all" or "downloading"
}

// DefaultCategories returns the default category mappings.
func DefaultCategories() map[string]string {
	return map[string]string{
		"tv":          "📺 Series",
		"movies":      "🎬 Movies",
		"download-pc": "📁 Other",
		"Music":       "🎵 Music",
		"photo":       "🖼 Photo",
		"cartoni":     "🧸 Cartoni",
		"vidcontent":  "🎬 Videos",
	}
}

// DefaultDomains returns the default domains recognized as video/media URLs.
func DefaultDomains() []string {
	return []string{
		"youtube.com",
		"youtu.be",
		"www.youtube.com",
		"m.youtube.com",
		"music.youtube.com",
		"vimeo.com",
		"twitter.com",
		"x.com",
		"instagram.com",
		"tiktok.com",
		"www.tiktok.com",
		"reddit.com",
		"www.reddit.com",
		"twitch.tv",
		"www.twitch.tv",
		"dailymotion.com",
		"www.dailymotion.com",
	}
}

// Load loads configuration from environment variables and an optional .env file.
func Load() (*Config, error) {
	// Load .env if it exists in current directory or app directory (silent ignore if not found)
	_ = godotenv.Load(".env")
	_ = godotenv.Load("/app/.env")

	telegramAPIURL := strings.TrimRight(getEnv("TELEGRAM_API_URL", ""), "/")
	defaultMaxUpload := 50
	if telegramAPIURL != "" && !strings.Contains(telegramAPIURL, "api.telegram.org") {
		defaultMaxUpload = 2000
	}

	cfg := &Config{
		BotToken:            getEnv("BOT_TOKEN", ""),
		AllowedUserIDs:      parseCommaInt64s(getEnv("ALLOWED_USER_IDS", "")),
		TelegramAPIURL:      telegramAPIURL,
		TelegramMaxUploadMB: getEnvInt("TELEGRAM_MAX_UPLOAD_MB", defaultMaxUpload),

		StateFile: getEnv("STATE_FILE", "./data/bot_state.json"),
		LogFile:   getEnv("LOG_FILE", ""),
		LogLevel:  strings.ToLower(getEnv("LOG_LEVEL", "info")),

		Disks:          parseCommaStrings(getEnv("DISKS", "/mnt/ftp,/mnt/ftp2")),
		DefaultDiskIdx: getEnvInt("DEFAULT_DISK_IDX", 0),
		Categories:     parseCategories(getEnv("CATEGORIES", "")),

		QBURL:            strings.TrimRight(getEnv("QB_URL", "http://127.0.0.1:8080"), "/"),
		QBUser:           getEnv("QB_USER", "admin"),
		QBPass:           getEnv("QB_PASS", ""),
		ActionOnComplete: strings.ToLower(getEnv("ACTION_ON_COMPLETE", "stop")),

		MeTubeURL:            strings.TrimRight(getEnv("METUBE_URL", "http://127.0.0.1:8081"), "/"),
		MeTubePublicURL:      strings.TrimRight(getEnv("METUBE_PUBLIC_URL", ""), "/"),
		MeTubeDefaultQuality: getEnv("METUBE_DEFAULT_QUALITY", "best"),
		MeTubeDomains:        parseCommaStringsWithDefault(getEnv("METUBE_DOMAINS", ""), DefaultDomains()),

		JellyfinURL:    strings.TrimRight(getEnv("JELLYFIN_URL", "http://127.0.0.1:8096"), "/"),
		JellyfinAPIKey: getEnv("JELLYFIN_API_KEY", ""),

		PollTimeout:             getEnvInt("POLL_TIMEOUT", 10),
		CheckInterval:           time.Duration(getEnvInt("CHECK_INTERVAL", 30)) * time.Second,
		StateSaveInterval:       time.Duration(getEnvInt("STATE_SAVE_INTERVAL", 300)) * time.Second,
		NotificationCleanupTime: time.Duration(getEnvInt("NOTIFICATION_CLEANUP_TIME", 120)) * time.Second,
		StatusShowLimit:         getEnvInt("STATUS_SHOW_LIMIT", 10),
		StatusFilter:            strings.ToLower(getEnv("STATUS_FILTER", "all")),
	}

	// Boolean toggles
	cfg.MeTubeEnabled = getEnvBool("METUBE_ENABLED", cfg.MeTubeURL != "")
	cfg.JellyfinEnabled = getEnvBool("JELLYFIN_ENABLED", false)

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required; please set it in your .env or environment")
	}

	if len(cfg.AllowedUserIDs) == 0 {
		return nil, fmt.Errorf("ALLOWED_USER_IDS is required; please specify at least one user ID in .env")
	}

	if cfg.DefaultDiskIdx < 0 || (len(cfg.Disks) > 0 && cfg.DefaultDiskIdx >= len(cfg.Disks)) {
		cfg.DefaultDiskIdx = 0
	}

	return cfg, nil
}

// IsUserAllowed checks if a Telegram user ID is authorized.
func (c *Config) IsUserAllowed(userID int64) bool {
	for _, id := range c.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		cleaned := strings.TrimSpace(val)
		cleaned = strings.Trim(cleaned, "\"'")
		return strings.TrimSpace(cleaned)
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		v := strings.ToLower(strings.TrimSpace(val))
		return v == "1" || v == "true" || v == "yes" || v == "on"
	}
	return defaultVal
}

func parseCommaStrings(val string) []string {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			res = append(res, p)
		}
	}
	return res
}

func parseCommaStringsWithDefault(val string, defaultVal []string) []string {
	res := parseCommaStrings(val)
	if len(res) == 0 {
		return defaultVal
	}
	return res
}

func parseCommaInt64s(val string) []int64 {
	var res []int64
	for _, p := range strings.Split(val, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			res = append(res, id)
		}
	}
	return res
}

func parseCategories(val string) map[string]string {
	val = strings.TrimSpace(val)
	if val == "" {
		return DefaultCategories()
	}

	// Try JSON first: e.g. {"tv":"📺 Series","movies":"🎬 Movies"}
	if strings.HasPrefix(val, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(val), &m); err == nil && len(m) > 0 {
			return m
		}
	}

	// Try key:value,key2:value2
	m := make(map[string]string)
	pairs := strings.Split(val, ",")
	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if k != "" && v != "" {
				m[k] = v
			}
		}
	}

	if len(m) == 0 {
		return DefaultCategories()
	}
	return m
}
