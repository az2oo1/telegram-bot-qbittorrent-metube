package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dominatos/qbittorrent-telegram-bot/internal/config"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/disk"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/jellyfin"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/metube"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/qbittorrent"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/state"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/telegram"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/torrserver"
)

const (
	Version                    = "2.0.0"
	MaxPendingLimitAttempts    = 5
	PendingDownloadExpirySecs  = 600
	TelegramFileLimitBytes     = 20 * 1024 * 1024 // 20 MB
)

// PendingDownload holds state for an interactive download selection session.
type PendingDownload struct {
	Type          string // "magnet", "metube", "file", "video", "photo", "torrent_url"
	Magnet        string
	URL           string
	FileID        string
	FileName      string
	FileData      []byte
	DiskIdx       int
	MeTubeFormat  string // "video" or "audio"
	MeTubeQuality string // "best", "1080p", "720p", "480p"
	Source        string // e.g. "torrserver"
	UserID        int64
	Expires       int64
}

// ActiveMeTubeJob tracks an in-flight MeTube download job.
type ActiveMeTubeJob struct {
	Token        string
	URL          string
	ChatID       int64
	UserID       int64
	StatusMsgID  int64
	Folder       string
	Quality      string
	Format       string
	SubmittedAt  time.Time
	LastPercent  float64
	MeTubeItemID string
}

// DeliveredMeTubeItem tracks a MeTube item delivered to Telegram so the user can delete or keep it.
type DeliveredMeTubeItem struct {
	Token     string
	ItemID    string
	URL       string
	Folder    string
	Filename  string
	ChatID    int64
	UserID    int64
	MessageID int64
	CreatedAt time.Time
}

// FailedMeTubeJob holds state for a failed MeTube download to allow retry or cancel.
type FailedMeTubeJob struct {
	Token     string
	ItemID    string
	URL       string
	Folder    string
	Quality   string
	Format    string
	ErrorMsg  string
	ChatID    int64
	UserID    int64
	MessageID int64
	CreatedAt time.Time
}

// PendingDeletion holds a message ID scheduled for auto-deletion.
type PendingDeletion struct {
	ChatID    int64
	MessageID int64
	Expires   int64
}

// Bot is the main orchestrator for qbittorrent-telegram-bot.
type Bot struct {
	cfg        *config.Config
	state      *state.State
	tg         *telegram.Client
	qb         *qbittorrent.Client
	metube     *metube.Client
	torrserver *torrserver.Client
	jellyfin   *jellyfin.Client

	logger *slog.Logger

	mu                   sync.Mutex
	pendingDownloads     map[int64]*PendingDownload
	pendingDeletions     []PendingDeletion
	lastStatusMessageIDs map[int64][]int64
	statusCancelFuncs    map[int64]context.CancelFunc
	torrServerMsgIDs     map[string][]PendingDeletion // hash -> []{chatID, messageID}
	torrServerFailCount  int
	activeMeTubeJobs     []ActiveMeTubeJob
	failedMeTubeJobs     map[string]*FailedMeTubeJob
	deliveredMeTubeItems map[string]*DeliveredMeTubeItem
}

// NewBot constructs a Bot instance with all dependent clients.
func NewBot(cfg *config.Config, logger *slog.Logger) (*Bot, error) {
	st := state.New(cfg.StateFile)
	tg := telegram.NewClient(cfg.BotToken, cfg.TelegramAPIURL)

	qb, err := qbittorrent.NewClient(cfg.QBURL, cfg.QBUser, cfg.QBPass)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize qBittorrent client: %w", err)
	}

	mt := metube.NewClient(cfg.MeTubeURL, cfg.MeTubePublicURL, cfg.MeTubeDomains)
	ts := torrserver.NewClient(cfg.TorrServerURL, cfg.TorrServerUser, cfg.TorrServerPass)
	jf := jellyfin.NewClient(cfg.JellyfinURL, cfg.JellyfinAPIKey)

	return &Bot{
		cfg:                  cfg,
		state:                st,
		tg:                   tg,
		qb:                   qb,
		metube:               mt,
		torrserver:           ts,
		jellyfin:             jf,
		logger:               logger,
		pendingDownloads:     make(map[int64]*PendingDownload),
		pendingDeletions:     make([]PendingDeletion, 0),
		lastStatusMessageIDs: make(map[int64][]int64),
		statusCancelFuncs:    make(map[int64]context.CancelFunc),
		torrServerMsgIDs:     make(map[string][]PendingDeletion),
		activeMeTubeJobs:     make([]ActiveMeTubeJob, 0),
		failedMeTubeJobs:     make(map[string]*FailedMeTubeJob),
		deliveredMeTubeItems: make(map[string]*DeliveredMeTubeItem),
	}, nil
}

// Run starts the bot polling loop and background background tasks until context cancellation.
func (b *Bot) Run(ctx context.Context) error {
	b.logger.Info("Starting qBittorrent Telegram Bot", "version", Version)

	// Validate disks on startup
	validDisks := make([]string, 0, len(b.cfg.Disks))
	for _, d := range b.cfg.Disks {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			warning := fmt.Sprintf("⚠️ Warning: Disk %s is not accessible on boot. It has been excluded from choices.", d)
			b.logger.Warn(warning)
			for _, adminID := range b.cfg.AllowedUserIDs {
				_, _ = b.tg.SendMessage(ctx, adminID, warning, "", nil)
			}
			continue
		}
		validDisks = append(validDisks, d)
	}
	if len(validDisks) > 0 {
		b.cfg.Disks = validDisks
	}

	// Try qBittorrent login
	if err := b.qb.Login(ctx); err != nil {
		b.logger.Warn("Initial qBittorrent login warning", "err", err)
	} else {
		b.logger.Info("Connected to qBittorrent successfully")
	}

	var offset int64 = 0

	tickerCheck := time.NewTicker(b.cfg.CheckInterval)
	defer tickerCheck.Stop()

	tickerTorr := time.NewTicker(b.cfg.TorrServerCheckInterval)
	defer tickerTorr.Stop()

	tickerSave := time.NewTicker(b.cfg.StateSaveInterval)
	defer tickerSave.Stop()

	tickerCleanup := time.NewTicker(5 * time.Second)
	defer tickerCleanup.Stop()

	tickerMeTube := time.NewTicker(4 * time.Second)
	defer tickerMeTube.Stop()

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("Shutdown requested, saving state...")
			_ = b.state.Save()
			return nil

		case <-tickerCheck.C:
			b.checkTorrentCompletions(ctx)

		case <-tickerTorr.C:
			if b.cfg.TorrServerEnabled {
				b.checkTorrServer(ctx)
			}

		case <-tickerMeTube.C:
			if b.cfg.MeTubeEnabled {
				b.checkMeTubeCompletions(ctx)
			}

		case <-tickerSave.C:
			if err := b.state.Save(); err != nil {
				b.logger.Error("Failed to save state", "err", err)
			}

		case <-tickerCleanup.C:
			b.cleanupExpired(ctx)
			b.checkPendingLimits(ctx)

		default:
			// Poll Telegram updates
			updates, err := b.tg.GetUpdates(ctx, offset+1, b.cfg.PollTimeout)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				b.logger.Error("Error getting Telegram updates", "err", err)
				time.Sleep(2 * time.Second)
				continue
			}

			for _, u := range updates {
				if u.UpdateID > offset {
					offset = u.UpdateID
				}
				b.handleUpdate(ctx, u)
			}
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, u telegram.Update) {
	if u.CallbackQuery != nil {
		b.handleCallback(ctx, u.CallbackQuery)
		return
	}

	m := u.Message
	if m == nil || m.From == nil {
		return
	}

	chatID := m.Chat.ID
	userID := m.From.ID

	if !b.cfg.IsUserAllowed(userID) {
		b.logger.Warn("Unauthorized message ignored", "user_id", userID, "chat_id", chatID)
		return
	}

	if b.state.AddKnownChatID(chatID) {
		_ = b.state.Save()
	}

	text := strings.TrimSpace(m.Text)

	if text == "/start" || text == "/help" {
		b.handleStart(ctx, chatID)
		return
	}

	lowerText := strings.ToLower(text)
	if text == "/status" || text == "/stats" || lowerText == "status" || lowerText == "stats" || lowerText == "statics" {
		_ = b.tg.DeleteMessage(ctx, chatID, m.MessageID)
		b.sendStatusMessage(ctx, chatID)
		return
	}

	if strings.HasPrefix(strings.ToLower(text), "magnet:?") {
		b.mu.Lock()
		b.pendingDownloads[chatID] = &PendingDownload{
			Type:    "magnet",
			Magnet:  text,
			DiskIdx: b.cfg.DefaultDiskIdx,
			UserID:  userID,
			Expires: time.Now().Unix() + PendingDownloadExpirySecs,
		}
		b.mu.Unlock()

		_, _ = b.tg.SendMessage(ctx, chatID, "🔗 *Magnet detected.* Choose destination:", "Markdown", b.categoryKeyboard(b.cfg.DefaultDiskIdx))
		return
	}

	if isTorrentURL(text) {
		cleanURL := strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "<>[]()\"'"))
		b.logger.Info("Downloading .torrent file from link", "url", cleanURL)
		data, fName, err := downloadTorrentBytes(ctx, cleanURL)
		if err != nil {
			b.logger.Error("Failed to download .torrent from link", "url", cleanURL, "err", err)
			_, _ = b.tg.SendMessage(ctx, chatID, fmt.Sprintf("❌ Failed to download `.torrent` file from link:\n`%s`", telegram.EscapeMarkdown(err.Error())), "Markdown", nil)
			return
		}
		if !isTorrentData(data) {
			b.logger.Warn("Downloaded file from torrent link is not valid bencode torrent", "url", cleanURL, "size", len(data))
			_, _ = b.tg.SendMessage(ctx, chatID, "❌ The link did not return a valid `.torrent` file.", "Markdown", nil)
			return
		}

		b.mu.Lock()
		b.pendingDownloads[chatID] = &PendingDownload{
			Type:     "file",
			FileName: fName,
			FileData: data,
			DiskIdx:  b.cfg.DefaultDiskIdx,
			UserID:   userID,
			Expires:  time.Now().Unix() + PendingDownloadExpirySecs,
		}
		b.mu.Unlock()

		safeName := telegram.EscapeMarkdown(fName)
		replyMsg, _ := b.tg.SendMessage(ctx, chatID, fmt.Sprintf("📥 Torrent file downloaded: `%s`\nChoose destination:", safeName), "Markdown", b.categoryKeyboard(b.cfg.DefaultDiskIdx))
		if replyMsg != nil {
			_ = b.tg.DeleteMessage(ctx, chatID, m.MessageID)
		}
		return
	}

	if b.cfg.MeTubeEnabled && b.metube.IsVideoURL(text) {
		b.mu.Lock()
		b.pendingDownloads[chatID] = &PendingDownload{
			Type:          "metube",
			URL:           text,
			DiskIdx:       b.cfg.DefaultDiskIdx,
			MeTubeFormat:  "video",
			MeTubeQuality: "best",
			UserID:        userID,
			Expires:       time.Now().Unix() + PendingDownloadExpirySecs,
		}
		b.mu.Unlock()

		_, _ = b.tg.SendMessage(ctx, chatID, "🎬 *Media URL detected.* Choose quality & format:", "Markdown", b.qualityKeyboard())
		return
	}

	// Media/documents
	b.processMedia(ctx, m, chatID, userID)
}

