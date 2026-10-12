package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State represents persistent bot state saved to disk.
type State struct {
	mu sync.RWMutex

	filePath string

	KnownChatIDs       []int64          `json:"known_chat_ids"`
	NotifiedTorrentIDs map[string]int64 `json:"notified_torrent_ids"`
}

// New creates or loads bot state from the specified file.
func New(filePath string) *State {
	s := &State{
		filePath:           filePath,
		KnownChatIDs:       make([]int64, 0),
		NotifiedTorrentIDs: make(map[string]int64),
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
		KnownChatIDs       []int64          `json:"known_chat_ids"`
		NotifiedTorrentIDs map[string]int64 `json:"notified_torrent_ids"`
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
		KnownChatIDs       []int64          `json:"known_chat_ids"`
		NotifiedTorrentIDs map[string]int64 `json:"notified_torrent_ids"`
	}{
		KnownChatIDs:       s.KnownChatIDs,
		NotifiedTorrentIDs: s.NotifiedTorrentIDs,
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
