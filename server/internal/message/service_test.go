package message

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func TestCreateMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "Hello, world!", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if msg == nil {
			t.Fatal("expected message, got nil")
		}
		if msg.Content != "Hello, world!" {
			t.Errorf("expected content 'Hello, world!', got %s", msg.Content)
		}
		if msg.ChannelID != "channel-1" {
			t.Errorf("expected channel ID channel-1, got %s", msg.ChannelID)
		}
	})

	t.Run("content too long", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		longContent := strings.Repeat("a", 2001)
		_, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", longContent, idgen.NextString(), "", nil)
		if err == nil {
			t.Fatal("expected error for too long content, got nil")
		}
	})

	t.Run("thread reply persists parentId and returns via thread query", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)
		channelID := "channel-1"
		userID := "user-1"

		parent, err := svc.CreateMessage(channelID, userID, "testuser", "MEMBER", "parent message", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create parent message: %v", err)
		}

		// reply with an explicit parentId
		reply, err := svc.CreateMessage(channelID, userID, "testuser", "MEMBER", "thread reply", idgen.NextString(), parent.ID, nil)
		if err != nil {
			t.Fatalf("failed to create reply message: %v", err)
		}
		if reply.ParentID == nil || *reply.ParentID != parent.ID {
			t.Fatalf("expected reply.ParentID to reference %s, got %v", parent.ID, reply.ParentID)
		}

		// parentId must be persisted so it survives a fresh read
		var stored model.Message
		if err := db.First(&stored, "id = ?", reply.ID).Error; err != nil {
			t.Fatalf("failed to reload reply: %v", err)
		}
		if stored.ParentID == nil || *stored.ParentID != parent.ID {
			t.Errorf("expected stored ParentID %s, got %v", parent.ID, stored.ParentID)
		}

		// thread query returns the reply linked by parentId
		threadMsgs, hasMore, err := svc.GetThread(parent.ID, 20)
		if err != nil {
			t.Fatalf("thread query failed: %v", err)
		}
		if hasMore {
			t.Error("expected hasMore false for single reply thread")
		}
		found := false
		for _, m := range threadMsgs {
			if m.ID == reply.ID && m.ParentID != nil && *m.ParentID == parent.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected thread to contain reply %s, got %+v", reply.ID, threadMsgs)
		}
	})

	t.Run("empty or whitespace parentId leaves ParentID nil", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		for _, raw := range []string{"", "   "} {
			msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "plain message", idgen.NextString(), raw, nil)
			if err != nil {
				t.Fatalf("failed to create message: %v", err)
			}
			if msg.ParentID != nil {
				t.Errorf("expected ParentID nil for parentId %q, got %v", raw, msg.ParentID)
			}
		}
	})
}

func TestGetMessages(t *testing.T) {
	t.Run("pagination", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		channelID := "channel-1"
		for i := 0; i < 15; i++ {
			_, err := svc.CreateMessage(channelID, "user-1", "testuser", "", "msg"+idgen.NextString(), idgen.NextString(), "", nil)
			if err != nil {
				t.Fatalf("failed to create message: %v", err)
			}
		}

		messages, hasMore, err := svc.GetMessages(channelID, "", 5)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(messages) != 5 {
			t.Errorf("expected 5 messages, got %d", len(messages))
		}
		if !hasMore {
			t.Error("expected hasMore to be true")
		}
	})

	t.Run("hasMore flag", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		channelID := "channel-1"
		for i := 0; i < 3; i++ {
			_, err := svc.CreateMessage(channelID, "user-1", "testuser", "", "msg"+idgen.NextString(), idgen.NextString(), "", nil)
			if err != nil {
				t.Fatalf("failed to create message: %v", err)
			}
		}

		messages, hasMore, err := svc.GetMessages(channelID, "", 5)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(messages) != 3 {
			t.Errorf("expected 3 messages, got %d", len(messages))
		}
		if hasMore {
			t.Error("expected hasMore to be false")
		}
	})
}

func TestGetMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "Hello", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}

		found, err := svc.GetMessage(msg.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found == nil {
			t.Fatal("expected message, got nil")
		}
		if found.ID != msg.ID {
			t.Errorf("expected message ID %s, got %s", msg.ID, found.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		_, err := svc.GetMessage("999999999")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestSearchAllMessagesScopesAndFilters(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	now := time.Now().UTC().Truncate(time.Second)
	space := &model.Space{ID: "space-search", Name: "Search", OwnerID: "owner-search"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	user := &model.User{ID: "user-search", Username: "searcher", Email: "searcher@example.com", DisplayName: "搜索者", Role: "MEMBER"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	textChannel := &model.Channel{ID: "channel-search-text", SpaceID: space.ID, Name: "general", Type: "text", Visibility: "public"}
	dmChannel := &model.Channel{ID: "channel-search-dm", SpaceID: space.ID, Name: "DM:legacy", Type: "DM", Visibility: "private"}
	voiceChannel := &model.Channel{ID: "channel-search-voice", SpaceID: space.ID, Name: "voice", Type: "voice", Visibility: "public"}
	for _, channel := range []*model.Channel{textChannel, dmChannel, voiceChannel} {
		if err := db.Create(channel).Error; err != nil {
			t.Fatalf("create channel %s: %v", channel.ID, err)
		}
	}
	messages := []*model.Message{
		{ID: "message-old", ChannelID: textChannel.ID, UserID: user.ID, Content: "alpha keyword", Type: "text", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "message-new", ChannelID: textChannel.ID, UserID: user.ID, Content: "beta keyword", Type: "text", CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: "message-dm", ChannelID: dmChannel.ID, UserID: user.ID, Content: "hidden keyword", Type: "text", CreatedAt: now, UpdatedAt: now},
		{ID: "message-voice", ChannelID: voiceChannel.ID, UserID: user.ID, Content: "voice keyword", Type: "text", CreatedAt: now, UpdatedAt: now},
	}
	for _, message := range messages {
		if err := db.Create(message).Error; err != nil {
			t.Fatalf("create message %s: %v", message.ID, err)
		}
	}

	results, total, err := svc.SearchAllMessages(GlobalSearchOptions{
		Query:      "keyword",
		ChannelIDs: []string{textChannel.ID, dmChannel.ID, voiceChannel.ID},
		AuthorID:   user.ID,
		Before:     ptrTime(now.Add(-30 * time.Minute)),
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("search all messages: %v", err)
	}
	if total != 2 || len(results) != 1 {
		t.Fatalf("expected 2 matching text-channel messages and one result, total=%d results=%d", total, len(results))
	}
	if results[0].MessageID != "message-new" {
		t.Fatalf("expected newest result first, got %s", results[0].MessageID)
	}

	results, total, err = svc.SearchAllMessages(GlobalSearchOptions{
		Query:      "hidden",
		ChannelIDs: []string{dmChannel.ID},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("search DM messages: %v", err)
	}
	if total != 0 || len(results) != 0 {
		t.Fatalf("expected DM messages to be excluded, total=%d results=%d", total, len(results))
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func TestSnapshotOverwrittenByRename(t *testing.T) {
	// Simulate user operation: send message, change display name, query again.
	// Per design (Discord/Slack behavior), historical messages show the CURRENT
	// display name because messages are owned by user ID, not a frozen name.
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	// Create user
	user := model.User{ID: "user-1", Username: "alice", DisplayName: "Alice", Avatar: "http://avatar.com/alice.png", Role: "MEMBER"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Send message (snapshot should be recorded)
	msg, err := svc.CreateMessage("channel-1", "user-1", "alice", "MEMBER", "Hello!", idgen.NextString(), "", nil)
	if err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	// Verify snapshot was recorded at creation time
	if msg.AuthorDisplayName != "Alice" {
		t.Errorf("expected snapshot display name 'Alice', got '%s'", msg.AuthorDisplayName)
	}
	if msg.AuthorAvatarURL != "http://avatar.com/alice.png" {
		t.Errorf("expected snapshot avatar 'http://avatar.com/alice.png', got '%s'", msg.AuthorAvatarURL)
	}
	if msg.AuthorRole != "MEMBER" {
		t.Errorf("expected snapshot role 'MEMBER', got '%s'", msg.AuthorRole)
	}

	// User changes display name and avatar
	if err := db.Model(&user).Updates(map[string]interface{}{"display_name": "Alice-New", "avatar": "http://avatar.com/new.png"}).Error; err != nil {
		t.Fatalf("failed to update user: %v", err)
	}

	// Query messages again
	messages, _, err := svc.GetMessages("channel-1", "", 10)
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	// Verify snapshot IS overwritten by the current user display name (rename
	// propagates to historical messages). Avatar snapshot stays frozen (only the
	// display name is dynamically resolved); role stays frozen unless empty.
	if messages[0].AuthorDisplayName != "Alice-New" {
		t.Errorf("expected current display name 'Alice-New' after rename, got '%s'", messages[0].AuthorDisplayName)
	}
	if messages[0].AuthorAvatarURL != "http://avatar.com/alice.png" {
		t.Errorf("expected frozen avatar 'http://avatar.com/alice.png', got '%s'", messages[0].AuthorAvatarURL)
	}
	if messages[0].AuthorRole != "MEMBER" {
		t.Errorf("expected snapshot role 'MEMBER', got '%s'", messages[0].AuthorRole)
	}
}

func TestBackwardCompatibility(t *testing.T) {
	// Simulate old messages where snapshot fields were empty
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	// Create user
	user := model.User{ID: "user-1", Username: "bob", DisplayName: "Bob", Avatar: "http://avatar.com/bob.png", Role: "ADMIN"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create old-style message with empty snapshot fields using raw SQL to bypass GORM defaults
	oldMsgID := idgen.NextString()
	if err := db.Exec(`INSERT INTO messages (id, channel_id, user_id, author_username, author_display_name, author_avatar_url, author_role, content, type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		oldMsgID, "channel-1", "user-1", "", "", "", "", "Old message", "text").Error; err != nil {
		t.Fatalf("failed to create old message via raw SQL: %v", err)
	}

	// Query messages - should fill in missing snapshot fields
	messages, _, err := svc.GetMessages("channel-1", "", 10)
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	// Debug: check what role the user actually has in DB
	var dbUser model.User
	db.First(&dbUser, "id = ?", "user-1")
	t.Logf("DB user role: %s", dbUser.Role)
	for _, u := range messages {
		t.Logf("message AuthorRole: '%s', AuthorDisplayName: '%s'", u.AuthorRole, u.AuthorDisplayName)
	}

	if messages[0].AuthorDisplayName != "Bob" {
		t.Errorf("expected backward-compatible display name 'Bob', got '%s'", messages[0].AuthorDisplayName)
	}
	if messages[0].AuthorAvatarURL != "http://avatar.com/bob.png" {
		t.Errorf("expected backward-compatible avatar, got '%s'", messages[0].AuthorAvatarURL)
	}
	if messages[0].AuthorRole != "ADMIN" {
		t.Errorf("expected backward-compatible role 'ADMIN', got '%s'", messages[0].AuthorRole)
	}
}

func TestConcurrentGetMessages(t *testing.T) {
	// Pressure test: concurrent message queries
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	// Create user and messages
	user := model.User{ID: "user-1", Username: "perf", DisplayName: "Perf"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	for i := 0; i < 50; i++ {
		_, err := svc.CreateMessage("channel-1", "user-1", "perf", "MEMBER", "msg"+idgen.NextString(), idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
	}

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			messages, _, err := svc.GetMessages("channel-1", "", 10)
			if err != nil {
				errors <- err
				return
			}
			if len(messages) == 0 {
				errors <- fmt.Errorf("expected messages, got 0")
			}
		}()
	}

	wg.Wait()
	close(errors)

	errCount := 0
	for err := range errors {
		t.Logf("concurrent error: %v", err)
		errCount++
	}
	if errCount > 0 {
		t.Errorf("got %d errors during concurrent queries", errCount)
	}
}

func TestAddReaction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "Hello", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}

		reaction, err := svc.AddReaction(msg.ID, "user-1", "👍")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if reaction.MessageID != msg.ID {
			t.Errorf("expected reaction message ID %s, got %s", msg.ID, reaction.MessageID)
		}
		if reaction.Emoji != "👍" {
			t.Errorf("expected emoji 👍, got %s", reaction.Emoji)
		}

		reactions, err := svc.GetReactions(msg.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(reactions) != 1 {
			t.Errorf("expected 1 reaction, got %d", len(reactions))
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "Hello", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}

		first, err := svc.AddReaction(msg.ID, "user-1", "👍")
		if err != nil {
			t.Fatalf("failed to add reaction: %v", err)
		}

		second, err := svc.AddReaction(msg.ID, "user-1", "👍")
		if err != nil {
			t.Fatalf("failed to add reaction second time: %v", err)
		}
		if first.ID != second.ID {
			t.Error("expected same reaction on duplicate add")
		}
	})

	t.Run("not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		_, err := svc.AddReaction("999999999", "user-1", "👍")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestRemoveReaction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "Hello", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
		_, _ = svc.AddReaction(msg.ID, "user-1", "👍")

		if err := svc.RemoveReaction(msg.ID, "user-1", "👍"); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		reactions, err := svc.GetReactions(msg.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(reactions) != 0 {
			t.Errorf("expected 0 reactions, got %d", len(reactions))
		}
	})
}

func TestMarkChannelRead(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		msg, err := svc.CreateMessage("channel-1", "user-1", "testuser", "MEMBER", "Hello", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}

		if err := svc.MarkChannelRead("user-1", "channel-1", msg.ID); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		info, err := svc.GetUnreadCount("user-1", "channel-1")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if info.UnreadCount != 0 {
			t.Errorf("expected unread count 0, got %d", info.UnreadCount)
		}
	})
}

// TestParseMentions verifies that "@显示名" is rewritten to "<@user_xxx>" and that
// exact tokens pass through; also that mentionUserIDs captures the targets.
func TestParseMentions(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	// Create a space + channel + members so name→ID resolution works.
	spaceID := "space-1"
	ch := &model.Channel{ID: "channel-m", SpaceID: spaceID, Name: "General", Type: "TEXT"}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	alice := &model.User{ID: "user_alice", Username: "alice_gen", DisplayName: "爱丽丝", Email: "alice@x.com"}
	bob := &model.User{ID: "user_bob", Username: "bob_gen", DisplayName: "Bob", Email: "bob@x.com"}
	if err := db.Create(alice).Error; err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := db.Create(bob).Error; err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: spaceID, UserID: "user_alice", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create alice membership: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: spaceID, UserID: "user_bob", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create bob membership: %v", err)
	}

	t.Run("rewrites display name to ID token", func(t *testing.T) {
		content, ids := svc.parseMentions("channel-m", "你好 @爱丽丝 在吗")
		if !strings.Contains(content, "<@user_alice>") {
			t.Errorf("expected <@user_alice> in rewritten content, got %q", content)
		}
		if !ids["user_alice"] {
			t.Errorf("expected user_alice in mentionUserIDs, got %v", ids)
		}
	})

	t.Run("keeps exact token", func(t *testing.T) {
		content, ids := svc.parseMentions("channel-m", "看这个 <@user_bob> 的回复")
		if !strings.Contains(content, "<@user_bob>") {
			t.Errorf("expected <@user_bob> preserved, got %q", content)
		}
		if !ids["user_bob"] {
			t.Errorf("expected user_bob in mentionUserIDs, got %v", ids)
		}
	})

	t.Run("plain text untouched", func(t *testing.T) {
		content, ids := svc.parseMentions("channel-m", "普通消息 123")
		if content != "普通消息 123" {
			t.Errorf("expected unchanged content, got %q", content)
		}
		if len(ids) != 0 {
			t.Errorf("expected no mentionUserIDs, got %v", ids)
		}
	})
}

// TestCreateMessageStoresRewrittenMentions verifies the full path: sending a message
// with "@显示名" stores "<@user_xxx>" in Content (the parseMentions result must be
// applied to msg.Content before insert).
func TestCreateMessageStoresRewrittenMentions(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	ch := &model.Channel{ID: "channel-m2", SpaceID: "space-2", Name: "General", Type: "TEXT"}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	carol := &model.User{ID: "user_carol", Username: "carol_gen", DisplayName: "卡罗尔", Email: "carol@x.com"}
	if err := db.Create(carol).Error; err != nil {
		t.Fatalf("create carol: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: "space-2", UserID: "user_carol", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	msg, err := svc.CreateMessage("channel-m2", "user_sender", "sender", "MEMBER", "嗨 @卡罗尔 在吗", idgen.NextString(), "", nil)
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	if !strings.Contains(msg.Content, "<@user_carol>") {
		t.Errorf("expected rewritten <@user_carol> in stored content, got %q", msg.Content)
	}
	if !msg.MentionUserIDs["user_carol"] {
		t.Errorf("expected user_carol in MentionUserIDs, got %v", msg.MentionUserIDs)
	}
}
