package message

// A9 二期（DES-20261029-01）线程权威未读的测试：
//   - 线程未读聚合（按 parent 分组 / 排除本人 / lastAt 倒序 / cap 20）；
//   - 线程已读位（不影响频道水位 / 幂等 / 非法 parent 404）；
//   - 事件两形态的 payload 契约与投递对象（归零只发本人、增量作者跳过）；
//   - 主列表排除线程回复、threadReplyCount 批量、around 命中回复回退父消息、
//     频道已读水位只落在顶层消息、R3 显式回复 ID 回退、mention 口径不变。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// ---------------------------------------------------------------- helpers

// recordingBroadcaster 记录广播调用，用于断言「投递给谁」。
type recordingBroadcaster struct {
	channelCalls       []recordedChannelBroadcast
	channelExceptCalls []recordedChannelExceptBroadcast
	userCalls          []recordedUserBroadcast
}

type recordedChannelBroadcast struct {
	channelID string
	data      []byte
}

type recordedChannelExceptBroadcast struct {
	channelID     string
	excludeUserID string
	data          []byte
}

type recordedUserBroadcast struct {
	userID string
	data   []byte
}

func (r *recordingBroadcaster) BroadcastToChannel(channelID string, data []byte) {
	r.channelCalls = append(r.channelCalls, recordedChannelBroadcast{channelID: channelID, data: data})
}

func (r *recordingBroadcaster) BroadcastToChannelExcept(channelID, excludeUserID string, data []byte) {
	r.channelExceptCalls = append(r.channelExceptCalls, recordedChannelExceptBroadcast{
		channelID: channelID, excludeUserID: excludeUserID, data: data,
	})
}

func (r *recordingBroadcaster) BroadcastToUser(userID string, data []byte) {
	r.userCalls = append(r.userCalls, recordedUserBroadcast{userID: userID, data: data})
}

func newThreadMsg(t *testing.T, svc *Service, channelID, userID, content, parentID string) *model.Message {
	t.Helper()
	msg, err := svc.CreateMessage(channelID, userID, userID+"_name", "MEMBER", content, idgen.NextString(), parentID, "", nil, nil)
	if err != nil {
		t.Fatalf("create message (%s/%s/parent=%q): %v", channelID, userID, parentID, err)
	}
	return msg
}

func mustAppErrCode(t *testing.T, err error, want errors.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", want)
	}
	appErr, ok := err.(*errors.AppError)
	if !ok {
		t.Fatalf("expected *errors.AppError, got %T: %v", err, err)
	}
	if appErr.Code != want {
		t.Fatalf("expected error code %s, got %s", want, appErr.Code)
	}
}

func findThreadUnread(info *UnreadInfo, parentID string) (ThreadUnread, bool) {
	for _, u := range info.ThreadUnreads {
		if u.ParentID == parentID {
			return u, true
		}
	}
	return ThreadUnread{}, false
}

// newThreadRouter 构造只挂目标 handler 的 gin 路由，并注入 recording broadcaster。
func newThreadRouter(t *testing.T) (*Handler, *gin.Engine, *recordingBroadcaster) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	h := NewHandler(db, config.DefaultConfig(), nil, nil)
	rec := &recordingBroadcaster{}
	h.hub = rec

	auth := func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}
	r := gin.New()
	r.POST("/channels/:id/messages", auth, h.CreateMessage)
	r.GET("/channels/:id/messages", auth, h.GetMessages)
	r.GET("/messages/:id/thread", auth, h.GetThread)
	r.POST("/channels/:id/mark-read", auth, h.MarkChannelRead)
	r.GET("/channels/:id/unread", auth, h.GetUnreadCount)
	return h, r, rec
}

