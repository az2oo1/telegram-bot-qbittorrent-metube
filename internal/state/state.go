package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PendingLimit holds retry state for setting a speed limit on a torrent in qBittorrent.
type PendingLimit struct {
	Limit       int64 `json:"limit"`
	Mbit        int   `json:"mbit"`
	Attempts    int   `json:"attempts"`
	NextAttempt int64 `json:"next_attempt"`
}

// State represents persistent bot state saved to disk.
type State struct {
	mu sync.RWMutex

	filePath string

	KnownChatIDs       []int64                 `json:"known_chat_ids"`
	NotifiedTorrentIDs map[string]int64        `json:"notified_torrent_ids"`
	NotifiedTorrHashes []string                `json:"notified_torr_hashes"`
	PendingLimits      map[string]PendingLimit `json:"pending_limits"`
}

// New creates or loads bot state from the specified file.
func New(filePath string) *State {
	s := &State{
		filePath:           filePath,
		KnownChatIDs:       make([]int64, 0),
		NotifiedTorrentIDs: make(map[string]int64),
		NotifiedTorrHashes: make([]string, 0),
		PendingLimits:      make(map[string]PendingLimit),
	}

	_ = s.load()
	return s
}

func (s *State) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	var loaded struct {
		KnownChatIDs       []int64                 `json:"known_chat_ids"`
		NotifiedTorrentIDs map[string]int64        `json:"notified_torrent_ids"`
		NotifiedTorrHashes []string                `json:"notified_torr_hashes"`
		PendingLimits      map[string]PendingLimit `json:"pending_limits"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	if loaded.KnownChatIDs != nil {
		s.KnownChatIDs = loaded.KnownChatIDs
	}
	if loaded.NotifiedTorrentIDs != nil {
		s.NotifiedTorrentIDs = loaded.NotifiedTorrentIDs
	}
	if loaded.NotifiedTorrHashes != nil {
		s.NotifiedTorrHashes = loaded.NotifiedTorrHashes
	}
	if loaded.PendingLimits != nil {
		s.PendingLimits = loaded.PendingLimits
	}

	return nil
}

// Save writes current state to the state file atomically.
func (s *State) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := filepath.Dir(s.filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(struct {
		KnownChatIDs       []int64                 `json:"known_chat_ids"`
		NotifiedTorrentIDs map[string]int64        `json:"notified_torrent_ids"`
		NotifiedTorrHashes []string                `json:"notified_torr_hashes"`
		PendingLimits      map[string]PendingLimit `json:"pending_limits"`
	}{
		KnownChatIDs:       s.KnownChatIDs,
		NotifiedTorrentIDs: s.NotifiedTorrentIDs,
		NotifiedTorrHashes: s.NotifiedTorrHashes,
		PendingLimits:      s.PendingLimits,
	}, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpFile, s.filePath)
}

// AddKnownChatID adds a chat ID to known chats if not already present.
func (s *State) AddKnownChatID(chatID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range s.KnownChatIDs {
		if id == chatID {
			return false
		}
	}
	s.KnownChatIDs = append(s.KnownChatIDs, chatID)
	return true
}

// GetKnownChatIDs returns a copy of known chat IDs.
func (s *State) GetKnownChatIDs() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make([]int64, len(s.KnownChatIDs))
	copy(res, s.KnownChatIDs)
	return res
}

// IsTorrentNotified checks if a completed torrent was already notified.
func (s *State) IsTorrentNotified(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.NotifiedTorrentIDs[hash]
	return ok
}

// SetTorrentNotified marks a completed torrent as notified.
func (s *State) SetTorrentNotified(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.NotifiedTorrentIDs[hash] = time.Now().Unix()
}

// PruneNotifiedTorrents removes hashes that no longer exist in qBittorrent.
func (s *State) PruneNotifiedTorrents(activeHashes map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for hash := range s.NotifiedTorrentIDs {
		if _, exists := activeHashes[hash]; !exists {
			delete(s.NotifiedTorrentIDs, hash)
		}
	}
}

// IsTorrHashNotified checks if a TorrServer torrent was notified or ignored.
func (s *State) IsTorrHashNotified(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.NotifiedTorrHashes {
		if h == hash {
			return true
		}
	}
	return false
}

// AddNotifiedTorrHash records a TorrServer torrent as notified.
func (s *State) AddNotifiedTorrHash(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, h := range s.NotifiedTorrHashes {
		if h == hash {
			return
		}
	}
	s.NotifiedTorrHashes = append(s.NotifiedTorrHashes, hash)
}

// PruneNotifiedTorrHashes removes TorrServer hashes that no longer exist in TorrServer.
func (s *State) PruneNotifiedTorrHashes(activeHashes map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := make([]string, 0, len(s.NotifiedTorrHashes))
	for _, h := range s.NotifiedTorrHashes {
		if _, exists := activeHashes[h]; exists {
			filtered = append(filtered, h)
		}
	}
	s.NotifiedTorrHashes = filtered
}

// SetPendingLimit sets or updates a pending download speed limit for a torrent.
func (s *State) SetPendingLimit(hash string, limit PendingLimit) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.PendingLimits[hash] = limit
}

// GetPendingLimits returns a copy of pending limits.
func (s *State) GetPendingLimits() map[string]PendingLimit {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make(map[string]PendingLimit, len(s.PendingLimits))
	for k, v := range s.PendingLimits {
		res[k] = v
	}
	return res
}

// DeletePendingLimit removes a pending speed limit entry.
func (s *State) DeletePendingLimit(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.PendingLimits, hash)
}
