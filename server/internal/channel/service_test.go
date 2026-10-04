package channel

import (
	"testing"
	"time"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"gorm.io/gorm"
)

func init() {
	_ = idgen.Init(1, 1)
}

func createTestSpace(db *gorm.DB) *model.Space {
	space := &model.Space{
		ID:      idgen.NextString(),
		Name:    "Test Space",
		OwnerID: idgen.NextString(),
	}
	if db != nil {
		_ = db.Create(space).Error
	}
	return space
}

func TestGetChannels(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		channels, err := svc.GetChannels("", "MEMBER")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 0 {
			t.Errorf("expected 0 channels, got %d", len(channels))
		}
	})

	t.Run("legacy DM channels remain hidden", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		space := createTestSpace(db)
		svc := NewService(db)

		public := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "public", Type: "text", Visibility: "public"}
		dm := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "DM:legacy", Type: "DM", Visibility: "private"}
		if err := db.Create(public).Error; err != nil {
			t.Fatalf("create public channel: %v", err)
		}
		if err := db.Create(dm).Error; err != nil {
			t.Fatalf("create legacy DM channel: %v", err)
		}

		channels, err := svc.GetChannels("", "OWNER")
		if err != nil {
			t.Fatalf("get channels: %v", err)
		}
		if len(channels) != 1 || channels[0].ID != public.ID {
			t.Fatalf("expected only public channel, got %+v", channels)
		}
	})

	t.Run("ordered by position", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		space := createTestSpace(db)
		svc := NewService(db)

		ch1 := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "general", Type: "text", Position: 1}
		ch2 := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "random", Type: "text", Position: 0}
		ch3 := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "voice", Type: "voice", Position: 2}
		_ = db.Create(ch1)
		_ = db.Create(ch2)
		_ = db.Create(ch3)

		channels, err := svc.GetChannels("", "ADMIN")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 3 {
			t.Fatalf("expected 3 channels, got %d", len(channels))
		}
		if channels[0].Name != "random" || channels[0].Position != 0 {
			t.Errorf("expected first channel to be random with position 0, got %s/%d", channels[0].Name, channels[0].Position)
		}
		if channels[1].Name != "general" || channels[1].Position != 1 {
			t.Errorf("expected second channel to be general with position 1, got %s/%d", channels[1].Name, channels[1].Position)
		}
		if channels[2].Name != "voice" || channels[2].Position != 2 {
			t.Errorf("expected third channel to be voice with position 2, got %s/%d", channels[2].Name, channels[2].Position)
		}
	})

	t.Run("member visibility filtering", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		space := createTestSpace(db)
		svc := NewService(db)

		// public channel — visible to MEMBER
		pub := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "public", Type: "text", Position: 0, Visibility: "public"}
		// admin-only channel — hidden from MEMBER
		adminOnly := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "admin-only", Type: "text", Position: 1, Visibility: "admin-only"}
		// role-specific channel with no permission — hidden from MEMBER
		roleSpec := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "role-spec", Type: "text", Position: 2, Visibility: "role-specific"}
		_ = db.Create(pub)
		_ = db.Create(adminOnly)
		_ = db.Create(roleSpec)

		channels, err := svc.GetChannels("", "MEMBER")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 1 {
			t.Fatalf("expected 1 channel visible to MEMBER, got %d", len(channels))
		}
		if channels[0].ID != pub.ID {
			t.Errorf("expected only public channel, got %s", channels[0].Name)
		}

		// Grant MEMBER view permission on role-specific channel
		perm := &model.ChannelRolePermission{
			ID:        idgen.NextString(),
			ChannelID: roleSpec.ID,
			Role:      "MEMBER",
			CanView:   true,
			CanWrite:  true,
		}
		_ = db.Create(perm)

		channels, err = svc.GetChannels("", "MEMBER")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 2 {
			t.Fatalf("expected 2 channels visible to MEMBER after grant, got %d", len(channels))
		}
	})
	t.Run("channels from other spaces are not listed (ISSUE-080)", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		// Main space (earliest created) with its own channels.
		mainSpace := createTestSpace(db)
		mainCh := &model.Channel{ID: idgen.NextString(), SpaceID: mainSpace.ID, Name: "general", Type: "text", Position: 0, Visibility: "public"}
		_ = db.Create(mainCh)

		// A leftover shell space with its own channels.
		shellSpace := &model.Space{ID: idgen.NextString(), Name: "Shell Space", OwnerID: idgen.NextString()}
		_ = db.Create(shellSpace)
		shellCh := &model.Channel{ID: idgen.NextString(), SpaceID: shellSpace.ID, Name: "shell-text", Type: "text", Position: 0, Visibility: "public"}
		_ = db.Create(shellCh)

		userID := idgen.NextString()

		// User without membership falls back to the default (main) space so
		// OWNER/ADMIN bootstrap flows keep working.
		channels, err := svc.GetChannels(userID, "OWNER")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 1 || channels[0].ID != mainCh.ID {
			t.Fatalf("expected only main space channel for membership-less user, got %+v", channels)
		}

		// After the user joins the main space, they still see only its channels.
		_ = db.Create(&model.Membership{ID: idgen.NextString(), UserID: userID, SpaceID: mainSpace.ID, Role: "MEMBER"})
		channels, err = svc.GetChannels(userID, "MEMBER")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 1 || channels[0].ID != mainCh.ID {
			t.Fatalf("expected only main space channel for member, got %+v", channels)
		}

		// A member of the shell space sees only the shell space's channels.
		shellUserID := idgen.NextString()
		_ = db.Create(&model.Membership{ID: idgen.NextString(), UserID: shellUserID, SpaceID: shellSpace.ID, Role: "MEMBER"})
		channels, err = svc.GetChannels(shellUserID, "MEMBER")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(channels) != 1 || channels[0].ID != shellCh.ID {
			t.Fatalf("expected only shell space channel for shell member, got %+v", channels)
		}
	})
}