func doJSON(r *gin.Engine, method, path string, body []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body == nil {
		req, _ = http.NewRequest(method, path, nil)
	} else {
		req, _ = http.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------- 线程未读聚合

func TestThreadUnreadAggregation(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	parent := newThreadMsg(t, svc, "ch-agg", "user-2", "parent", "")
	newThreadMsg(t, svc, "ch-agg", "user-2", "reply-1", parent.ID)
	time.Sleep(3 * time.Millisecond)
	newThreadMsg(t, svc, "ch-agg", "user-2", "reply-2", parent.ID)

	info, err := svc.GetUnreadCount("user-1", "ch-agg")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	// 频道未读只算顶层消息：线程回复不进主列表计数。
	if info.UnreadCount != 1 {
		t.Errorf("channel unreadCount = %d, want 1 (top-level only)", info.UnreadCount)
	}
	if len(info.ThreadUnreads) != 1 {
		t.Fatalf("threadUnreads len = %d, want 1 (%+v)", len(info.ThreadUnreads), info.ThreadUnreads)
	}
	got := info.ThreadUnreads[0]
	if got.ParentID != parent.ID {
		t.Errorf("threadUnreads[0].parentId = %s, want %s", got.ParentID, parent.ID)
	}
	if got.Count != 2 {
		t.Errorf("threadUnreads[0].count = %d, want 2", got.Count)
	}
}

func TestThreadUnreadExcludesSelf(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	parent := newThreadMsg(t, svc, "ch-self", "user-2", "parent", "")
	newThreadMsg(t, svc, "ch-self", "user-1", "my own reply", parent.ID) // 本人回复不计未读
	time.Sleep(3 * time.Millisecond)
	newThreadMsg(t, svc, "ch-self", "user-2", "other reply", parent.ID)

	info, err := svc.GetUnreadCount("user-1", "ch-self")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	got, ok := findThreadUnread(info, parent.ID)
	if !ok {
		t.Fatalf("threadUnreads missing parent %s: %+v", parent.ID, info.ThreadUnreads)
	}
	if got.Count != 1 {
		t.Errorf("thread unread count = %d, want 1 (self reply excluded)", got.Count)
	}
}

func TestThreadUnreadOrderByLastAt(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	p1 := newThreadMsg(t, svc, "ch-order", "user-2", "p1", "")
	newThreadMsg(t, svc, "ch-order", "user-2", "p1-reply", p1.ID)
	time.Sleep(5 * time.Millisecond)
	p2 := newThreadMsg(t, svc, "ch-order", "user-2", "p2", "")
	newThreadMsg(t, svc, "ch-order", "user-2", "p2-reply", p2.ID)

	info, err := svc.GetUnreadCount("user-1", "ch-order")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if len(info.ThreadUnreads) != 2 {
		t.Fatalf("threadUnreads len = %d, want 2", len(info.ThreadUnreads))
	}
	if info.ThreadUnreads[0].ParentID != p2.ID {
		t.Errorf("threadUnreads[0] = %s, want latest thread %s", info.ThreadUnreads[0].ParentID, p2.ID)
	}
	if !info.ThreadUnreads[0].LastAt.After(info.ThreadUnreads[1].LastAt) {
		t.Errorf("threadUnreads not ordered by lastAt desc: %+v", info.ThreadUnreads)
	}
}

func TestThreadUnreadCap20(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	for i := 0; i < 21; i++ {
		p := newThreadMsg(t, svc, "ch-cap", "user-2", "parent", "")
		newThreadMsg(t, svc, "ch-cap", "user-2", "reply", p.ID)
	}
	info, err := svc.GetUnreadCount("user-1", "ch-cap")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if len(info.ThreadUnreads) != 20 {
		t.Errorf("threadUnreads len = %d, want cap 20", len(info.ThreadUnreads))
	}
}

// ---------------------------------------------------------------- 线程已读位

func TestMarkThreadReadDoesNotAffectChannelUnread(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	parent := newThreadMsg(t, svc, "ch-mt", "user-2", "parent", "")
	reply := newThreadMsg(t, svc, "ch-mt", "user-2", "reply", parent.ID)

	before, err := svc.GetUnreadCount("user-1", "ch-mt")
	if err != nil {
		t.Fatalf("GetUnreadCount before: %v", err)
	}
	if before.UnreadCount != 1 || len(before.ThreadUnreads) != 1 {
		t.Fatalf("unexpected baseline: %+v", before)
	}

	if _, err := svc.MarkThreadRead("user-1", "ch-mt", parent.ID, reply.ID); err != nil {
		t.Fatalf("MarkThreadRead: %v", err)
	}

	after, err := svc.GetUnreadCount("user-1", "ch-mt")
	if err != nil {
		t.Fatalf("GetUnreadCount after: %v", err)
	}
	if after.UnreadCount != before.UnreadCount {
		t.Errorf("channel unread changed by thread mark-read: %d -> %d", before.UnreadCount, after.UnreadCount)
	}
	if len(after.ThreadUnreads) != 0 {
		t.Errorf("thread unread not cleared: %+v", after.ThreadUnreads)
	}
	// 线程已读不得写 user_channel_reads（频道水位不动）。
	var channelReadRows int64
	if err := db.Model(&model.UserChannelRead{}).Where("user_id = ?", "user-1").Count(&channelReadRows).Error; err != nil {
		t.Fatalf("count user_channel_reads: %v", err)
	}
	if channelReadRows != 0 {
		t.Errorf("user_channel_reads rows = %d, want 0", channelReadRows)
	}
}

func TestMarkThreadReadIdempotent(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	parent := newThreadMsg(t, svc, "ch-idem", "user-2", "parent", "")
	reply := newThreadMsg(t, svc, "ch-idem", "user-2", "reply", parent.ID)

	for i := 0; i < 2; i++ {
		if _, err := svc.MarkThreadRead("user-1", "ch-idem", parent.ID, reply.ID); err != nil {
			t.Fatalf("MarkThreadRead #%d: %v", i+1, err)
		}
	}
	var rows int64
	if err := db.Model(&model.MessageThreadRead{}).
		Where("user_id = ? AND parent_id = ?", "user-1", parent.ID).Count(&rows).Error; err != nil {
		t.Fatalf("count message_thread_reads: %v", err)
	}
	if rows != 1 {
		t.Errorf("message_thread_reads rows = %d, want 1 (upsert)", rows)
	}
	info, err := svc.GetUnreadCount("user-1", "ch-idem")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if len(info.ThreadUnreads) != 0 {
		t.Errorf("thread unread not cleared after idempotent mark: %+v", info.ThreadUnreads)
	}
}

func TestMarkThreadReadInvalidParent404(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	parent := newThreadMsg(t, svc, "ch-bad", "user-2", "parent", "")
	newThreadMsg(t, svc, "ch-bad", "user-2", "reply", parent.ID)
	emptyParent := newThreadMsg(t, svc, "ch-bad", "user-2", "no replies", "")
	otherChannelParent := newThreadMsg(t, svc, "ch-other", "user-2", "parent elsewhere", "")

	t.Run("nonexistent parent", func(t *testing.T) {
		mustAppErrCode(t, markThreadReadErr(svc, "user-1", "ch-bad", "no-such-parent", ""), errors.MESSAGE_NOT_FOUND)
	})
	t.Run("parent in another channel", func(t *testing.T) {
		mustAppErrCode(t, markThreadReadErr(svc, "user-1", "ch-bad", otherChannelParent.ID, ""), errors.MESSAGE_NOT_FOUND)
	})
	t.Run("parent without replies", func(t *testing.T) {
		mustAppErrCode(t, markThreadReadErr(svc, "user-1", "ch-bad", emptyParent.ID, ""), errors.MESSAGE_NOT_FOUND)
	})
}

func TestUnreadResponseBackwardCompatible(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	info, err := svc.GetUnreadCount("user-1", "ch-empty")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal UnreadInfo: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal UnreadInfo: %v", err)
	}
	for _, key := range []string{"unreadCount", "mentionCount", "firstUnreadMessageId", "threadUnreads"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("unread response missing legacy field %q: %s", key, raw)
		}
	}
	if string(decoded["threadUnreads"]) != "[]" {
		t.Errorf("threadUnreads = %s, want [] (empty array, never null)", decoded["threadUnreads"])
	}
}

