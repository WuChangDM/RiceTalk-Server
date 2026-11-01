package message

import (
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// TestMentionNotificationPersistedWithoutHub 锁定 1.9.4 task-118 的关键修复点：
//
// 旧实现在 sendMentionNotifications 开头就 `if h.hub == nil { return }`，
// 于是「没有 realtime hub」的场景（降级模式、部分测试装配、hub 注入失败的部署）
// 只推 WS、完全不落库，通知中心永久为空。现在「落库」必须与 WS 推送解耦：
// hub 为 nil 也要在 notifications 表留下记录。
func TestMentionNotificationPersistedWithoutHub(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	// hub 显式传 nil：走 MessageBroadcaster 为 nil 的分支。
	h := NewHandler(db, cfg, nil, nil)

	space := &model.Space{ID: idgen.NextString(), Name: "notif-space", OwnerID: "u-owner"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	channel := &model.Channel{
		ID:        idgen.NextString(),
		SpaceID:   space.ID,
		Name:      "general",
		Type:      "TEXT",
		CreatedBy: "u-author",
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	msg := &model.Message{
		ID:                idgen.NextString(),
		ChannelID:         channel.ID,
		UserID:            "u-author",
		AuthorUsername:    "alice",
		AuthorDisplayName: "Alice",
		Content:           "大家好 @bob",
		// 含作者自己在内：作者本人绝不能收到「提到了你」的通知。
		MentionUserIDs: map[string]bool{"u-bob": true, "u-author": true},
	}

	h.sendMentionNotifications(msg)

	var bobs []model.Notification
	if err := db.Where("user_id = ?", "u-bob").Find(&bobs).Error; err != nil {
		t.Fatalf("查询 bob 的通知: %v", err)
	}
	if len(bobs) != 1 {
		t.Fatalf("hub 为 nil 时也必须落库 1 条 mention 通知，实际 %d 条", len(bobs))
	}
	n := bobs[0]
	if n.Type != "mention" || n.RefType != "message" || n.RefID != msg.ID {
		t.Fatalf("通知字段不符：type=%q refType=%q refID=%q（期望 mention/message/%s）", n.Type, n.RefType, n.RefID, msg.ID)
	}
	if n.Title != "Alice 提到了你" {
		t.Fatalf("标题应为 %q，实际 %q", "Alice 提到了你", n.Title)
	}
	if n.Body != "大家好 @bob" {
		t.Fatalf("正文摘要应为 %q，实际 %q", "大家好 @bob", n.Body)
	}
	if n.SpaceID != space.ID {
		t.Fatalf("space_id 应为 %q，实际 %q", space.ID, n.SpaceID)
	}

	var authorNotifs int64
	if err := db.Model(&model.Notification{}).Where("user_id = ?", "u-author").Count(&authorNotifs).Error; err != nil {
		t.Fatalf("统计作者通知: %v", err)
	}
	if authorNotifs != 0 {
		t.Fatalf("作者本人不应收到 mention 通知，实际 %d 条", authorNotifs)
	}
}
