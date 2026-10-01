// Package gormrepo provides GORM implementations of Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.2.
package gormrepo

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func TestGormUserRepository_CRUD(t *testing.T) {
	db := testutil.MustSetupTestDB()
	repo := NewGormUserRepository(db)
	ctx := context.Background()

	// Create
	user := &model.User{
		ID:           "user_test1",
		Username:     "alice",
		Email:        "alice@example.com",
		PasswordHash: "hash",
		Role:         "MEMBER",
	}
	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// GetByID
	got, err := repo.GetByID(ctx, "user_test1")
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("got username %s, want alice", got.Username)
	}

	// GetByEmail
	got, err = repo.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail failed: %v", err)
	}
	if got.ID != "user_test1" {
		t.Fatalf("got id %s, want user_test1", got.ID)
	}

	// GetByUsername
	got, err = repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUsername failed: %v", err)
	}
	if got.ID != "user_test1" {
		t.Fatalf("got id %s, want user_test1", got.ID)
	}

	// Update
	got.DisplayName = "Alice Updated"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	got2, _ := repo.GetByID(ctx, "user_test1")
	if got2.DisplayName != "Alice Updated" {
		t.Fatalf("DisplayName not updated, got %s", got2.DisplayName)
	}

	// UpdateLastLogin
	if err := repo.UpdateLastLogin(ctx, "user_test1"); err != nil {
		t.Fatalf("UpdateLastLogin failed: %v", err)
	}
	got3, _ := repo.GetByID(ctx, "user_test1")
	if got3.LastLoginAt == nil {
		t.Fatal("LastLoginAt should be set")
	}

	// Delete
	if err := repo.Delete(ctx, "user_test1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, err = repo.GetByID(ctx, "user_test1")
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("expected ErrRecordNotFound after delete, got %v", err)
	}
}

func TestGormChannelRepository_CRUD(t *testing.T) {
	db := testutil.MustSetupTestDB()
	repo := NewGormChannelRepository(db)
	ctx := context.Background()

	// Create
	ch := &model.Channel{
		ID:      "ch_test1",
		SpaceID: "space1",
		Name:    "general",
		Type:    "text",
	}
	if err := repo.Create(ctx, ch); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Create second channel for list test
	ch2 := &model.Channel{
		ID:       "ch_test2",
		SpaceID:  "space1",
		Name:     "random",
		Type:     "text",
		Position: 1,
	}
	_ = repo.Create(ctx, ch2)

	// GetByID
	got, err := repo.GetByID(ctx, "ch_test1")
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if got.Name != "general" {
		t.Fatalf("got name %s, want general", got.Name)
	}

	// GetBySpaceID
	list, err := repo.GetBySpaceID(ctx, "space1")
	if err != nil {
		t.Fatalf("GetBySpaceID failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(list))
	}

	// Update
	got.Name = "general-updated"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	got2, _ := repo.GetByID(ctx, "ch_test1")
	if got2.Name != "general-updated" {
		t.Fatalf("Name not updated, got %s", got2.Name)
	}

	// Delete
	if err := repo.Delete(ctx, "ch_test1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, err = repo.GetByID(ctx, "ch_test1")
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("expected ErrRecordNotFound after delete, got %v", err)
	}
}

func TestGormMessageRepository_CRUD(t *testing.T) {
	db := testutil.MustSetupTestDB()
	repo := NewGormMessageRepository(db)
	ctx := context.Background()

	// Create
	msg := &model.Message{
		ID:        "msg_test1",
		ChannelID: "ch1",
		UserID:    "u1",
		Content:   "hello",
		Type:      "text",
		CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, msg); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Create second message for list test
	msg2 := &model.Message{
		ID:        "msg_test2",
		ChannelID: "ch1",
		UserID:    "u1",
		Content:   "world",
		Type:      "text",
		CreatedAt: time.Now().UTC().Add(time.Second),
	}
	_ = repo.Create(ctx, msg2)

	// GetByID
	got, err := repo.GetByID(ctx, "msg_test1")
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if got.Content != "hello" {
		t.Fatalf("got content %s, want hello", got.Content)
	}

	// GetByChannelID with limit
	list, err := repo.GetByChannelID(ctx, "ch1", 10, 0)
	if err != nil {
		t.Fatalf("GetByChannelID failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(list))
	}

	// GetByChannelID with limit=1
	list, err = repo.GetByChannelID(ctx, "ch1", 1, 0)
	if err != nil {
		t.Fatalf("GetByChannelID failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 message with limit=1, got %d", len(list))
	}

	// Update
	got.Content = "hello-updated"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	got2, _ := repo.GetByID(ctx, "msg_test1")
	if got2.Content != "hello-updated" {
		t.Fatalf("Content not updated, got %s", got2.Content)
	}

	// Delete
	if err := repo.Delete(ctx, "msg_test1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, err = repo.GetByID(ctx, "msg_test1")
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("expected ErrRecordNotFound after delete, got %v", err)
	}
}