// ---------------------------------------------------------------- 主列表 / 富化

func TestGetMessagesTopLevelOnly(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	parent := newThreadMsg(t, svc, "ch-top", "user-2", "parent", "")
	newThreadMsg(t, svc, "ch-top", "user-2", "reply", parent.ID)
	time.Sleep(3 * time.Millisecond)
	newThreadMsg(t, svc, "ch-top", "user-2", "second top-level", "")

	msgs, _, err := svc.GetMessages("ch-top", "", 50)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("GetMessages returned %d messages, want 2 top-level only", len(msgs))
	}
	for _, m := range msgs {
		if m.ParentID != nil && *m.ParentID != "" {
			t.Errorf("thread reply leaked into main list: %s (parent=%s)", m.ID, *m.ParentID)
		}
	}
}

func TestThreadReplyCountBatch(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	svc := h.service
	parent := newThreadMsg(t, svc, "ch-rc", "user-2", "parent", "")
	newThreadMsg(t, svc, "ch-rc", "user-2", "r1", parent.ID)
	newThreadMsg(t, svc, "ch-rc", "user-2", "r2", parent.ID)
	other := newThreadMsg(t, svc, "ch-rc", "user-2", "no replies", "")

	w := doJSON(r, http.MethodGet, "/channels/ch-rc/messages", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Items []struct {
				ID               string `json:"id"`
				ThreadReplyCount int64  `json:"threadReplyCount"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	counts := map[string]int64{}
	for _, it := range resp.Data.Items {
		counts[it.ID] = it.ThreadReplyCount
	}
	if counts[parent.ID] != 2 {
		t.Errorf("parent threadReplyCount = %d, want 2", counts[parent.ID])
	}
	if counts[other.ID] != 0 {
		t.Errorf("unrelated message threadReplyCount = %d, want 0", counts[other.ID])
	}
}

func TestGetMessagesAroundThreadReplyFallsBackToParent(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	svc := h.service
	parent := newThreadMsg(t, svc, "ch-around", "user-2", "parent", "")
	time.Sleep(3 * time.Millisecond)
	reply := newThreadMsg(t, svc, "ch-around", "user-2", "reply", parent.ID)
	time.Sleep(3 * time.Millisecond)
	next := newThreadMsg(t, svc, "ch-around", "user-2", "next top-level", "")

	w := doJSON(r, http.MethodGet, "/channels/ch-around/messages?around="+reply.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("around status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			ThreadParentID string `json:"threadParentId"`
			FocusMessageID string `json:"focusMessageId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal around: %v", err)
	}
	if resp.Data.ThreadParentID != parent.ID {
		t.Errorf("threadParentId = %q, want %q", resp.Data.ThreadParentID, parent.ID)
	}
	if resp.Data.FocusMessageID != reply.ID {
		t.Errorf("focusMessageId = %q, want reply %q", resp.Data.FocusMessageID, reply.ID)
	}
	seen := map[string]bool{}
	for _, it := range resp.Data.Items {
		seen[it.ID] = true
	}
	if !seen[parent.ID] {
		t.Errorf("around page missing parent %s: %+v", parent.ID, seen)
	}
	if seen[reply.ID] {
		t.Errorf("around page must not contain the thread reply itself")
	}
	if !seen[next.ID] {
		t.Errorf("around page missing sibling %s: %+v", next.ID, seen)
	}
}

