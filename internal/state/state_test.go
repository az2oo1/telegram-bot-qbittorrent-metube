package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateSaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "qbot_state_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	stateFile := filepath.Join(tmpDir, "bot_state.json")
	s1 := New(stateFile)

	s1.AddKnownChatID(123456)
	s1.AddKnownChatID(789012)
	s1.SetTorrentNotified("hash123")
	s1.AddNotifiedTorrHash("torr_hash_456")
	s1.SetPendingLimit("limit_hash_789", PendingLimit{
		Limit:    1048576,
		Mbit:     8,
		Attempts: 1,
	})

	if err := s1.Save(); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Load in fresh state instance
	s2 := New(stateFile)

	chats := s2.GetKnownChatIDs()
	if len(chats) != 2 || chats[0] != 123456 || chats[1] != 789012 {
		t.Errorf("unexpected known chat IDs: %v", chats)
	}

	if !s2.IsTorrentNotified("hash123") {
		t.Errorf("expected hash123 to be notified")
	}

	if !s2.IsTorrHashNotified("torr_hash_456") {
		t.Errorf("expected torr_hash_456 to be notified")
	}

	limits := s2.GetPendingLimits()
	pl, exists := limits["limit_hash_789"]
	if !exists || pl.Mbit != 8 || pl.Limit != 1048576 {
		t.Errorf("unexpected pending limits: %+v", limits)
	}
}