func (b *Bot) handleStart(ctx context.Context, chatID int64) {
	diskLines := make([]string, 0, len(b.cfg.Disks))
	for i, d := range b.cfg.Disks {
		freeStr := "N/A"
		if freeBytes, err := disk.GetFreeSpace(d); err == nil {
			freeGb := float64(freeBytes) / 1024 / 1024 / 1024
			freeStr = fmt.Sprintf("%.1f GB", freeGb)
		}
		diskLines = append(diskLines, fmt.Sprintf("• Disk %d (`%s`): %s free", i+1, d, freeStr))
	}

	disksText := "None configured"
	if len(diskLines) > 0 {
		disksText = strings.Join(diskLines, "\n")
	}

	features := []string{
		"• Send a *magnet link*, *.torrent link*, or *.torrent file* to download via qBittorrent.",
	}
	if b.cfg.MeTubeEnabled {
		features = append(features, "• Send a *video URL* (YouTube, Vimeo, Twitter, etc.) to download via MeTube.")
	}
	features = append(features, "• Send photos or videos directly to store on the host disk (up to 20MB).")
	features = append(features, "• Use `/status` to monitor active downloads and free space.")

	msg := fmt.Sprintf("👋 *Welcome to qBot v%s!*\n\n*Features:*\n%s\n\n💾 *Disks & Storage:*\n%s",
		Version, strings.Join(features, "\n"), disksText)

	_, _ = b.tg.SendMessage(ctx, chatID, msg, "Markdown", nil)
}

func (b *Bot) processMedia(ctx context.Context, m *telegram.Message, chatID, userID int64) {
	var fileID, name, fileType string
	var size int64

	if m.Document != nil {
		fileID = m.Document.FileID
		name = m.Document.FileName
		if name == "" {
			name = "file"
		}
		fileType = "file"
		size = m.Document.FileSize
	} else if m.Video != nil {
		fileID = m.Video.FileID
		name = m.Video.FileName
		if name == "" {
			name = "video.mp4"
		}
		fileType = "video"
		size = m.Video.FileSize
	} else if len(m.Photo) > 0 {
		p := m.Photo[len(m.Photo)-1]
		fileID = p.FileID
		name = fmt.Sprintf("photo_%d.jpg", time.Now().Unix())
		fileType = "photo"
		size = p.FileSize
	}

	if fileID == "" {
		return
	}

	maxIncomingBytes := int64(TelegramFileLimitBytes)
	if b.cfg.TelegramMaxUploadMB > 50 {
		maxIncomingBytes = int64(b.cfg.TelegramMaxUploadMB) * 1024 * 1024
	}

	if size > maxIncomingBytes {
		sizeMb := float64(size) / 1024 / 1024
		limitMb := float64(maxIncomingBytes) / 1024 / 1024
		_, _ = b.tg.SendMessage(ctx, chatID, fmt.Sprintf("⚠️ *File too large* (%.1f MB).\nTelegram Bot API limit is *%.0f MB*.", sizeMb, limitMb), "Markdown", nil)
		return
	}

	b.mu.Lock()
	b.pendingDownloads[chatID] = &PendingDownload{
		Type:     fileType,
		FileID:   fileID,
		FileName: name,
		DiskIdx:  b.cfg.DefaultDiskIdx,
		UserID:   userID,
		Expires:  time.Now().Unix() + PendingDownloadExpirySecs,
	}
	b.mu.Unlock()

	isTorrent := strings.HasSuffix(strings.ToLower(name), ".torrent")
	safeName := telegram.EscapeMarkdown(name)
	replyMsg, _ := b.tg.SendMessage(ctx, chatID, fmt.Sprintf("📥 Received: `%s`\nChoose destination:", safeName), "Markdown", b.categoryKeyboard(b.cfg.DefaultDiskIdx))
	if replyMsg != nil && isTorrent {
		_ = b.tg.DeleteMessage(ctx, chatID, m.MessageID)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *telegram.CallbackQuery) {
	if cb.Message == nil {
		return
	}

	chatID := cb.Message.Chat.ID
	data := cb.Data
	callerID := cb.From.ID

	b.logger.Info("Received callback query", "data", data, "chat_id", chatID, "caller_id", callerID)

	b.mu.Lock()
	pending, hasPending := b.pendingDownloads[chatID]
	b.mu.Unlock()

	isPendingOwner := hasPending && pending.UserID == callerID
	isWhitelisted := b.cfg.IsUserAllowed(callerID)

	if !isWhitelisted && !isPendingOwner {
		b.logger.Warn("Unauthorized callback attempt", "data", data, "caller_id", callerID)
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.", true)
		return
	}

	if data == "noop" {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "", false)
		return
	}

	if strings.HasPrefix(data, "mt_retry:") {
		token := strings.TrimPrefix(data, "mt_retry:")
		b.handleMeTubeRetry(ctx, cb, token)
		return
	}

	if strings.HasPrefix(data, "mt_cancel:") {
		token := strings.TrimPrefix(data, "mt_cancel:")
		b.handleMeTubeCancel(ctx, cb, token)
		return
	}

	if strings.HasPrefix(data, "mt_cancel_active:") {
		token := strings.TrimPrefix(data, "mt_cancel_active:")
		b.handleMeTubeCancelActive(ctx, cb, token)
		return
	}

	if strings.HasPrefix(data, "mt_del_file:") {
		token := strings.TrimPrefix(data, "mt_del_file:")
		b.handleMeTubeDeleteFile(ctx, cb, token)
		return
	}

	if strings.HasPrefix(data, "mt_keep_file:") {
		token := strings.TrimPrefix(data, "mt_keep_file:")
		b.handleMeTubeKeepFile(ctx, cb, token)
		return
	}

	// Validate pending session requirement
	if data == "cancel_dl" || strings.HasPrefix(data, "set_disk:") || strings.HasPrefix(data, "dl:") || strings.HasPrefix(data, "metube_fmt:") || strings.HasPrefix(data, "yt_fmt:") || strings.HasPrefix(data, "metube_q:") {
		if !hasPending {
			_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "No pending download or session expired.", true)
			return
		}
		if pending.UserID != callerID {
			_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized. This isn't your download.", true)
			return
		}
	}

	_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "", false)

	if data == "cancel_dl" {
		b.mu.Lock()
		delete(b.pendingDownloads, chatID)
		b.mu.Unlock()
		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		return
	}

	if strings.HasPrefix(data, "metube_q:") {
		parts := strings.Split(data, ":")
		quality := "best"
		format := "video"
		if len(parts) >= 2 {
			quality = parts[1]
		}
		if len(parts) >= 3 {
			format = parts[2]
		}

		b.mu.Lock()
		pending.MeTubeQuality = quality
		pending.MeTubeFormat = format
		pCopy := *pending
		delete(b.pendingDownloads, chatID)
		b.mu.Unlock()

		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		b.finalizeDownload(ctx, chatID, "vidcontent", "", &pCopy)
		return
	}

	if strings.HasPrefix(data, "metube_fmt:") || strings.HasPrefix(data, "yt_fmt:") {
		format := "video"
		if strings.HasSuffix(data, ":audio") {
			format = "audio"
		}

		b.mu.Lock()
		pending.MeTubeQuality = "best"
		pending.MeTubeFormat = format
		pCopy := *pending
		delete(b.pendingDownloads, chatID)
		b.mu.Unlock()

		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		b.finalizeDownload(ctx, chatID, "vidcontent", "", &pCopy)
		return
	}

	if strings.HasPrefix(data, "set_disk:") {
		idxStr := strings.TrimPrefix(data, "set_disk:")
		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx < 0 || idx >= len(b.cfg.Disks) {
			return
		}

		b.mu.Lock()
		pending.DiskIdx = idx
		b.mu.Unlock()

		freeInfo := ""
		if freeBytes, err := disk.GetFreeSpace(b.cfg.Disks[idx]); err == nil {
			freeGb := float64(freeBytes) / 1024 / 1024 / 1024
			freeInfo = fmt.Sprintf(" [%.1fGB free]", freeGb)
		}

		newText := fmt.Sprintf("💿 Disk: %s%s\nChoose category:", b.cfg.Disks[idx], freeInfo)
		kb := b.categoryKeyboard(idx)

		if len(cb.Message.Photo) > 0 {
			_ = b.tg.EditMessageCaption(ctx, chatID, cb.Message.MessageID, newText, "", kb)
		} else {
			_ = b.tg.EditMessageText(ctx, chatID, cb.Message.MessageID, newText, "", kb)
		}
		return
	}

	if strings.HasPrefix(data, "dl:") {
		catKey := strings.TrimPrefix(data, "dl:")
		if _, ok := b.cfg.Categories[catKey]; !ok {
			b.logger.Error("Unknown callback category", "category", catKey)
			return
		}

		b.mu.Lock()
		pCopy := *pending
		delete(b.pendingDownloads, chatID)
		b.mu.Unlock()

		diskPath := ""
		if pCopy.DiskIdx >= 0 && pCopy.DiskIdx < len(b.cfg.Disks) {
			diskPath = b.cfg.Disks[pCopy.DiskIdx]
		}
		targetDir := filepath.Join(diskPath, catKey)

		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		b.finalizeDownload(ctx, chatID, catKey, targetDir, &pCopy)
		return
	}

	if strings.HasPrefix(data, "ts_dl:") {
		hash := strings.TrimPrefix(data, "ts_dl:")
		if b.handleTorrServerDownload(ctx, chatID, hash, cb.Message, callerID) {
			b.deleteOtherTorrServerMessages(ctx, hash, chatID)
			b.mu.Lock()
			delete(b.torrServerMsgIDs, hash)
			b.mu.Unlock()
			_ = b.state.Save()
		}
		return
	}

	if strings.HasPrefix(data, "ts_ignore:") {
		hash := strings.TrimPrefix(data, "ts_ignore:")
		b.state.AddNotifiedTorrHash(hash)
		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		b.deleteOtherTorrServerMessages(ctx, hash, chatID)
		b.mu.Lock()
		delete(b.torrServerMsgIDs, hash)
		b.mu.Unlock()
		_ = b.state.Save()
		return
	}

	if strings.HasPrefix(data, "qb_pause:") {
		hash := strings.TrimPrefix(data, "qb_pause:")
		_ = b.qb.Pause(ctx, hash)
		torrents, _ := b.qb.GetTorrents(ctx, "", hash)
		if len(torrents) > 0 {
			t := torrents[0]
			cardText := formatTorrentCard(t)
			kb := b.torrentKeyboard(hash, true)
			_ = b.tg.EditMessageText(ctx, chatID, cb.Message.MessageID, cardText, "Markdown", kb)
		}
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "⏸ Paused", false)
		return
	}

	if strings.HasPrefix(data, "qb_res:") {
		hash := strings.TrimPrefix(data, "qb_res:")
		_ = b.qb.Resume(ctx, hash)
		torrents, _ := b.qb.GetTorrents(ctx, "", hash)
		if len(torrents) > 0 {
			t := torrents[0]
			cardText := formatTorrentCard(t)
			kb := b.torrentKeyboard(hash, false)
			_ = b.tg.EditMessageText(ctx, chatID, cb.Message.MessageID, cardText, "Markdown", kb)
		}
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "▶️ Started", false)
		return
	}

	if strings.HasPrefix(data, "qb_del:") {
		hash := strings.TrimPrefix(data, "qb_del:")
		_ = b.qb.Delete(ctx, hash, true)
		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		b.mu.Lock()
		if ids, ok := b.lastStatusMessageIDs[chatID]; ok {
			filtered := make([]int64, 0, len(ids))
			for _, id := range ids {
				if id != cb.Message.MessageID {
					filtered = append(filtered, id)
				}
			}
			b.lastStatusMessageIDs[chatID] = filtered
		}
		b.mu.Unlock()
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "🗑 Deleted with files", false)
		return
	}

	if data == "qb_clean" {
		b.mu.Lock()
		if cancel, ok := b.statusCancelFuncs[chatID]; ok {
			cancel()
			delete(b.statusCancelFuncs, chatID)
		}
		ids := b.lastStatusMessageIDs[chatID]
		delete(b.lastStatusMessageIDs, chatID)
		b.mu.Unlock()

		for _, id := range ids {
			_ = b.tg.DeleteMessage(ctx, chatID, id)
		}
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "🧹 Cleaned", false)
		return
	}
}