func TestGetMessagesAroundParentMissing(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	newThreadMsg(t, h.service, "ch-miss", "user-2", "only top", "")

	w := doJSON(r, http.MethodGet, "/channels/ch-miss/messages?around=does-not-exist", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("around missing status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Code != string(errors.MESSAGE_NOT_FOUND) {
		t.Errorf("code = %s, want %s", resp.Code, errors.MESSAGE_NOT_FOUND)
	}
}

func TestExcludesThreadReplies(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	p1 := newThreadMsg(t, svc, "ch-ex", "user-2", "p1", "")
	time.Sleep(3 * time.Millisecond)
	reply := newThreadMsg(t, svc, "ch-ex", "user-2", "reply under p1", p1.ID)
	time.Sleep(3 * time.Millisecond)
	p2 := newThreadMsg(t, svc, "ch-ex", "user-2", "p2", "")
	time.Sleep(3 * time.Millisecond)
	p3 := newThreadMsg(t, svc, "ch-ex", "user-2", "p3", "")

	info, err := svc.AroundMessages("ch-ex", p2.ID, 50)
	if err != nil {
		t.Fatalf("AroundMessages: %v", err)
	}
	for _, m := range info.Messages {
		if m.ID == reply.ID {
			t.Errorf("thread reply leaked into around page")
		}
	}
	if len(info.Messages) != 3 {
		t.Errorf("around page len = %d, want 3 top-level (p1,p2,p3)", len(info.Messages))
	}
	ids := map[string]bool{info.Messages[0].ID: true}
	for _, m := range info.Messages {
		ids[m.ID] = true
	}
	if !ids[p1.ID] || !ids[p3.ID] {
		t.Errorf("around page missing neighbours: %+v", ids)
	}
}

// ---------------------------------------------------------------- 频道已读水位

func TestMarkChannelReadWatermarkLatestTopLevel(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	svc := h.service
	parent := newThreadMsg(t, svc, "ch-wm", "user-2", "parent", "")
	time.Sleep(3 * time.Millisecond)
	reply := newThreadMsg(t, svc, "ch-wm", "user-2", "latest is a reply", parent.ID)

	w := doJSON(r, http.MethodPost, "/channels/ch-wm/mark-read", []byte(`{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("mark-read status = %d, body=%s", w.Code, w.Body.String())
	}
	var read model.UserChannelRead
	if err := h.db.Where("user_id = ? AND channel_id = ?", "user-1", "ch-wm").First(&read).Error; err != nil {
		t.Fatalf("load user_channel_reads: %v", err)
	}
	if read.LastReadMessageID != parent.ID {
		t.Errorf("watermark = %s, want latest top-level %s (reply %s must not advance it)",
			read.LastReadMessageID, parent.ID, reply.ID)
	}
}

func TestMarkChannelReadReplyIdFallback(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	p1 := newThreadMsg(t, svc, "ch-r3", "user-2", "p1", "")
	time.Sleep(3 * time.Millisecond)
	p2 := newThreadMsg(t, svc, "ch-r3", "user-2", "p2", "")
	time.Sleep(3 * time.Millisecond)
	reply := newThreadMsg(t, svc, "ch-r3", "user-2", "reply to p1, later than p2", p1.ID)

	if err := svc.MarkChannelRead("user-1", "ch-r3", reply.ID); err != nil {
		t.Fatalf("MarkChannelRead: %v", err)
	}
	var read model.UserChannelRead
	if err := db.Where("user_id = ? AND channel_id = ?", "user-1", "ch-r3").First(&read).Error; err != nil {
		t.Fatalf("load user_channel_reads: %v", err)
	}
	// R3：显式传线程回复 ID 时回退到该回复之前最近的顶层消息（p2），不得写 0 值。
	if read.LastReadMessageID != p2.ID {
		t.Errorf("watermark = %s, want fallback top-level %s", read.LastReadMessageID, p2.ID)
	}
}

func TestMarkChannelReadReplyIdWithoutTopLevelKeepsWatermark(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	// 线程回复之前没有任何顶层消息（parent 自身的 created_at 更晚是不可能的，
	// 因此直接构造：先建 parent，再把 parent 的 created_at 推后，使回退查询落空）。
	parent := newThreadMsg(t, svc, "ch-r3b", "user-2", "parent", "")
	reply := newThreadMsg(t, svc, "ch-r3b", "user-2", "reply", parent.ID)
	if err := db.Model(&model.Message{}).Where("id = ?", parent.ID).
		Update("created_at", reply.CreatedAt.Add(time.Second)).Error; err != nil {
		t.Fatalf("shift parent created_at: %v", err)
	}
	if err := svc.MarkChannelRead("user-1", "ch-r3b", reply.ID); err != nil {
		t.Fatalf("MarkChannelRead: %v", err)
	}
	var rows int64
	if err := db.Model(&model.UserChannelRead{}).Where("user_id = ?", "user-1").Count(&rows).Error; err != nil {
		t.Fatalf("count user_channel_reads: %v", err)
	}
	if rows != 0 {
		t.Errorf("user_channel_reads rows = %d, want 0 (watermark must not be zeroed)", rows)
	}
}

func TestMentionCountKeepsResponding(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	db := h.db
	if err := db.Create(&model.Channel{ID: "ch-mn", SpaceID: "space-mn", Name: "General", Type: "TEXT"}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Create(&model.User{ID: "user_carol", Username: "carol_mn", DisplayName: "卡罗尔", Email: "carol_mn@x.com"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: "space-mn", UserID: "user_carol", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	parent := newThreadMsg(t, h.service, "ch-mn", "user-2", "parent", "")
	// 已读行必须存在，mention 计数走增量 upsert。
	if err := db.Create(&model.UserChannelRead{
		ID: idgen.NextString(), UserID: "user_carol", ChannelID: "ch-mn",
		MessageID: parent.ID, LastReadMessageID: parent.ID, ReadAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed user_channel_reads: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{"content": "线程回复里 @卡罗尔 也算提及", "parentId": parent.ID})
	w := doJSON(r, http.MethodPost, "/channels/ch-mn/messages", body)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create thread reply status = %d, body=%s", w.Code, w.Body.String())
	}
	var read model.UserChannelRead
	if err := db.Where("user_id = ? AND channel_id = ?", "user_carol", "ch-mn").First(&read).Error; err != nil {
		t.Fatalf("load user_channel_reads: %v", err)
	}
	// 口径锁定：线程回复里的 @ 仍计入频道 mentionCount（不被 parent_id 过滤误伤）。
	if read.MentionCount != 1 {
		t.Errorf("mentionCount = %d, want 1 (thread reply mention must still count)", read.MentionCount)
	}
}

// TestThreadUnreadIndependent 锁 DES §6.3 时序：频道水位与线程读位相互独立。
func TestThreadUnreadIndependent(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	parent := newThreadMsg(t, svc, "ch-ind", "user-2", "parent", "")
	time.Sleep(3 * time.Millisecond)
	reply1 := newThreadMsg(t, svc, "ch-ind", "user-2", "reply-1", parent.ID)

	// 1) 只读频道到 parent：线程仍有 1 条未读，频道 0。
	if err := svc.MarkChannelRead("user-1", "ch-ind", parent.ID); err != nil {
		t.Fatalf("MarkChannelRead: %v", err)
	}
	info, err := svc.GetUnreadCount("user-1", "ch-ind")
	if err != nil {
		t.Fatalf("GetUnreadCount step1: %v", err)
	}
	if info.UnreadCount != 0 {
		t.Errorf("step1 channel unread = %d, want 0", info.UnreadCount)
	}
	if got, ok := findThreadUnread(info, parent.ID); !ok || got.Count != 1 {
		t.Errorf("step1 thread unread = %+v (ok=%v), want count 1", got, ok)
	}

	// 2) 再读线程：线程 0，频道仍 0。
	if _, err := svc.MarkThreadRead("user-1", "ch-ind", parent.ID, reply1.ID); err != nil {
		t.Fatalf("MarkThreadRead: %v", err)
	}
	info, err = svc.GetUnreadCount("user-1", "ch-ind")
	if err != nil {
		t.Fatalf("GetUnreadCount step2: %v", err)
	}
	if info.UnreadCount != 0 || len(info.ThreadUnreads) != 0 {
		t.Errorf("step2 unread = %+v, want all zero", info)
	}

	// 3) 新线程回复：线程 +1，频道不变。
	time.Sleep(3 * time.Millisecond)
	newThreadMsg(t, svc, "ch-ind", "user-2", "reply-2", parent.ID)
	info, err = svc.GetUnreadCount("user-1", "ch-ind")
	if err != nil {
		t.Fatalf("GetUnreadCount step3: %v", err)
	}
	if info.UnreadCount != 0 {
		t.Errorf("step3 channel unread = %d, want 0", info.UnreadCount)
	}
	if got, ok := findThreadUnread(info, parent.ID); !ok || got.Count != 1 {
		t.Errorf("step3 thread unread = %+v (ok=%v), want count 1", got, ok)
	}

	// 4) 新顶层消息：频道 +1，线程保持 1。
	time.Sleep(3 * time.Millisecond)
	newThreadMsg(t, svc, "ch-ind", "user-2", "new top-level", "")
	info, err = svc.GetUnreadCount("user-1", "ch-ind")
	if err != nil {
		t.Fatalf("GetUnreadCount step4: %v", err)
	}
	if info.UnreadCount != 1 {
		t.Errorf("step4 channel unread = %d, want 1", info.UnreadCount)
	}
	if got, ok := findThreadUnread(info, parent.ID); !ok || got.Count != 1 {
		t.Errorf("step4 thread unread = %+v (ok=%v), want count 1", got, ok)
	}
}

// TestGetThreadReplyCount 覆盖 DES §7.7 / §11.1：GetThread 回填 threadReplyCount。
func TestGetThreadReplyCount(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	if err := h.db.Create(&model.Channel{ID: "ch-gt", SpaceID: "space-gt", Name: "General", Type: "TEXT", Visibility: "public"}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	parent := newThreadMsg(t, h.service, "ch-gt", "user-2", "parent", "")
	newThreadMsg(t, h.service, "ch-gt", "user-2", "r1", parent.ID)
	newThreadMsg(t, h.service, "ch-gt", "user-2", "r2", parent.ID)

	w := doJSON(r, http.MethodGet, "/messages/"+parent.ID+"/thread", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get thread status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			ThreadReplyCount int64 `json:"threadReplyCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal thread response: %v", err)
	}
	if resp.Data.ThreadReplyCount != 2 {
		t.Errorf("threadReplyCount = %d, want 2", resp.Data.ThreadReplyCount)
	}
	if len(resp.Data.Messages) != 2 {
		t.Errorf("thread messages len = %d, want 2", len(resp.Data.Messages))
	}
}

// ---------------------------------------------------------------- 事件契约与投递对象

func TestThreadUnreadChangedResetPayloadContract(t *testing.T) {
	payload := threadUnreadResetPayload("ch-1", "parent-1", "msg-9")
	if payload["channelId"] != "ch-1" || payload["parentId"] != "parent-1" {
		t.Errorf("reset payload ids wrong: %+v", payload)
	}
	if payload["unreadCount"] != 0 {
		t.Errorf("reset payload unreadCount = %v, want 0", payload["unreadCount"])
	}
	if payload["lastReadMessageId"] != "msg-9" {
		t.Errorf("reset payload lastReadMessageId = %v, want msg-9", payload["lastReadMessageId"])
	}
	if _, ok := payload["delta"]; ok {
		t.Errorf("reset payload must not carry delta: %+v", payload)
	}

	delta := threadUnreadDeltaPayload("ch-1", "parent-1", "msg-10", "user-2")
	if delta["delta"] != 1 {
		t.Errorf("delta payload delta = %v, want 1", delta["delta"])
	}
	for _, key := range []string{"channelId", "parentId", "messageId", "authorId"} {
		if delta[key] == nil {
			t.Errorf("delta payload missing %q: %+v", key, delta)
		}
	}
	if _, ok := delta["unreadCount"]; ok {
		t.Errorf("delta payload must not carry unreadCount: %+v", delta)
	}
}

func TestThreadUnreadChangedResetBroadcastToSelfOnly(t *testing.T) {
	h, r, rec := newThreadRouter(t)
	parent := newThreadMsg(t, h.service, "ch-reset", "user-2", "parent", "")
	reply := newThreadMsg(t, h.service, "ch-reset", "user-2", "reply", parent.ID)

	body, _ := json.Marshal(map[string]string{"threadParentId": parent.ID, "lastMessageId": reply.ID})
	w := doJSON(r, http.MethodPost, "/channels/ch-reset/mark-read", body)
	if w.Code != http.StatusOK {
		t.Fatalf("thread mark-read status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(rec.userCalls) != 1 {
		t.Fatalf("BroadcastToUser calls = %d, want 1 (reason: thread reset must reach only the reader)", len(rec.userCalls))
	}
	if rec.userCalls[0].userID != "user-1" {
		t.Errorf("BroadcastToUser target = %s, want user-1", rec.userCalls[0].userID)
	}
	var ev struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(rec.userCalls[0].data, &ev); err != nil {
		t.Fatalf("unmarshal reset event: %v", err)
	}
	if ev.Type != "thread_unread_changed" {
		t.Errorf("event type = %s, want thread_unread_changed", ev.Type)
	}
	if ev.Payload["parentId"] != parent.ID {
		t.Errorf("payload.parentId = %v, want %s", ev.Payload["parentId"], parent.ID)
	}
	if ev.Payload["unreadCount"] != float64(0) {
		t.Errorf("payload.unreadCount = %v, want 0", ev.Payload["unreadCount"])
	}
	if len(rec.channelCalls) != 0 || len(rec.channelExceptCalls) != 0 {
		t.Errorf("thread reset must not broadcast to channel: channel=%d except=%d",
			len(rec.channelCalls), len(rec.channelExceptCalls))
	}
}

func TestThreadUnreadChangedDeltaChannelAuthorSkipped(t *testing.T) {
	h, r, rec := newThreadRouter(t)
	parent := newThreadMsg(t, h.service, "ch-delta", "user-2", "parent", "")

	body, _ := json.Marshal(map[string]interface{}{"content": "reply via handler", "parentId": parent.ID})
	w := doJSON(r, http.MethodPost, "/channels/ch-delta/messages", body)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create thread reply status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(rec.channelExceptCalls) != 1 {
		t.Fatalf("BroadcastToChannelExcept calls = %d, want 1 (reason: delta must skip the author only)",
			len(rec.channelExceptCalls))
	}
	call := rec.channelExceptCalls[0]
	if call.channelID != "ch-delta" {
		t.Errorf("broadcast channel = %s, want ch-delta", call.channelID)
	}
	if call.excludeUserID != "user-1" {
		t.Errorf("excluded user = %s, want author user-1", call.excludeUserID)
	}
	var ev struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(call.data, &ev); err != nil {
		t.Fatalf("unmarshal delta event: %v", err)
	}
	if ev.Type != "thread_unread_changed" {
		t.Errorf("event type = %s, want thread_unread_changed", ev.Type)
	}
	if ev.Payload["delta"] != float64(1) {
		t.Errorf("payload.delta = %v, want 1", ev.Payload["delta"])
	}
	if ev.Payload["parentId"] != parent.ID {
		t.Errorf("payload.parentId = %v, want %s", ev.Payload["parentId"], parent.ID)
	}
	if len(rec.userCalls) != 0 {
		t.Errorf("delta event must not be delivered via BroadcastToUser: %+v", rec.userCalls)
	}
}

// TestThreadSoftDeletedRepliesExcluded（D1，coordinator 复核）：被软删除的线程回复
// 不得计入线程未读聚合，也不得计入 threadReplyCount，且与 GetThread 的可见条数一致。
func TestThreadSoftDeletedRepliesExcluded(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	parent := newThreadMsg(t, svc, "ch-sd", "user-2", "parent", "")
	removed := newThreadMsg(t, svc, "ch-sd", "user-2", "reply-to-delete", parent.ID)
	kept := newThreadMsg(t, svc, "ch-sd", "user-2", "reply-kept", parent.ID)

	if err := db.Where("id = ?", removed.ID).Delete(&model.Message{}).Error; err != nil {
		t.Fatalf("soft delete reply: %v", err)
	}

	info, err := svc.GetUnreadCount("user-1", "ch-sd")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	unread, ok := findThreadUnread(info, parent.ID)
	if !ok || unread.Count != 1 {
		t.Fatalf("thread unread should count only visible replies (want 1), got %+v (ok=%v)", unread, ok)
	}

	counts, err := svc.ThreadReplyCounts([]string{parent.ID})
	if err != nil {
		t.Fatalf("ThreadReplyCounts: %v", err)
	}
	if counts[parent.ID] != 1 {
		t.Fatalf("threadReplyCount should exclude soft-deleted reply (want 1), got %d", counts[parent.ID])
	}

	messages, _, err := svc.GetThread(parent.ID, 20)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != kept.ID {
		t.Fatalf("GetThread should return only the visible reply, got %d messages", len(messages))
	}
}

// markThreadReadErr 适配 MarkThreadRead 返回解析后 lastMessageID 的签名（D3）。
func markThreadReadErr(svc *Service, userID, channelID, parentID, lastMessageID string) error {
	_, err := svc.MarkThreadRead(userID, channelID, parentID, lastMessageID)
	return err
}

// TestGetThreadTotalNotTruncatedByLimit（D2，coordinator 复核）：GetThread 的 total 必须是
// 线程回复的真实总数，不随 limit 截断；且与 threadReplyCount 同值。
func TestGetThreadTotalNotTruncatedByLimit(t *testing.T) {
	h, r, _ := newThreadRouter(t)
	if err := h.db.Create(&model.Channel{ID: "ch-total", SpaceID: "space-total", Name: "total", Type: "text", Visibility: "public"}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	parent := newThreadMsg(t, h.service, "ch-total", "user-2", "parent", "")
	for i := 1; i <= 3; i++ {
		newThreadMsg(t, h.service, "ch-total", "user-2", "reply", parent.ID)
		time.Sleep(3 * time.Millisecond)
	}

	w := doJSON(r, http.MethodGet, "/messages/"+parent.ID+"/thread?limit=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GetThread status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Messages         []model.Message `json:"messages"`
			HasMore          bool            `json:"hasMore"`
			Total            int64           `json:"total"`
			ThreadReplyCount int64           `json:"threadReplyCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal GetThread response: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Data.Messages) != 2 || !resp.Data.HasMore {
		t.Errorf("limit=2 should yield 2 messages + hasMore: got %d messages, hasMore=%v", len(resp.Data.Messages), resp.Data.HasMore)
	}
	if resp.Data.Total != 3 {
		t.Errorf("total = %d, want 3 (真实回复总数，不得被 limit 截断)", resp.Data.Total)
	}
	if resp.Data.ThreadReplyCount != 3 {
		t.Errorf("threadReplyCount = %d, want 3", resp.Data.ThreadReplyCount)
	}
}

// TestThreadResetBroadcastResolvesLatestReplyID（D3，coordinator 复核）：线程 mark-read
// 未传 lastMessageId 时，服务端解析出线程最新回复 ID，归零广播里必须是该真实 ID 而非空串。
func TestThreadResetBroadcastResolvesLatestReplyID(t *testing.T) {
	h, r, rec := newThreadRouter(t)
	parent := newThreadMsg(t, h.service, "ch-resolve", "user-2", "parent", "")
	newThreadMsg(t, h.service, "ch-resolve", "user-2", "older-reply", parent.ID)
	time.Sleep(3 * time.Millisecond)
	latest := newThreadMsg(t, h.service, "ch-resolve", "user-2", "latest-reply", parent.ID)

	// 故意只传 threadParentId，不带 lastMessageId，验证 service 解析后回填广播。
	body, _ := json.Marshal(map[string]string{"threadParentId": parent.ID})
	w := doJSON(r, http.MethodPost, "/channels/ch-resolve/mark-read", body)
	if w.Code != http.StatusOK {
		t.Fatalf("thread mark-read status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(rec.userCalls) != 1 {
		t.Fatalf("BroadcastToUser calls = %d, want 1", len(rec.userCalls))
	}
	var ev struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(rec.userCalls[0].data, &ev); err != nil {
		t.Fatalf("unmarshal reset event: %v", err)
	}
	if ev.Type != "thread_unread_changed" {
		t.Fatalf("event type = %s, want thread_unread_changed", ev.Type)
	}
	got, _ := ev.Payload["lastReadMessageId"].(string)
	if got == "" {
		t.Fatalf("payload.lastReadMessageId must not be empty when lastMessageId omitted (D3 回归)")
	}
	if got != latest.ID {
		t.Errorf("payload.lastReadMessageId = %s, want 线程最新回复 %s", got, latest.ID)
	}
}

// ---------------------------------------------------------------- D-1 / S-01 已读水位语义

// storedThreadRead 读取线程已读行，便于断言落库值（read_at / last_read_message_id）。
func storedThreadRead(t *testing.T, db *gorm.DB, userID, parentID string) model.MessageThreadRead {
	t.Helper()
	var row model.MessageThreadRead
	if err := db.Where("user_id = ? AND parent_id = ?", userID, parentID).First(&row).Error; err != nil {
		t.Fatalf("load message_thread_reads (user=%s parent=%s): %v", userID, parentID, err)
	}
	return row
}

// assertApproxTime 允许 SQLite TEXT 往返的亚毫秒级精度损失。
func assertApproxTime(t *testing.T, got time.Time, want time.Time, label string) {
	t.Helper()
	if d := got.Sub(want); d > time.Millisecond || d < -time.Millisecond {
		t.Errorf("%s = %v, want ≈ %v (delta=%v)", label, got, want, d)
	}
}

// TestMarkThreadReadDoesNotClearReplyCreatedAfterReport 锁定 D-1 核心回归：
// 客户端在 fetchThread 返回前就上报已读时，「上报瞬间已存在但客户端尚未渲染」的回复
// 不得被算作已读。旧实现 read_at=time.Now() 会把它静默清零（未读丢失）。
func TestMarkThreadReadDoesNotClearReplyCreatedAfterReport(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	parent := newThreadMsg(t, svc, "ch-d1", "user-2", "parent", "")
	reply1 := newThreadMsg(t, svc, "ch-d1", "user-2", "reply-1", parent.ID)
	reply2 := newThreadMsg(t, svc, "ch-d1", "user-3", "reply-2 arrived during report", parent.ID)

	// reply2 的 created_at 压到 reply1 之后 1ms：晚于客户端已知水位、早于上报时刻。
	arrivedAt := reply1.CreatedAt.Add(time.Millisecond)
	if err := db.Model(&model.Message{}).Where("id = ?", reply2.ID).
		Update("created_at", arrivedAt).Error; err != nil {
		t.Fatalf("shift reply2 created_at: %v", err)
	}

	res, err := svc.MarkThreadRead("user-1", "ch-d1", parent.ID, reply1.ID)
	if err != nil {
		t.Fatalf("MarkThreadRead: %v", err)
	}
	if !res.Advanced {
		t.Fatalf("first report must advance watermark, got Advanced=false")
	}

	row := storedThreadRead(t, db, "user-1", parent.ID)
	assertApproxTime(t, row.ReadAt, reply1.CreatedAt, "read_at")
	if !row.ReadAt.Before(arrivedAt) {
		t.Errorf("read_at = %v must be before reply2.created_at = %v (水位不得取上报时刻)", row.ReadAt, arrivedAt)
	}

	info, err := svc.GetUnreadCount("user-1", "ch-d1")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	tu, ok := findThreadUnread(info, parent.ID)
	if !ok || tu.Count != 1 {
		t.Fatalf("thread unread = %+v (found=%v), want reply2 counted once (未被误清零)", info.ThreadUnreads, ok)
	}
}

// TestMarkThreadReadNoRegressOnOlderLastMessageId 锁定 D-1 单调性：
// 重复上报更早的 lastMessageId 不得回退 read_at / last_read_message_id，也不得新增未读。
func TestMarkThreadReadNoRegressOnOlderLastMessageId(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	parent := newThreadMsg(t, svc, "ch-d1b", "user-2", "parent", "")
	reply1 := newThreadMsg(t, svc, "ch-d1b", "user-2", "reply-1", parent.ID)
	time.Sleep(2 * time.Millisecond)
	reply2 := newThreadMsg(t, svc, "ch-d1b", "user-2", "reply-2", parent.ID)

	first, err := svc.MarkThreadRead("user-1", "ch-d1b", parent.ID, reply2.ID)
	if err != nil {
		t.Fatalf("MarkThreadRead(reply2): %v", err)
	}
	if !first.Advanced || first.LastReadMessageID != reply2.ID {
		t.Fatalf("first report = %+v, want Advanced=true/last=%s", first, reply2.ID)
	}

	second, err := svc.MarkThreadRead("user-1", "ch-d1b", parent.ID, reply1.ID)
	if err != nil {
		t.Fatalf("MarkThreadRead(reply1): %v", err)
	}
	if second.Advanced {
		t.Errorf("older lastMessageId must not advance watermark, got Advanced=true")
	}
	if second.LastReadMessageID != reply2.ID {
		t.Errorf("returned lastReadMessageID = %s, want 水位保持 %s", second.LastReadMessageID, reply2.ID)
	}

	row := storedThreadRead(t, db, "user-1", parent.ID)
	assertApproxTime(t, row.ReadAt, reply2.CreatedAt, "read_at 回退检查")
	if row.LastReadMessageID != reply2.ID {
		t.Errorf("stored last_read_message_id = %s, want %s (不得回退)", row.LastReadMessageID, reply2.ID)
	}

	info, err := svc.GetUnreadCount("user-1", "ch-d1b")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if len(info.ThreadUnreads) != 0 {
		t.Errorf("thread unread after regressive report = %+v, want none", info.ThreadUnreads)
	}
}

// TestMarkThreadReadUnadvancedReportSkipsBroadcast 锁定 D-1 第 4 条（D-S01-b）：
// 水位未推进时不广播归零事件，返回 200 且语义为 "thread already read"。
func TestMarkThreadReadUnadvancedReportSkipsBroadcast(t *testing.T) {
	h, r, rec := newThreadRouter(t)
	parent := newThreadMsg(t, h.service, "ch-d1c", "user-2", "parent", "")
	reply1 := newThreadMsg(t, h.service, "ch-d1c", "user-2", "reply-1", parent.ID)
	time.Sleep(2 * time.Millisecond)
	reply2 := newThreadMsg(t, h.service, "ch-d1c", "user-2", "reply-2", parent.ID)

	post := func(lastMessageID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"threadParentId": parent.ID, "lastMessageId": lastMessageID})
		return doJSON(r, http.MethodPost, "/channels/ch-d1c/mark-read", body)
	}

	if w := post(reply2.ID); w.Code != http.StatusOK {
		t.Fatalf("first mark-read status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(rec.userCalls) != 1 {
		t.Fatalf("BroadcastToUser calls = %d, want 1 after first report", len(rec.userCalls))
	}

	w := post(reply1.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("regressive mark-read status = %d, want 200 (status 不变), body=%s", w.Code, w.Body.String())
	}
	if len(rec.userCalls) != 1 {
		t.Errorf("BroadcastToUser calls = %d, want 1（水位未推进时不得广播归零事件）", len(rec.userCalls))
	}
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Code != "OK" {
		t.Errorf("response code = %s, want OK", resp.Code)
	}
	if resp.Data["message"] != "thread already read" {
		t.Errorf("response message = %v, want \"thread already read\"", resp.Data["message"])
	}
}

// TestMarkThreadReadForeignMessage404 锁定 D-1 第 2 条（Low-1 归属校验）：
// lastMessageId 必须属于该线程，否则可用同频道其它线程/顶层消息抬高水位。
func TestMarkThreadReadForeignMessage404(t *testing.T) {
	svc := NewService(testutil.MustSetupTestDB())
	parentA := newThreadMsg(t, svc, "ch-d1d", "user-2", "parent-a", "")
	newThreadMsg(t, svc, "ch-d1d", "user-2", "reply-a", parentA.ID)
	parentB := newThreadMsg(t, svc, "ch-d1d", "user-2", "parent-b", "")
	replyB := newThreadMsg(t, svc, "ch-d1d", "user-2", "reply-b", parentB.ID)
	topLevel := newThreadMsg(t, svc, "ch-d1d", "user-2", "top-level", "")

	t.Run("其它线程的回复", func(t *testing.T) {
		mustAppErrCode(t, markThreadReadErr(svc, "user-1", "ch-d1d", parentA.ID, replyB.ID), errors.MESSAGE_NOT_FOUND)
	})
	t.Run("同频道顶层消息", func(t *testing.T) {
		mustAppErrCode(t, markThreadReadErr(svc, "user-1", "ch-d1d", parentA.ID, topLevel.ID), errors.MESSAGE_NOT_FOUND)
	})
	t.Run("父消息自身不是回复", func(t *testing.T) {
		mustAppErrCode(t, markThreadReadErr(svc, "user-1", "ch-d1d", parentA.ID, parentA.ID), errors.MESSAGE_NOT_FOUND)
	})
}

// TestMarkThreadReadDefaultUsesLatestReplyCreatedAt 锁定 D-1 第 1 条后半：
// 未传 lastMessageId 时水位取该线程最新回复的 created_at（而非上报时刻）。
func TestMarkThreadReadDefaultUsesLatestReplyCreatedAt(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	parent := newThreadMsg(t, svc, "ch-d1e", "user-2", "parent", "")
	newThreadMsg(t, svc, "ch-d1e", "user-2", "reply-1", parent.ID)
	time.Sleep(2 * time.Millisecond)
	reply2 := newThreadMsg(t, svc, "ch-d1e", "user-2", "reply-2", parent.ID)

	res, err := svc.MarkThreadRead("user-1", "ch-d1e", parent.ID, "")
	if err != nil {
		t.Fatalf("MarkThreadRead(default): %v", err)
	}
	if res.LastReadMessageID != reply2.ID {
		t.Errorf("resolved lastReadMessageID = %s, want latest reply %s", res.LastReadMessageID, reply2.ID)
	}
	row := storedThreadRead(t, db, "user-1", parent.ID)
	assertApproxTime(t, row.ReadAt, reply2.CreatedAt, "read_at（缺省最新回复）")

	info, err := svc.GetUnreadCount("user-1", "ch-d1e")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if len(info.ThreadUnreads) != 0 {
		t.Errorf("thread unread after default mark-read = %+v, want none", info.ThreadUnreads)
	}
}
