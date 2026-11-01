package message

import (
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// 1.9.5 task-136：提及计数必须原子 upsert。
//
// 旧实现 `UPDATE user_channel_reads SET mention_count = mention_count + 1
// WHERE user_id = ? AND channel_id = ?` 在「用户从未读过该频道」时命中 0 行，
// 错误仍为 nil，计数被静默丢弃（首次被 @ 的徽标永久丢失）。这些用例锁定修复后的
// 行为：无行则建行（unread_count=1、mention_count=1），有行则原子 +1。

func newMentionCountFixture(t *testing.T) (*Handler, *model.Channel) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	// hub 显式传 nil：验证计数与 WS 推送完全解耦。
	h := NewHandler(db, cfg, nil, nil)

	space := &model.Space{ID: idgen.NextString(), Name: "mention-count-space", OwnerID: "u-owner"}
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
	return h, channel
}

func newMentionMessage(channelID string, mentions ...string) *model.Message {
	set := make(map[string]bool, len(mentions))
	for _, m := range mentions {
		set[m] = true
	}
	return &model.Message{
		ID:                idgen.NextString(),
		ChannelID:         channelID,
		UserID:            "u-author",
		AuthorUsername:    "alice",
		AuthorDisplayName: "Alice",
		Content:           "ping",
		MentionUserIDs:    set,
	}
}

func loadReadRow(t *testing.T, h *Handler, userID, channelID string) model.UserChannelRead {
	t.Helper()
	var rows []model.UserChannelRead
	if err := h.db.Where("user_id = ? AND channel_id = ?", userID, channelID).Find(&rows).Error; err != nil {
		t.Fatalf("load user_channel_reads: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 user_channel_reads row, got %d", len(rows))
	}
	return rows[0]
}

// 无既有行：必须建行，且 unread_count=1、mention_count=1（「@ 到就是一条未读」）。
func TestMentionCountCreatesRowWhenAbsent(t *testing.T) {
	h, channel := newMentionCountFixture(t)

	h.sendMentionNotifications(newMentionMessage(channel.ID, "u-bob"))

	row := loadReadRow(t, h, "u-bob", channel.ID)
	if row.MentionCount != 1 {
		t.Fatalf("新建行 mention_count 应为 1，实际 %d", row.MentionCount)
	}
	if row.UnreadCount != 1 {
		t.Fatalf("新建行 unread_count 应为 1（@ 即一条未读），实际 %d", row.UnreadCount)
	}
	if row.LastReadMessageID != "" {
		t.Fatalf("新建行 last_read_message_id 应留空（用户尚未读过），实际 %q", row.LastReadMessageID)
	}
	if !row.ReadAt.IsZero() {
		t.Fatalf("新建行 read_at 应为零值，实际 %v", row.ReadAt)
	}
}

// 已有行：只原子 +1 提及数，不得覆盖 unread_count / 已读水位。
func TestMentionCountIncrementsExistingRow(t *testing.T) {
	h, channel := newMentionCountFixture(t)

	existing := &model.UserChannelRead{
		ID:                idgen.NextString(),
		UserID:            "u-bob",
		ChannelID:         channel.ID,
		LastReadMessageID: "msg-previous",
		UnreadCount:       5,
		MentionCount:      2,
	}
	if err := h.db.Create(existing).Error; err != nil {
		t.Fatalf("seed user_channel_reads: %v", err)
	}

	h.sendMentionNotifications(newMentionMessage(channel.ID, "u-bob"))

	row := loadReadRow(t, h, "u-bob", channel.ID)
	if row.MentionCount != 3 {
		t.Fatalf("已有行 mention_count 应为 3（2+1），实际 %d", row.MentionCount)
	}
	if row.UnreadCount != 5 {
		t.Fatalf("已有行 unread_count 不应被改写，期望 5，实际 %d", row.UnreadCount)
	}
	if row.LastReadMessageID != "msg-previous" {
		t.Fatalf("已有行 last_read_message_id 不应被改写，实际 %q", row.LastReadMessageID)
	}
}

// 作者本人被 @ 时既不发通知也不计数。
func TestMentionCountSkipsAuthor(t *testing.T) {
	h, channel := newMentionCountFixture(t)

	h.sendMentionNotifications(newMentionMessage(channel.ID, "u-author", "u-bob"))

	var rows []model.UserChannelRead
	if err := h.db.Where("channel_id = ?", channel.ID).Find(&rows).Error; err != nil {
		t.Fatalf("load rows: %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != "u-bob" {
		t.Fatalf("只应为被提及的他人建 1 行，实际 %+v", rows)
	}
}

// 并发 @ 同一用户：必须恰好一行且计数等于提及次数（不能丢更新）。
func TestMentionCountConcurrentNoLostUpdate(t *testing.T) {
	h, channel := newMentionCountFixture(t)

	// SQLite 单写者：把连接数压到 1，避免 "database is locked" 干扰计数断言。
	sqlDB, err := h.db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			h.bumpMentionCount("u-bob", newMentionMessage(channel.ID))
		}()
	}
	wg.Wait()

	row := loadReadRow(t, h, "u-bob", channel.ID)
	if row.MentionCount != n {
		t.Fatalf("并发 %d 次提及后 mention_count 应为 %d，实际 %d（存在丢更新）", n, n, row.MentionCount)
	}
}

// 建行后用户真正读频道：MarkChannelRead 必须能归零（证明 1 是「未读」而非脏数据）。
func TestMentionRowZeroedByMarkChannelRead(t *testing.T) {
	h, channel := newMentionCountFixture(t)

	h.sendMentionNotifications(newMentionMessage(channel.ID, "u-bob"))
	before := loadReadRow(t, h, "u-bob", channel.ID)
	if before.UnreadCount != 1 || before.MentionCount != 1 {
		t.Fatalf("前置条件不符：unread=%d mention=%d", before.UnreadCount, before.MentionCount)
	}

	svc := NewService(h.db)
	if err := svc.MarkChannelRead("u-bob", channel.ID, ""); err != nil {
		t.Fatalf("MarkChannelRead: %v", err)
	}

	after := loadReadRow(t, h, "u-bob", channel.ID)
	if after.UnreadCount != 0 {
		t.Fatalf("已读后 unread_count 应为 0，实际 %d", after.UnreadCount)
	}
	if after.MentionCount != 0 {
		t.Fatalf("已读后 mention_count 应为 0，实际 %d", after.MentionCount)
	}
}