func (b *Bot) finalizeDownload(ctx context.Context, chatID int64, catKey, targetDir string, p *PendingDownload) {
	_ = os.MkdirAll(targetDir, 0755)

	var msgText string
	success := false

	switch p.Type {
	case "magnet":
		err := b.qb.AddMagnet(ctx, p.Magnet, targetDir, catKey)
		if err != nil {
			b.logger.Error("Failed to add magnet to qBittorrent", "err", err)
			msgText = fmt.Sprintf("❌ Failed to add magnet to qBit: %s", err.Error())
		} else {
			limitInfo := ""
			// If originated from TorrServer, apply configured speed limit
			if p.Source == "torrserver" && b.cfg.TorrServerDLLimitMbit > 0 {
				if hash := extractBtih(p.Magnet); hash != "" {
					limitBytes := int64(b.cfg.TorrServerDLLimitMbit * 1024 * 1024 / 8)
					b.state.SetPendingLimit(hash, state.PendingLimit{
						Limit:       limitBytes,
						Mbit:        b.cfg.TorrServerDLLimitMbit,
						Attempts:    0,
						NextAttempt: 0,
					})
					limitInfo = fmt.Sprintf("\nSpeed Limit: %d Mbit/s (Pending)", b.cfg.TorrServerDLLimitMbit)
					_ = b.state.Save()
				}
			}

			msgText = fmt.Sprintf("✅ Magnet added to qBit.\nDir: `%s`%s", targetDir, limitInfo)
			success = true
		}

	case "torrent_url":
		var fileData []byte = p.FileData
		var fName string = p.FileName
		var addErr error
		if len(fileData) == 0 {
			torrentURL := p.URL
			if torrentURL == "" {
				torrentURL = p.Magnet
			}
			data, fn, fetchErr := downloadTorrentBytes(ctx, torrentURL)
			if fetchErr == nil && isTorrentData(data) {
				fileData = data
				fName = fn
			} else if fetchErr != nil {
				b.logger.Warn("Could not download .torrent file directly, passing URL to qBittorrent", "url", torrentURL, "err", fetchErr)
				addErr = b.qb.AddMagnet(ctx, torrentURL, targetDir, catKey)
			}
		}
		if len(fileData) > 0 {
			if fName == "" {
				fName = "download.torrent"
			}
			b.logger.Info("Uploading .torrent file to qBittorrent", "filename", fName, "bytes", len(fileData))
			addErr = b.qb.AddTorrentFile(ctx, fName, fileData, targetDir, catKey)
		}

		if addErr != nil {
			b.logger.Error("Failed to add torrent to qBittorrent", "err", addErr)
			msgText = fmt.Sprintf("❌ Failed to add torrent to qBit: %s", addErr.Error())
		} else {
			msgText = fmt.Sprintf("✅ Torrent added to qBit.\nDir: `%s`", targetDir)
			success = true
		}

	case "metube":
		quality := p.MeTubeQuality
		if quality == "" {
			quality = b.cfg.MeTubeDefaultQuality
		}
		format := "mp4"
		if p.MeTubeFormat == "audio" {
			format = "mp3"
		}

		err := b.metube.AddDownload(ctx, metube.AddRequest{
			URL:       p.URL,
			Quality:   quality,
			Format:    format,
			Folder:    catKey,
			AutoStart: true,
		})

		if err != nil {
			b.logger.Error("Failed to add download to MeTube", "err", err)
			failedJob := &FailedMeTubeJob{
				URL:      p.URL,
				Folder:   catKey,
				Quality:  quality,
				Format:   p.MeTubeFormat,
				ErrorMsg: err.Error(),
				ChatID:   chatID,
				UserID:   p.UserID,
			}
			token := b.registerFailedMeTubeJob(failedJob)
			errMsg := fmt.Sprintf("❌ *MeTube Download Error*\n\n*URL:* %s\n*Message:* `%s`",
				telegram.EscapeMarkdown(p.URL), telegram.EscapeMarkdown(err.Error()))
			sentMsg, _ := b.tg.SendMessage(ctx, chatID, errMsg, "Markdown", b.meTubeErrorKeyboard(token))
			if sentMsg != nil {
				b.mu.Lock()
				failedJob.MessageID = sentMsg.MessageID
				b.mu.Unlock()
			}
			return
		}

		token := fmt.Sprintf("%x", time.Now().UnixNano())
		bar := renderProgressBar(0, 10)
		msgText = fmt.Sprintf("⏳ *Starting download on MeTube!*\nQuality: `%s` (%s)\n\n%s 0.0%%\n\n_The bot will send the video file directly to you here once completed._",
			quality, p.MeTubeFormat, bar)
		success = true

		// Trigger Jellyfin library refresh if enabled
		if b.cfg.JellyfinEnabled {
			go func() {
				_ = b.jellyfin.RefreshLibrary(context.Background())
			}()
		}

		sentMsg, err := b.tg.SendMessage(ctx, chatID, msgText, "Markdown", b.meTubeCancelKeyboard(token))
		if err == nil && sentMsg != nil {
			b.mu.Lock()
			b.activeMeTubeJobs = append(b.activeMeTubeJobs, ActiveMeTubeJob{
				Token:       token,
				URL:         p.URL,
				ChatID:      chatID,
				UserID:      p.UserID,
				StatusMsgID: sentMsg.MessageID,
				Folder:      catKey,
				Quality:     quality,
				Format:      p.MeTubeFormat,
				SubmittedAt: time.Now(),
				LastPercent: 0,
			})
			b.mu.Unlock()
		}
		return

	case "file", "video", "photo":
		var fileData []byte
		var fName string = p.FileName
		if len(p.FileData) > 0 {
			fileData = p.FileData
		} else {
			fileInfo, err := b.tg.GetFile(ctx, p.FileID)
			if err != nil {
				b.logger.Error("Could not get file info from Telegram", "err", err)
				msgText = "❌ Could not get file from Telegram (Check 20MB limit)."
				break
			}

			_, _ = b.tg.SendMessage(ctx, chatID, "📥 Downloading file from Telegram...", "Markdown", nil)

			var dlErr error
			fileData, dlErr = b.tg.DownloadFile(ctx, fileInfo.FilePath)
			if dlErr != nil {
				b.logger.Error("Could not download file data from Telegram", "err", dlErr)
				msgText = "❌ Failed to download file from Telegram."
				break
			}
		}

		isTorrent := strings.HasSuffix(strings.ToLower(fName), ".torrent") || isTorrentData(fileData)
		if isTorrent {
			if !strings.HasSuffix(strings.ToLower(fName), ".torrent") {
				fName = fName + ".torrent"
			}
			err := b.qb.AddTorrentFile(ctx, fName, fileData, targetDir, catKey)
			if err != nil {
				b.logger.Error("Failed to upload torrent to qBittorrent", "err", err)
				msgText = fmt.Sprintf("❌ Failed to add torrent to qBit: %s", err.Error())
			} else {
				msgText = fmt.Sprintf("✅ Torrent added to qBit.\nDir: `%s`", targetDir)
				success = true
			}
		} else {
			// Save media directly to destination directory
			cleanName := filepath.Base(fName)
			destPath := filepath.Join(targetDir, cleanName)
			if err := os.WriteFile(destPath, fileData, 0644); err != nil {
				b.logger.Error("Failed to save media file to disk", "err", err)
				msgText = "❌ Failed to save media file to destination."
			} else {
				msgText = fmt.Sprintf("✅ Media saved to `%s`", destPath)
				success = true
			}
		}
	}

	sentMsg, err := b.tg.SendMessage(ctx, chatID, msgText, "Markdown", nil)
	if success && err == nil && sentMsg != nil {
		b.mu.Lock()
		b.pendingDeletions = append(b.pendingDeletions, PendingDeletion{
			ChatID:    chatID,
			MessageID: sentMsg.MessageID,
			Expires:   time.Now().Add(b.cfg.NotificationCleanupTime).Unix(),
		})
		b.mu.Unlock()
	}
}

