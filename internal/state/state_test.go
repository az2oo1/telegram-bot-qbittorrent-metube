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
}