func TestGetChannel(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		space := createTestSpace(db)
		svc := NewService(db)

		ch := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "general", Type: "text", Position: 0}
		_ = db.Create(ch)

		found, err := svc.GetChannel(ch.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found == nil {
			t.Fatal("expected channel, got nil")
		}
		if found.ID != ch.ID {
			t.Errorf("expected channel ID %s, got %s", ch.ID, found.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		_, err := svc.GetChannel("999999999")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestLegacyDMOperationsAreNotPublic(t *testing.T) {
	db := testutil.MustSetupTestDB()
	space := createTestSpace(db)
	dm := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "legacy-dm", Type: "DM", Visibility: "private"}
	if err := db.Create(dm).Error; err != nil {
		t.Fatalf("create legacy DM channel: %v", err)
	}

	svc := NewService(db)
	assertChannelNotFound := func(t *testing.T, err error) {
		t.Helper()
		appErr, ok := err.(*errors.AppError)
		if !ok || appErr.Code != errors.CHANNEL_NOT_FOUND {
			t.Fatalf("expected CHANNEL_NOT_FOUND, got %v", err)
		}
	}

	if _, err := svc.GetChannel(dm.ID); err == nil {
		t.Fatal("expected GetChannel to reject legacy DM")
	} else {
		assertChannelNotFound(t, err)
	}
	if err := svc.UpdateChannel(dm.ID, map[string]interface{}{"name": "should-not-change"}); err == nil {
		t.Fatal("expected UpdateChannel to reject legacy DM")
	} else {
		assertChannelNotFound(t, err)
	}
	if err := svc.DeleteChannel(dm.ID); err == nil {
		t.Fatal("expected DeleteChannel to reject legacy DM")
	} else {
		assertChannelNotFound(t, err)
	}
	if err := svc.PinMessage(dm.ID, "missing-message"); err == nil {
		t.Fatal("expected PinMessage to reject legacy DM")
	} else {
		assertChannelNotFound(t, err)
	}
	if err := svc.UnpinMessage(dm.ID); err == nil {
		t.Fatal("expected UnpinMessage to reject legacy DM")
	} else {
		assertChannelNotFound(t, err)
	}

	var stored model.Channel
	if err := db.Unscoped().First(&stored, "id = ?", dm.ID).Error; err != nil {
		t.Fatalf("reload legacy DM: %v", err)
	}
	if stored.Name != dm.Name || stored.DeletedAt.Valid {
		t.Fatalf("legacy DM was modified or deleted: %+v", stored)
	}
}

// TestDeleteChannelWithScreenShareSession 是缺陷回归：DeleteChannel 曾用不存在的
// 列 bind_channel_id 清理 ScreenShareSession（000008 迁移已把该列改名为
// channel_id），导致整个删除事务失败、DELETE /api/admin/channels/:channelId
// 恒 500。修复后删除必须成功，且该频道的屏幕共享记录被真正清理。
func TestDeleteChannelWithScreenShareSession(t *testing.T) {
	db := testutil.MustSetupTestDB()
	space := createTestSpace(db)
	svc := NewService(db)

	ch := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "voice", Type: "voice", Position: 0}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	// 本频道下的活跃共享记录（应被清理）
	session := &model.ScreenShareSession{
		ID: idgen.NextString(), UserID: idgen.NextString(), SpaceID: space.ID,
		ChannelID: ch.ID, ShareType: "screen", Active: true, StartedAt: time.Now(),
	}
	if err := db.Create(session).Error; err != nil {
		t.Fatalf("create screen share session: %v", err)
	}
	// 另一个频道的记录（不应被误删）
	otherCh := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "other", Type: "voice", Position: 1}
	if err := db.Create(otherCh).Error; err != nil {
		t.Fatalf("create other channel: %v", err)
	}
	otherSession := &model.ScreenShareSession{
		ID: idgen.NextString(), UserID: idgen.NextString(), SpaceID: space.ID,
		ChannelID: otherCh.ID, ShareType: "screen", Active: true, StartedAt: time.Now(),
	}
	if err := db.Create(otherSession).Error; err != nil {
		t.Fatalf("create other screen share session: %v", err)
	}

	if err := svc.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}

	var remaining int64
	if err := db.Model(&model.ScreenShareSession{}).Where("id = ?", session.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("count screen share sessions: %v", err)
	}
	if remaining != 0 {
		t.Errorf("screen share session for deleted channel not cleaned up")
	}
	if err := db.Model(&model.ScreenShareSession{}).Where("id = ?", otherSession.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("count other screen share sessions: %v", err)
	}
	if remaining != 1 {
		t.Errorf("screen share session of unrelated channel was deleted")
	}
}