func (b *Bot) handleTorrServerDownload(ctx context.Context, chatID int64, hash string, origMsg *telegram.Message, userID int64) bool {
	torrents, err := b.torrserver.ListTorrents(ctx)
	if err != nil {
		_, _ = b.tg.SendMessage(ctx, chatID, "❌ Could not reach TorrServer.", "", nil)
		return false
	}

	var target *torrserver.Torrent
	for i := range torrents {
		if strings.EqualFold(torrents[i].Hash, hash) {
			target = &torrents[i]
			break
		}
	}

	if target == nil {
		_, _ = b.tg.SendMessage(ctx, chatID, "❌ Torrent not found in TorrServer.", "", nil)
		return false
	}

	magnet := target.Magnet
	if magnet == "" {
		sanitizedHash := regexp.MustCompile(`[^a-fA-F0-9]`).ReplaceAllString(target.Hash, "")
		title := url.QueryEscape(target.Title)
		magnet = fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", sanitizedHash, title)
	}

	b.mu.Lock()
	b.pendingDownloads[chatID] = &PendingDownload{
		Type:    "magnet",
		Magnet:  magnet,
		DiskIdx: b.cfg.DefaultDiskIdx,
		Source:  "torrserver",
		UserID:  userID,
		Expires: time.Now().Unix() + PendingDownloadExpirySecs,
	}
	b.mu.Unlock()

	safeTitle := telegram.EscapeMarkdown(target.Title)
	newText := fmt.Sprintf("📥 From TorrServer: `%s`\nChoose destination:", safeTitle)
	kb := b.categoryKeyboard(b.cfg.DefaultDiskIdx)

	if origMsg != nil && len(origMsg.Photo) > 0 {
		_ = b.tg.EditMessageCaption(ctx, chatID, origMsg.MessageID, newText, "Markdown", kb)
	} else if origMsg != nil {
		_ = b.tg.EditMessageText(ctx, chatID, origMsg.MessageID, newText, "Markdown", kb)
	}

	b.state.AddNotifiedTorrHash(hash)
	_ = b.state.Save()
	return true
}

func (b *Bot) deleteOtherTorrServerMessages(ctx context.Context, hash string, actingChatID int64) {
	b.mu.Lock()
	entries, ok := b.torrServerMsgIDs[hash]
	b.mu.Unlock()

	if !ok {
		return
	}

	for _, entry := range entries {
		if entry.ChatID != actingChatID {
			_ = b.tg.DeleteMessage(ctx, entry.ChatID, entry.MessageID)
		}
	}
}

type trackedTorrentMsg struct {
	Hash      string
	MessageID int64
	LastText  string
	IsPaused  bool
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; exp++ {
		div *= unit
		n /= unit
	}
	units := []string{"KB", "MB", "GB", "TB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), units[exp])
}

func formatSpeed(b int64) string {
	if b <= 0 {
		return "0 B/s"
	}
	return formatBytes(b) + "/s"
}

func formatETA(sec int64) string {
	if sec <= 0 || sec >= 8640000 {
		return "∞"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%02dh %02dm", h, m)
	}
	return fmt.Sprintf("%02dm %02ds", m, s)
}

func renderProgressBar(prog float64, width int) string {
	if prog < 0 {
		prog = 0
	}
	if prog > 1 {
		prog = 1
	}
	filled := int(prog * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func formatState(state string) (string, bool) {
	switch state {
	case "downloading":
		return "📥 Downloading", false
	case "pausedDL":
		return "⏸ Paused (DL)", true
	case "pausedUP":
		return "⏸ Paused (Complete)", true
	case "uploading", "seeding":
		return "📤 Seeding", false
	case "queuedDL", "queuedUP":
		return "⏳ Queued", false
	case "checkingDL", "checkingUP", "checkingResumeData":
		return "🔍 Checking", false
	case "stalledDL":
		return "⏳ Stalled (DL)", false
	case "stalledUP":
		return "📤 Stalled (Seed)", false
	case "moving":
		return "📦 Moving", false
	case "error", "missingFiles":
		return "⚠️ Error", false
	default:
		isPaused := strings.Contains(strings.ToLower(state), "paused") || strings.Contains(strings.ToLower(state), "stopped")
		if isPaused {
			return "⏸ " + state, true
		}
		return state, false
	}
}

func formatTorrentCard(t qbittorrent.TorrentInfo) string {
	stateStr, _ := formatState(t.State)
	progPct := t.Progress * 100
	bar := renderProgressBar(t.Progress, 12)
	name := telegram.EscapeMarkdown(cleanTorrentName(t.Name))

	sizeDownloaded := formatBytes(t.Downloaded)
	if t.Downloaded == 0 && t.Progress > 0 && t.Size > 0 {
		sizeDownloaded = formatBytes(int64(float64(t.Size) * t.Progress))
	}
	sizeTotal := formatBytes(t.Size)

	return fmt.Sprintf("🎬 *%s*\n\n`[%s]` *%.1f%%*\n\n• *Status:* %s\n• *Size:* `%s / %s`\n• *Speed:* `⬇️ %s | ⬆️ %s`\n• *ETA:* `%s`",
		name, bar, progPct, stateStr, sizeDownloaded, sizeTotal,
		formatSpeed(t.DLSpeed), formatSpeed(t.UPSpeed), formatETA(t.ETA))
}

func (b *Bot) torrentKeyboard(hash string, isPaused bool) *telegram.InlineKeyboardMarkup {
	var pauseBtn telegram.InlineKeyboardButton
	if isPaused {
		pauseBtn = telegram.InlineKeyboardButton{
			Text:         "▶️ Start",
			CallbackData: "qb_res:" + hash,
		}
	} else {
		pauseBtn = telegram.InlineKeyboardButton{
			Text:         "⏸ Pause",
			CallbackData: "qb_pause:" + hash,
		}
	}

	delBtn := telegram.InlineKeyboardButton{
		Text:         "🗑 Delete with File",
		CallbackData: "qb_del:" + hash,
	}

	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{pauseBtn, delBtn},
		},
	}
}

func (b *Bot) statusCleanKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "👌 OK (Clean)", CallbackData: "qb_clean"},
			},
		},
	}
}

