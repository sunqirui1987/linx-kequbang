package upstream

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGetOrCreateSession_SameKeyStable(t *testing.T) {
	m := &Manager{}
	token := "Bearer token_123"
	convKey := "conv_abc"

	s1 := m.GetOrCreateSession(token, convKey)
	s2 := m.GetOrCreateSession(token, convKey)

	if s1.DialogID != s2.DialogID {
		t.Fatalf("dialog id should be stable, got %q vs %q", s1.DialogID, s2.DialogID)
	}
	if s1.UserID != s2.UserID {
		t.Fatalf("user id should be stable, got %q vs %q", s1.UserID, s2.UserID)
	}
	if !s1.CreatedAt.Equal(s2.CreatedAt) {
		t.Fatalf("createdAt should be stable, got %v vs %v", s1.CreatedAt, s2.CreatedAt)
	}

	wantDialogID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("dialog|"+token+"|"+convKey)).String()
	if s1.DialogID != wantDialogID {
		t.Fatalf("unexpected dialog id, want %q got %q", wantDialogID, s1.DialogID)
	}

	wantUserID := "user_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("user|"+token)).String()[:8]
	if s1.UserID != wantUserID {
		t.Fatalf("unexpected user id, want %q got %q", wantUserID, s1.UserID)
	}
}

func TestGetOrCreateSession_DifferentConvKeyDifferentDialogID(t *testing.T) {
	m := &Manager{}
	token := "Bearer token_123"

	s1 := m.GetOrCreateSession(token, "conv_1")
	s2 := m.GetOrCreateSession(token, "conv_2")

	if s1.DialogID == s2.DialogID {
		t.Fatalf("dialog id should differ for different convKey, got %q", s1.DialogID)
	}
	if s1.UserID != s2.UserID {
		t.Fatalf("user id should be stable per token, got %q vs %q", s1.UserID, s2.UserID)
	}
}

func TestUpdateSession_UpdatesDialogIDPreservesCreatedAt(t *testing.T) {
	m := &Manager{}
	token := "Bearer token_123"
	convKey := "conv_abc"

	s1 := m.GetOrCreateSession(token, convKey)
	time.Sleep(10 * time.Millisecond)

	newDialogID := "dialog_new"
	newUserID := "user_new"
	m.UpdateSession(token, convKey, newDialogID, newUserID)

	s2 := m.GetOrCreateSession(token, convKey)
	if s2.DialogID != newDialogID {
		t.Fatalf("dialog id should be updated, want %q got %q", newDialogID, s2.DialogID)
	}
	if s2.UserID != newUserID {
		t.Fatalf("user id should be updated, want %q got %q", newUserID, s2.UserID)
	}
	if !s2.CreatedAt.Equal(s1.CreatedAt) {
		t.Fatalf("createdAt should be preserved, got %v vs %v", s2.CreatedAt, s1.CreatedAt)
	}
	if !s2.UpdatedAt.After(s1.UpdatedAt) && !s2.UpdatedAt.Equal(s1.UpdatedAt) {
		t.Fatalf("updatedAt should not go backwards, got %v then %v", s1.UpdatedAt, s2.UpdatedAt)
	}
}