func TestCreateChannel(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		space := createTestSpace(db)
		svc := NewService(db)

		ch, err := svc.CreateChannel("announcements", "text", false, "", "", "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if ch == nil {
			t.Fatal("expected channel, got nil")
		}
		if ch.Name != "announcements" {
			t.Errorf("expected name announcements, got %s", ch.Name)
		}
		if ch.SpaceID != space.ID {
			t.Errorf("expected space ID %s, got %s", space.ID, ch.SpaceID)
		}
		if ch.Type != "text" {
			t.Errorf("expected type text, got %s", ch.Type)
		}
		if ch.Visibility != "public" {
			t.Errorf("expected visibility public, got %s", ch.Visibility)
		}
	})

	t.Run("explicit visibility", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		ch, err := svc.CreateChannel("admin-room", "text", false, "", "admin-only", "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if ch.Visibility != "admin-only" {
			t.Errorf("expected visibility admin-only, got %s", ch.Visibility)
		}
	})

	t.Run("invalid visibility", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		_, err := svc.CreateChannel("bad", "text", false, "", "invalid", "")
		if err == nil {
			t.Fatal("expected error for invalid visibility, got nil")
		}
	})

	t.Run("invalid channel type", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		_, err := svc.CreateChannel("bad", "INVALID", false, "", "", "")
		if err == nil {
			t.Fatal("expected error for invalid channel type, got nil")
		}
	})

	t.Run("DM channel type is not creatable", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		if _, err := svc.CreateChannel("legacy-dm", "dm", false, "", "", ""); err == nil {
			t.Fatal("expected DM channel creation to be rejected")
		}
	})

	t.Run("invalid audio quality", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		_, err := svc.CreateChannel("bad", "voice", false, "ultra-hd", "", "")
		if err == nil {
			t.Fatal("expected error for invalid audioQuality, got nil")
		}
	})

	t.Run("valid audio quality", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		ch, err := svc.CreateChannel("voice-room", "voice", false, "fluent", "", "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if ch.VoiceQuality != "fluent" {
			t.Errorf("expected voice quality fluent, got %s", ch.VoiceQuality)
		}
	})
}