func (b *Bot) sendStatusMessage(ctx context.Context, chatID int64) {
	// Clean up previous status messages and stop active updater
	b.mu.Lock()
	if cancel, ok := b.statusCancelFuncs[chatID]; ok {
		cancel()
		delete(b.statusCancelFuncs, chatID)
	}
	if oldIDs, exists := b.lastStatusMessageIDs[chatID]; exists {
		for _, id := range oldIDs {
			_ = b.tg.DeleteMessage(ctx, chatID, id)
		}
		delete(b.lastStatusMessageIDs, chatID)
	}
	b.mu.Unlock()

	filter := b.cfg.StatusFilter
	torrents, err := b.qb.GetTorrents(ctx, filter, "")
	if err != nil {
		b.logger.Error("Failed to fetch torrent status", "err", err)
		msg, _ := b.tg.SendMessage(ctx, chatID, fmt.Sprintf("❌ Error querying qBittorrent: %s", err.Error()), "", b.statusCleanKeyboard())
		if msg != nil {
			b.mu.Lock()
			b.lastStatusMessageIDs[chatID] = append(b.lastStatusMessageIDs[chatID], msg.MessageID)
			b.mu.Unlock()
		}
		return
	}

	if b.cfg.StatusFilter == "downloading" {
		filtered := make([]qbittorrent.TorrentInfo, 0, len(torrents))
		for _, t := range torrents {
			if strings.Contains(strings.ToUpper(t.State), "DL") || strings.ToLower(t.State) == "downloading" {
				filtered = append(filtered, t)
			}
		}
		torrents = filtered
	}

	if b.cfg.StatusShowLimit > 0 && len(torrents) > b.cfg.StatusShowLimit {
		torrents = torrents[:b.cfg.StatusShowLimit]
	}

	if len(torrents) == 0 {
		msg, _ := b.tg.SendMessage(ctx, chatID, "📭 No active torrents.", "", b.statusCleanKeyboard())
		if msg != nil {
			b.mu.Lock()
			b.lastStatusMessageIDs[chatID] = append(b.lastStatusMessageIDs[chatID], msg.MessageID)
			b.mu.Unlock()
		}
		return
	}

	tracked := make([]trackedTorrentMsg, 0, len(torrents))

	for _, t := range torrents {
		_, isPaused := formatState(t.State)
		cardText := formatTorrentCard(t)
		kb := b.torrentKeyboard(t.Hash, isPaused)
		sentMsg, err := b.tg.SendMessage(ctx, chatID, cardText, "Markdown", kb)
		if err == nil && sentMsg != nil {
			b.mu.Lock()
			b.lastStatusMessageIDs[chatID] = append(b.lastStatusMessageIDs[chatID], sentMsg.MessageID)
			b.mu.Unlock()
			tracked = append(tracked, trackedTorrentMsg{
				Hash:      t.Hash,
				MessageID: sentMsg.MessageID,
				LastText:  cardText,
				IsPaused:  isPaused,
			})
		}
	}

	summaryText := fmt.Sprintf("📊 *qBit Status* (%d active)\n", len(torrents))
	links := make([]string, 0)
	if b.cfg.QBURL != "" {
		links = append(links, fmt.Sprintf("[qBittorrent](%s)", b.cfg.QBURL))
	}
	if b.cfg.MeTubeEnabled && b.cfg.MeTubeURL != "" {
		if b.cfg.MeTubePublicURL != "" {
			links = append(links, fmt.Sprintf("[MeTube (LAN)](%s)", b.cfg.MeTubeURL))
			links = append(links, fmt.Sprintf("[MeTube (Public)](%s)", b.cfg.MeTubePublicURL))
		} else {
			links = append(links, fmt.Sprintf("[MeTube](%s)", b.cfg.MeTubeURL))
		}
	}
	if b.cfg.TorrServerEnabled && b.cfg.TorrServerURL != "" {
		links = append(links, fmt.Sprintf("[TorrServer](%s)", b.cfg.TorrServerURL))
	}
	if b.cfg.JellyfinEnabled && b.cfg.JellyfinURL != "" {
		links = append(links, fmt.Sprintf("[Jellyfin](%s)", b.cfg.JellyfinURL))
	}
	if len(links) > 0 {
		summaryText += "\n🔗 " + strings.Join(links, " | ")
	}

	sumMsg, err := b.tg.SendMessage(ctx, chatID, summaryText, "Markdown", b.statusCleanKeyboard())
	if err == nil && sumMsg != nil {
		b.mu.Lock()
		b.lastStatusMessageIDs[chatID] = append(b.lastStatusMessageIDs[chatID], sumMsg.MessageID)
		b.mu.Unlock()
	}

	// Start live updater for this chat
	liveCtx, liveCancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.statusCancelFuncs[chatID] = liveCancel
	b.mu.Unlock()

	go b.runStatusLiveUpdater(liveCtx, chatID, tracked)
}

func (b *Bot) runStatusLiveUpdater(ctx context.Context, chatID int64, tracked []trackedTorrentMsg) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeoutTimer.C:
			return
		case <-ticker.C:
			torrents, err := b.qb.GetTorrents(ctx, "", "")
			if err != nil {
				continue
			}

			torrentMap := make(map[string]qbittorrent.TorrentInfo, len(torrents))
			for _, t := range torrents {
				torrentMap[t.Hash] = t
			}

			for i := range tracked {
				item := &tracked[i]
				t, found := torrentMap[item.Hash]
				if !found {
					continue
				}

				_, isPaused := formatState(t.State)
				newText := formatTorrentCard(t)
				if newText != item.LastText || isPaused != item.IsPaused {
					kb := b.torrentKeyboard(t.Hash, isPaused)
					_ = b.tg.EditMessageText(ctx, chatID, item.MessageID, newText, "Markdown", kb)
					item.LastText = newText
					item.IsPaused = isPaused
				}
			}
		}
	}
}

func (b *Bot) checkTorrentCompletions(ctx context.Context) {
	allTorrents, err := b.qb.GetTorrents(ctx, "all", "")
	if err != nil {
		b.logger.Warn("Failed to fetch torrent list for completion check", "err", err)
		return
	}

	activeMap := make(map[string]struct{}, len(allTorrents))
	for _, t := range allTorrents {
		activeMap[t.Hash] = struct{}{}
	}
	b.state.PruneNotifiedTorrents(activeMap)

	completed, err := b.qb.GetTorrents(ctx, "completed", "")
	if err != nil {
		return
	}

	action := strings.ToLower(strings.TrimSpace(b.cfg.ActionOnComplete))
	knownChats := b.state.GetKnownChatIDs()

	for _, t := range completed {
		if b.state.IsTorrentNotified(t.Hash) {
			continue
		}

		actionMsg := " (Paused)"
		switch action {
		case "remove_data", "delete_data":
			_ = b.qb.Delete(ctx, t.Hash, true)
			actionMsg = " (Removed with data)"
		case "remove", "delete":
			_ = b.qb.Delete(ctx, t.Hash, false)
			actionMsg = " (Removed from list)"
		default:
			_ = b.qb.Pause(ctx, t.Hash)
		}

		safeName := telegram.EscapeMarkdown(t.Name)
		notifText := fmt.Sprintf("✅ *Finished:* `%s`%s", safeName, telegram.EscapeMarkdown(actionMsg))

		for _, cid := range knownChats {
			_, _ = b.tg.SendMessage(ctx, cid, notifText, "Markdown", nil)
		}

		b.state.SetTorrentNotified(t.Hash)
		_ = b.state.Save()

		if b.cfg.JellyfinEnabled {
			go func() {
				_ = b.jellyfin.RefreshLibrary(context.Background())
			}()
		}
	}
}

func (b *Bot) checkTorrServer(ctx context.Context) {
	torrents, err := b.torrserver.ListTorrents(ctx)
	if err != nil {
		b.logger.Warn("Failed to list TorrServer torrents", "err", err)
		b.torrServerFailCount++
		if b.torrServerFailCount >= 5 {
			knownChats := b.state.GetKnownChatIDs()
			for _, cid := range knownChats {
				_, _ = b.tg.SendMessage(ctx, cid, "⚠️ TorrServer unavailable after 5 attempts. Please check the service.", "", nil)
			}
			b.torrServerFailCount = 0
		}
		return
	}
	b.torrServerFailCount = 0

	activeMap := make(map[string]struct{}, len(torrents))
	for _, t := range torrents {
		activeMap[t.Hash] = struct{}{}
	}
	b.state.PruneNotifiedTorrHashes(activeMap)

	knownChats := b.state.GetKnownChatIDs()

	for _, t := range torrents {
		if b.state.IsTorrHashNotified(t.Hash) {
			continue
		}

		name := t.Title
		if name == "" || name == "Unknown" {
			continue
		}

		fileInfoStr := ""
		totalSize := t.GetTotalSize()
		files := t.ParseFiles()

		if len(files) == 1 {
			fName := filepath.Base(files[0].Path)
			fSizeMb := float64(files[0].Length) / 1024 / 1024
			fileInfoStr = fmt.Sprintf("\n📄 File: `%s`\n📦 Size: %.1f MB", telegram.EscapeMarkdown(fName), fSizeMb)
		} else if len(files) > 1 {
			totalSizeMb := float64(totalSize) / 1024 / 1024
			fileInfoStr = fmt.Sprintf("\n📺 Episodes: %d\n📦 Total Size: %.1f MB", len(files), totalSizeMb)
		} else if totalSize > 0 {
			totalSizeMb := float64(totalSize) / 1024 / 1024
			fileInfoStr = fmt.Sprintf("\n📦 Size: %.1f MB", totalSizeMb)
		}

		safeName := telegram.EscapeMarkdown(name)
		msg := fmt.Sprintf("🎬 *New in TorrServer:*\n\n`%s`%s\n\nDownload to qBit?", safeName, fileInfoStr)

		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					{Text: "✅ Download", CallbackData: fmt.Sprintf("ts_dl:%s", t.Hash)},
					{Text: "🙈 Ignore", CallbackData: fmt.Sprintf("ts_ignore:%s", t.Hash)},
				},
			},
		}

		for _, cid := range knownChats {
			var sentMsg *telegram.Message
			var err error
			if t.Poster != "" {
				sentMsg, err = b.tg.SendPhoto(ctx, cid, t.Poster, msg, "Markdown", kb)
			}
			if t.Poster == "" || err != nil {
				sentMsg, _ = b.tg.SendMessage(ctx, cid, msg, "Markdown", kb)
			}

			if sentMsg != nil {
				b.mu.Lock()
				b.torrServerMsgIDs[t.Hash] = append(b.torrServerMsgIDs[t.Hash], PendingDeletion{
					ChatID:    cid,
					MessageID: sentMsg.MessageID,
				})
				b.mu.Unlock()
			}
		}

		b.state.AddNotifiedTorrHash(t.Hash)
		_ = b.state.Save()
	}
}

func (b *Bot) checkPendingLimits(ctx context.Context) {
	limits := b.state.GetPendingLimits()
	now := time.Now().Unix()

	for hash, data := range limits {
		if now < data.NextAttempt {
			continue
		}

		data.Attempts++
		data.NextAttempt = now + 5

		info, err := b.qb.GetTorrents(ctx, "", hash)
		if err == nil && len(info) > 0 {
			if err := b.qb.SetDownloadLimit(ctx, hash, data.Limit); err == nil {
				b.logger.Info("Applied download limit to torrent", "hash", hash, "mbit", data.Mbit)
				b.state.DeletePendingLimit(hash)
				_ = b.state.Save()
				continue
			}
		}

		if data.Attempts >= MaxPendingLimitAttempts {
			b.logger.Warn("Failed to apply download limit after max attempts, giving up", "hash", hash)
			b.state.DeletePendingLimit(hash)
			_ = b.state.Save()
		} else {
			b.state.SetPendingLimit(hash, data)
		}
	}
}

func (b *Bot) cleanupExpired(ctx context.Context) {
	now := time.Now().Unix()

	b.mu.Lock()
	// Clean up pending downloads
	for cid, p := range b.pendingDownloads {
		if p.Expires > 0 && now >= p.Expires {
			delete(b.pendingDownloads, cid)
			go func(chatID int64) {
				_, _ = b.tg.SendMessage(ctx, chatID, "⏱ Download request expired. Please try again.", "", nil)
			}(cid)
		}
	}

	// Clean up pending deletions
	activeDeletions := make([]PendingDeletion, 0, len(b.pendingDeletions))
	for _, pd := range b.pendingDeletions {
		if now >= pd.Expires {
			go func(del PendingDeletion) {
				_ = b.tg.DeleteMessage(ctx, del.ChatID, del.MessageID)
			}(pd)
		} else {
			activeDeletions = append(activeDeletions, pd)
		}
	}
	b.pendingDeletions = activeDeletions

	// Clean up expired failed MeTube jobs
	for token, fj := range b.failedMeTubeJobs {
		if time.Since(fj.CreatedAt) > 24*time.Hour {
			delete(b.failedMeTubeJobs, token)
		}
	}
	b.mu.Unlock()
}

func (b *Bot) categoryKeyboard(currentDiskIdx int) *telegram.InlineKeyboardMarkup {
	rows := make([][]telegram.InlineKeyboardButton, 0)

	// Add categories
	for k, label := range b.cfg.Categories {
		rows = append(rows, []telegram.InlineKeyboardButton{
			{Text: label, CallbackData: "dl:" + k},
		})
	}

	// Add disk selection row if disks exist
	if len(b.cfg.Disks) > 0 {
		diskRow := make([]telegram.InlineKeyboardButton, 0, len(b.cfg.Disks))
		for idx, d := range b.cfg.Disks {
			freeInfo := ""
			if freeBytes, err := disk.GetFreeSpace(d); err == nil {
				freeGb := float64(freeBytes) / 1024 / 1024 / 1024
				freeInfo = fmt.Sprintf(" [%.1fGB]", freeGb)
			}

			label := fmt.Sprintf("💾 D%d%s", idx+1, freeInfo)
			if idx == currentDiskIdx {
				label = fmt.Sprintf("✅ Disk %d%s", idx+1, freeInfo)
			}

			diskRow = append(diskRow, telegram.InlineKeyboardButton{
				Text:         label,
				CallbackData: fmt.Sprintf("set_disk:%d", idx),
			})
		}
		rows = append(rows, diskRow)
	}

	// Add Cancel button row
	rows = append(rows, []telegram.InlineKeyboardButton{
		{Text: "❌ Cancel", CallbackData: "cancel_dl"},
	})

	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) qualityKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "🌟 Best", CallbackData: "metube_q:best:video"},
				{Text: "💎 4K (2160p)", CallbackData: "metube_q:2160:video"},
			},
			{
				{Text: "🎬 1080p", CallbackData: "metube_q:1080:video"},
				{Text: "📺 720p", CallbackData: "metube_q:720:video"},
			},
			{
				{Text: "📱 480p", CallbackData: "metube_q:480:video"},
				{Text: "💾 360p", CallbackData: "metube_q:360:video"},
			},
			{
				{Text: "🎵 Audio (MP3)", CallbackData: "metube_q:best:audio"},
				{Text: "❌ Cancel", CallbackData: "cancel_dl"},
			},
		},
	}
}

func (b *Bot) formatKeyboard() *telegram.InlineKeyboardMarkup {
	return b.qualityKeyboard()
}

func (b *Bot) removeActiveMeTubeJob(urlStr string) {
	filtered := make([]ActiveMeTubeJob, 0, len(b.activeMeTubeJobs))
	for _, j := range b.activeMeTubeJobs {
		if j.URL != urlStr {
			filtered = append(filtered, j)
		}
	}
	b.activeMeTubeJobs = filtered
}

func (b *Bot) registerFailedMeTubeJob(job *FailedMeTubeJob) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	token := fmt.Sprintf("%x", time.Now().UnixNano())
	job.Token = token
	job.CreatedAt = time.Now()
	b.failedMeTubeJobs[token] = job
	return token
}

func (b *Bot) meTubeErrorKeyboard(token string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "🔄 Retry", CallbackData: "mt_retry:" + token},
				{Text: "❌ Cancel", CallbackData: "mt_cancel:" + token},
			},
		},
	}
}

func (b *Bot) meTubeCancelKeyboard(token string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "❌ Cancel", CallbackData: "mt_cancel_active:" + token},
			},
		},
	}
}

func (b *Bot) meTubeVideoKeyboard(token string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "🗑 Delete from MeTube", CallbackData: "mt_del_file:" + token},
				{Text: "💾 Keep in MeTube", CallbackData: "mt_keep_file:" + token},
			},
		},
	}
}

func (b *Bot) handleMeTubeRetry(ctx context.Context, cb *telegram.CallbackQuery, token string) {
	chatID := cb.Message.Chat.ID
	callerID := cb.From.ID

	b.mu.Lock()
	job, exists := b.failedMeTubeJobs[token]
	b.mu.Unlock()

	if !exists {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Download session expired.", true)
		return
	}

	if !b.cfg.IsUserAllowed(callerID) && job.UserID != callerID {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.", true)
		return
	}

	_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "🔄 Retrying download...", false)

	// Clean up old failed item from MeTube if it exists
	go func(itemID, itemURL string) {
		_ = b.metube.DeleteByItemOrURL(context.Background(), itemID, itemURL, false)
	}(job.ItemID, job.URL)

	quality := job.Quality
	if quality == "" {
		quality = b.cfg.MeTubeDefaultQuality
	}
	format := "mp4"
	if job.Format == "audio" {
		format = "mp3"
	}

	err := b.metube.AddDownload(ctx, metube.AddRequest{
		URL:       job.URL,
		Quality:   quality,
		Format:    format,
		Folder:    job.Folder,
		AutoStart: true,
	})

	if err != nil {
		b.logger.Error("Failed to retry MeTube download", "err", err)
		newErrMsg := fmt.Sprintf("❌ *MeTube Download Error*\n\n*URL:* %s\n*Message:* `%s`",
			telegram.EscapeMarkdown(job.URL), telegram.EscapeMarkdown(err.Error()))
		b.mu.Lock()
		job.ErrorMsg = err.Error()
		b.mu.Unlock()
		_ = b.tg.EditMessageText(ctx, chatID, cb.Message.MessageID, newErrMsg, "Markdown", b.meTubeErrorKeyboard(token))
		return
	}

	newToken := fmt.Sprintf("%x", time.Now().UnixNano())
	b.mu.Lock()
	delete(b.failedMeTubeJobs, token)
	b.activeMeTubeJobs = append(b.activeMeTubeJobs, ActiveMeTubeJob{
		Token:       newToken,
		URL:         job.URL,
		ChatID:      chatID,
		UserID:      callerID,
		StatusMsgID: cb.Message.MessageID,
		Folder:      job.Folder,
		Quality:     job.Quality,
		Format:      job.Format,
		SubmittedAt: time.Now(),
		LastPercent: 0,
	})
	b.mu.Unlock()

	bar := renderProgressBar(0, 10)
	retryingText := fmt.Sprintf("⏳ *Starting download on MeTube!*\nQuality: `%s` (%s)\n\n%s 0.0%%\n\n_The bot will send the video file directly to you here once completed._",
		quality, job.Format, bar)
	_ = b.tg.EditMessageText(ctx, chatID, cb.Message.MessageID, retryingText, "Markdown", b.meTubeCancelKeyboard(newToken))
}

func (b *Bot) handleMeTubeCancel(ctx context.Context, cb *telegram.CallbackQuery, token string) {
	chatID := cb.Message.Chat.ID
	callerID := cb.From.ID

	b.mu.Lock()
	job, exists := b.failedMeTubeJobs[token]
	if exists {
		delete(b.failedMeTubeJobs, token)
	}
	b.mu.Unlock()

	if !exists {
		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Removed.", false)
		return
	}

	if !b.cfg.IsUserAllowed(callerID) && job.UserID != callerID {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.", true)
		return
	}

	_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "🗑 Removed", false)

	go func(itemID, itemURL string) {
		_ = b.metube.DeleteByItemOrURL(context.Background(), itemID, itemURL, true)
	}(job.ItemID, job.URL)

	_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
}

func (b *Bot) handleMeTubeCancelActive(ctx context.Context, cb *telegram.CallbackQuery, token string) {
	chatID := cb.Message.Chat.ID
	callerID := cb.From.ID

	b.mu.Lock()
	var targetJob *ActiveMeTubeJob
	newJobs := make([]ActiveMeTubeJob, 0, len(b.activeMeTubeJobs))
	for _, j := range b.activeMeTubeJobs {
		if j.Token == token {
			copyJ := j
			targetJob = &copyJ
		} else {
			newJobs = append(newJobs, j)
		}
	}
	if targetJob != nil {
		b.activeMeTubeJobs = newJobs
	}
	b.mu.Unlock()

	if targetJob == nil {
		_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Download expired or already completed.", false)
		return
	}

	if !b.cfg.IsUserAllowed(callerID) && targetJob.UserID != callerID {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.", true)
		return
	}

	_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "❌ Cancelled", false)

	go func(itemID, itemURL string) {
		_ = b.metube.DeleteByItemOrURL(context.Background(), itemID, itemURL, true)
	}(targetJob.MeTubeItemID, targetJob.URL)

	_ = b.tg.DeleteMessage(ctx, chatID, cb.Message.MessageID)
}

func (b *Bot) handleMeTubeDeleteFile(ctx context.Context, cb *telegram.CallbackQuery, token string) {
	chatID := cb.Message.Chat.ID
	callerID := cb.From.ID

	b.mu.Lock()
	item, exists := b.deliveredMeTubeItems[token]
	if exists {
		delete(b.deliveredMeTubeItems, token)
	}
	b.mu.Unlock()

	if !exists {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Action expired or already processed.", true)
		_ = b.tg.EditMessageReplyMarkup(ctx, chatID, cb.Message.MessageID, nil)
		return
	}

	if !b.cfg.IsUserAllowed(callerID) && item.UserID != callerID {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.", true)
		return
	}

	_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "🗑 Deleting from MeTube...", false)

	go func(itemID, itemURL string) {
		_ = b.metube.DeleteByItemOrURL(context.Background(), itemID, itemURL, true)
	}(item.ItemID, item.URL)

	doneKb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "🗑 Deleted from MeTube", CallbackData: "noop"},
			},
		},
	}
	_ = b.tg.EditMessageReplyMarkup(ctx, chatID, cb.Message.MessageID, doneKb)
}

func (b *Bot) handleMeTubeKeepFile(ctx context.Context, cb *telegram.CallbackQuery, token string) {
	chatID := cb.Message.Chat.ID
	callerID := cb.From.ID

	b.mu.Lock()
	item, exists := b.deliveredMeTubeItems[token]
	if exists {
		delete(b.deliveredMeTubeItems, token)
	}
	b.mu.Unlock()

	if !exists {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Action expired or already processed.", true)
		_ = b.tg.EditMessageReplyMarkup(ctx, chatID, cb.Message.MessageID, nil)
		return
	}

	if !b.cfg.IsUserAllowed(callerID) && item.UserID != callerID {
		_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.", true)
		return
	}

	_ = b.tg.AnswerCallbackQuery(ctx, cb.ID, "💾 Kept in MeTube", false)

	keptKb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "💾 Kept in MeTube", CallbackData: "noop"},
			},
		},
	}
	_ = b.tg.EditMessageReplyMarkup(ctx, chatID, cb.Message.MessageID, keptKb)
}

func (b *Bot) checkMeTubeCompletions(ctx context.Context) {
	b.mu.Lock()
	if len(b.activeMeTubeJobs) == 0 {
		b.mu.Unlock()
		return
	}
	jobs := make([]ActiveMeTubeJob, len(b.activeMeTubeJobs))
	copy(jobs, b.activeMeTubeJobs)
	b.mu.Unlock()

	hist, err := b.metube.GetHistory(ctx)
	if err != nil {
		return
	}

	for _, job := range jobs {
		var doneItem *metube.HistoryItem
		var errorItem *metube.HistoryItem
		var queueItem *metube.HistoryItem

		// Check done items
		for _, item := range hist.Done {
			matches := item.URL == job.URL || (item.ID != "" && strings.Contains(job.URL, item.ID))
			if !matches {
				continue
			}
			if item.Timestamp > 0 {
				itemSec := item.Timestamp / 1e9
				if itemSec < job.SubmittedAt.Unix()-10 {
					continue
				}
			}
			if item.Status == "finished" {
				doneItem = &item
				break
			} else if item.Status == "error" {
				errorItem = &item
				break
			}
		}

		// Also check queue items for errors or active download progress
		if doneItem == nil && errorItem == nil {
			for _, item := range hist.Queue {
				matches := item.URL == job.URL || (item.ID != "" && strings.Contains(job.URL, item.ID))
				if !matches {
					continue
				}
				if item.Status == "error" {
					errorItem = &item
					break
				}
				queueItem = &item
				break
			}
		}

		if errorItem != nil {
			errMsg := errorItem.ErrorMessage()
			if errMsg == "" {
				errMsg = "Download failed on MeTube"
			}
			b.logger.Warn("MeTube download failed", "url", job.URL, "msg", errMsg)

			b.mu.Lock()
			b.removeActiveMeTubeJob(job.URL)
			b.mu.Unlock()

			// Delete the initial confirmation message if present
			if job.StatusMsgID != 0 {
				_ = b.tg.DeleteMessage(ctx, job.ChatID, job.StatusMsgID)
			}

			failedJob := &FailedMeTubeJob{
				ItemID:   errorItem.ID,
				URL:      job.URL,
				Folder:   job.Folder,
				Quality:  job.Quality,
				Format:   job.Format,
				ErrorMsg: errMsg,
				ChatID:   job.ChatID,
				UserID:   job.UserID,
			}
			token := b.registerFailedMeTubeJob(failedJob)

			msgText := fmt.Sprintf("❌ *MeTube Download Error*\n\n*URL:* %s\n*Message:* `%s`",
				telegram.EscapeMarkdown(job.URL), telegram.EscapeMarkdown(errMsg))
			kb := b.meTubeErrorKeyboard(token)

			sentMsg, err := b.tg.SendMessage(ctx, job.ChatID, msgText, "Markdown", kb)
			if err == nil && sentMsg != nil {
				b.mu.Lock()
				failedJob.MessageID = sentMsg.MessageID
				b.mu.Unlock()
			}
			continue
		}

		if doneItem == nil {
			if queueItem != nil && job.StatusMsgID != 0 {
				if queueItem.ID != "" && job.MeTubeItemID != queueItem.ID {
					b.mu.Lock()
					for i := range b.activeMeTubeJobs {
						if b.activeMeTubeJobs[i].Token == job.Token {
							b.activeMeTubeJobs[i].MeTubeItemID = queueItem.ID
							break
						}
					}
					b.mu.Unlock()
					job.MeTubeItemID = queueItem.ID
				}

				percent := queueItem.Percent
				if percent < 0 {
					percent = 0
				}
				if percent > 100 {
					percent = 100
				}

				diff := math.Abs(percent - job.LastPercent)
				if diff >= 5 || (job.LastPercent == 0 && percent > 0) {
					bar := renderProgressBar(percent/100.0, 10)
					titleLine := ""
					if queueItem.Title != "" {
						titleLine = fmt.Sprintf("🎬 *%s*\n", telegram.EscapeMarkdown(queueItem.Title))
					}
					progressText := fmt.Sprintf("⏳ *Downloading on MeTube...*\n%sQuality: `%s` (%s)\n\n%s %.1f%%\n\n_The bot will send the video file directly to you here once completed._",
						titleLine, job.Quality, job.Format, bar, percent)
					_ = b.tg.EditMessageText(ctx, job.ChatID, job.StatusMsgID, progressText, "Markdown", b.meTubeCancelKeyboard(job.Token))

					b.mu.Lock()
					for i := range b.activeMeTubeJobs {
						if b.activeMeTubeJobs[i].Token == job.Token {
							b.activeMeTubeJobs[i].LastPercent = percent
							break
						}
					}
					b.mu.Unlock()
				}
			}

			if time.Since(job.SubmittedAt) > 30*time.Minute {
				b.mu.Lock()
				b.removeActiveMeTubeJob(job.URL)
				b.mu.Unlock()
				_, _ = b.tg.SendMessage(ctx, job.ChatID, fmt.Sprintf("⏱ Download timed out for URL: `%s`", job.URL), "Markdown", nil)
			}
			continue
		}

		b.logger.Info("MeTube download finished, preparing delivery to Telegram", "title", doneItem.Title, "size", doneItem.Size)

		b.mu.Lock()
		b.removeActiveMeTubeJob(job.URL)
		b.mu.Unlock()

		go b.deliverMeTubeFile(job, *doneItem)
	}
}

func (b *Bot) deliverMeTubeFile(job ActiveMeTubeJob, item metube.HistoryItem) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	downloadURL := b.metube.GetDownloadURL(item.Folder, item.Filename, item.DownloadType)
	publicURL := b.metube.GetPublicDownloadURL(item.Folder, item.Filename, item.DownloadType)
	title := item.Title
	if title == "" {
		title = item.Filename
	}

	// Delete the initial status message if present
	if job.StatusMsgID != 0 {
		_ = b.tg.DeleteMessage(ctx, job.ChatID, job.StatusMsgID)
	}

	cleanFileName := item.Filename
	if cleanFileName == "" {
		cleanFileName = "video.mp4"
		if strings.ToLower(item.DownloadType) == "audio" {
			cleanFileName = "audio.mp3"
		}
	}

	token := fmt.Sprintf("%x", time.Now().UnixNano())
	delItem := &DeliveredMeTubeItem{
		Token:     token,
		ItemID:    item.ID,
		URL:       item.URL,
		Folder:    item.Folder,
		Filename:  cleanFileName,
		ChatID:    job.ChatID,
		UserID:    job.UserID,
		CreatedAt: time.Now(),
	}
	b.mu.Lock()
	b.deliveredMeTubeItems[token] = delItem
	b.mu.Unlock()

	mediaKb := b.meTubeVideoKeyboard(token)

	maxUploadMB := b.cfg.TelegramMaxUploadMB
	if maxUploadMB <= 0 {
		maxUploadMB = 50
	}
	maxTelegramBytes := int64(maxUploadMB) * 1024 * 1024

	if item.Size > maxTelegramBytes {
		sizeMb := float64(item.Size) / 1024 / 1024
		links := fmt.Sprintf("• [🏠 LAN Link](%s)", downloadURL)
		if b.metube.HasPublicURL() {
			links = fmt.Sprintf("• [🌐 Public Link](%s)\n• [🏠 LAN Link](%s)", publicURL, downloadURL)
		}
		msg := fmt.Sprintf("✅ *Download Complete on MeTube!*\n\n🎬 *%s*\n📦 Size: *%.1f MB*\n\n⚠️ _File exceeds Telegram's %dMB upload limit._\n%s",
			telegram.EscapeMarkdown(title), sizeMb, maxUploadMB, links)
		sentMsg, _ := b.tg.SendMessage(ctx, job.ChatID, msg, "Markdown", mediaKb)
		if sentMsg != nil {
			b.mu.Lock()
			delItem.MessageID = sentMsg.MessageID
			b.mu.Unlock()
		}
		return
	}

	statusMsg, _ := b.tg.SendMessage(ctx, job.ChatID, fmt.Sprintf("📤 *Downloading from MeTube & uploading to Telegram:* `%s`...", telegram.EscapeMarkdown(title)), "Markdown", nil)

	data, err := b.metube.DownloadFile(ctx, downloadURL)
	if err != nil {
		b.logger.Error("Failed to fetch file from MeTube", "err", err, "url", downloadURL)
		if statusMsg != nil {
			_ = b.tg.DeleteMessage(ctx, job.ChatID, statusMsg.MessageID)
		}
		links := fmt.Sprintf("• [🏠 LAN Link](%s)", downloadURL)
		if b.metube.HasPublicURL() {
			links = fmt.Sprintf("• [🌐 Public Link](%s)\n• [🏠 LAN Link](%s)", publicURL, downloadURL)
		}
		_, _ = b.tg.SendMessage(ctx, job.ChatID, fmt.Sprintf("❌ Failed to retrieve file from MeTube: %s\n%s", err.Error(), links), "Markdown", nil)
		return
	}

	caption := fmt.Sprintf("🎬 %s", title)
	var sentMediaMsg *telegram.Message
	if strings.ToLower(item.DownloadType) == "audio" {
		caption = fmt.Sprintf("🎵 %s", title)
		sentMediaMsg, err = b.tg.SendAudio(ctx, job.ChatID, cleanFileName, data, caption, mediaKb)
	} else {
		sentMediaMsg, err = b.tg.SendVideo(ctx, job.ChatID, cleanFileName, data, caption, mediaKb)
	}

	if err != nil {
		b.logger.Warn("Failed to send video/audio directly, trying as document", "err", err)
		sentMediaMsg, err = b.tg.SendDocument(ctx, job.ChatID, cleanFileName, data, caption, mediaKb)
	}

	if statusMsg != nil {
		_ = b.tg.DeleteMessage(ctx, job.ChatID, statusMsg.MessageID)
	}

	if sentMediaMsg != nil {
		b.mu.Lock()
		delItem.MessageID = sentMediaMsg.MessageID
		b.mu.Unlock()
	}

	if err != nil {
		b.logger.Error("Failed to upload file to Telegram", "err", err)
		links := fmt.Sprintf("• [🏠 LAN Link](%s)", downloadURL)
		if b.metube.HasPublicURL() {
			links = fmt.Sprintf("• [🌐 Public Link](%s)\n• [🏠 LAN Link](%s)", publicURL, downloadURL)
		}
		_, _ = b.tg.SendMessage(ctx, job.ChatID, fmt.Sprintf("❌ Failed to send file to Telegram: %s\n%s", err.Error(), links), "Markdown", nil)
	}
}

func extractBtih(magnet string) string {
	re := regexp.MustCompile(`(?i)urn:btih:([a-zA-Z0-9]+)`)
	match := re.FindStringSubmatch(magnet)
	if len(match) > 1 {
		return strings.ToLower(match[1])
	}
	return ""
}

func cleanTorrentName(name string) string {
	r := strings.NewReplacer("`", "", "_", "", "*", "")
	return r.Replace(name)
}

func isTorrentURL(text string) bool {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "<>"))
	lower := strings.ToLower(text)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	u, err := url.Parse(text)
	if err != nil || u.Host == "" {
		return false
	}

	pathLower := strings.ToLower(u.Path)
	if strings.HasSuffix(pathLower, ".torrent") || strings.Contains(pathLower, ".torrent/") || strings.Contains(pathLower, ".torrent") {
		return true
	}

	queryLower := strings.ToLower(u.RawQuery)
	if strings.Contains(queryLower, ".torrent") || strings.Contains(queryLower, "torrent=") || strings.Contains(queryLower, "=torrent") {
		return true
	}

	return false
}

func isTorrentData(data []byte) bool {
	return len(data) > 0 && data[0] == 'd' && (bytes.Contains(data, []byte("announce")) || bytes.Contains(data, []byte("info")))
}

func downloadTorrentBytes(ctx context.Context, urlStr string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	fileName := "download.torrent"
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn, ok := params["filename"]; ok && fn != "" {
				fileName = fn
			}
		}
	} else if u, err := url.Parse(urlStr); err == nil {
		base := filepath.Base(u.Path)
		if strings.HasSuffix(strings.ToLower(base), ".torrent") {
			fileName = base
		}
	}

	return data, fileName, nil
}

