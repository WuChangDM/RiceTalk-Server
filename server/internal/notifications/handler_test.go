package notifications

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// 1.9.4 task-118：通知中心 HTTP 契约用例。
//
// 覆盖：鉴权（未登录 401）、列表 + 未读数、unreadOnly 过滤、游标分页（不重不漏）、
// 单条已读幂等（重复标记不覆盖首次已读时间）、read-all 幂等、跨用户标记 403、
// 不存在 404。全部走真实 SQLite（生产 AutoMigrate 清单）+ 真实 gin + 真实
// AuthRequired，不 mock service。

func init() {
	_ = idgen.Init(1, 1)
}

type notifListItem struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	RefType   string     `json:"refType"`
	RefID     string     `json:"refId"`
	ReadAt    *time.Time `json:"readAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

type notifListData struct {
	Notifications []notifListItem `json:"notifications"`
	UnreadCount   int64           `json:"unreadCount"`
	NextCursor    string          `json:"nextCursor"`
}

type notifEnvelope struct {
	Code string        `json:"code"`
	Data notifListData `json:"data"`
}

type notifActionEnvelope struct {
	Code string `json:"code"`
	Data struct {
		Message string `json:"message"`
		Updated int64  `json:"updated"`
	} `json:"data"`
}

type notifTestEnv struct {
	db     *gorm.DB
	router *gin.Engine
	cfg    *config.Config
	alice  *model.User
	bob    *model.User
}

func setupNotificationTestRouter(t *testing.T) *notifTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-for-notifications-32b!!"
	cfg.EncryptionKey = "test-encryption-key-for-notif-32by"

	alice := createNotifTestUser(t, db, "notif-alice")
	bob := createNotifTestUser(t, db, "notif-bob")

	handler := NewHandler(db, cfg)
	router := gin.New()
	// 与 internal/server/routes.go 的真实装配一致：同时挂 /api/v1 与 legacy /api。
	handler.RegisterRoutes(router.Group("/api/v1"))
	handler.RegisterRoutes(router.Group("/api"))

	return &notifTestEnv{db: db, router: router, cfg: cfg, alice: alice, bob: bob}
}

func createNotifTestUser(t *testing.T, db *gorm.DB, name string) *model.User {
	t.Helper()
	u := &model.User{
		ID:           idgen.NextString(),
		Username:     name,
		Email:        name + "@example.com",
		PasswordHash: "x",
		Role:         middleware.RoleMember,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func (e *notifTestEnv) tokenOf(t *testing.T, u *model.User) string {
	t.Helper()
	token, _, err := middleware.GenerateTokenPair(u.ID, u.Username, u.Email, u.Role, u.TokenVersion, "", "", e.cfg)
	if err != nil {
		t.Fatalf("generate token for %s: %v", u.Username, err)
	}
	return token
}

func (e *notifTestEnv) do(t *testing.T, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// createNotif 造一条通知；createdAt 显式给值，保证分页顺序不依赖时钟精度。
func (e *notifTestEnv) createNotif(t *testing.T, userID, title string, read bool, createdAt time.Time) *model.Notification {
	t.Helper()
	n := &model.Notification{
		ID:        idgen.GenerateID(idgen.PrefixNotification),
		UserID:    userID,
		Type:      TypeMention,
		Title:     title,
		Body:      "body:" + title,
		RefType:   RefTypeMessage,
		RefID:     "msg_" + title,
		CreatedAt: createdAt,
	}
	if read {
		readAt := createdAt.Add(time.Minute)
		n.ReadAt = &readAt
	}
	if err := e.db.Create(n).Error; err != nil {
		t.Fatalf("create notification %q: %v", title, err)
	}
	return n
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) notifListData {
	t.Helper()
	var env notifEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list response: %v (%s)", err, w.Body.String())
	}
	if env.Code != "OK" {
		t.Fatalf("expected OK code, got %q (%s)", env.Code, w.Body.String())
	}
	return env.Data
}

// TestNotificationRoutesRequireAuth：三条路由都必须先过登录校验（防「通知裸奔」）。
func TestNotificationRoutesRequireAuth(t *testing.T) {
	env := setupNotificationTestRouter(t)

	for _, p := range []struct{ method, path string }{
		{"GET", "/api/v1/notifications"},
		{"POST", "/api/v1/notifications/read-all"},
		{"POST", "/api/v1/notifications/ntf_x/read"},
	} {
		if w := env.do(t, p.method, p.path, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("未登录 %s %s 应 401，实际 %d: %s", p.method, p.path, w.Code, w.Body.String())
		}
	}
}

// TestListNotificationsWithUnreadCount：列表按时间倒序、只含本人通知、未读数正确。
func TestListNotificationsWithUnreadCount(t *testing.T) {
	env := setupNotificationTestRouter(t)
	token := env.tokenOf(t, env.alice)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	env.createNotif(t, env.alice.ID, "oldest", false, base)
	env.createNotif(t, env.alice.ID, "middle", true, base.Add(time.Minute))
	env.createNotif(t, env.alice.ID, "newest", false, base.Add(2*time.Minute))
	// 别人的通知绝不能出现在 alice 的列表里。
	env.createNotif(t, env.bob.ID, "bobs", false, base.Add(3*time.Minute))

	w := env.do(t, "GET", "/api/v1/notifications", token)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	data := decodeList(t, w)
	if len(data.Notifications) != 3 {
		t.Fatalf("alice 应有 3 条通知（不得含他人），实际 %d: %+v", len(data.Notifications), data.Notifications)
	}
	if data.UnreadCount != 2 {
		t.Fatalf("未读数应为 2，实际 %d", data.UnreadCount)
	}
	if data.Notifications[0].Title != "newest" || data.Notifications[2].Title != "oldest" {
		t.Fatalf("通知应按 created_at 倒序，实际顺序 %q..%q", data.Notifications[0].Title, data.Notifications[2].Title)
	}
	if data.Notifications[0].ReadAt != nil {
		t.Fatalf("未读通知 readAt 应为 null，实际 %v", data.Notifications[0].ReadAt)
	}
	if data.NextCursor != "" {
		t.Fatalf("不足一页时 nextCursor 应为空，实际 %q", data.NextCursor)
	}
}

// TestListUnreadOnly：unreadOnly=1 只返回未读，但未读数仍是全量未读数。
func TestListUnreadOnly(t *testing.T) {
	env := setupNotificationTestRouter(t)
	token := env.tokenOf(t, env.alice)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	env.createNotif(t, env.alice.ID, "unread-1", false, base)
	env.createNotif(t, env.alice.ID, "unread-2", false, base.Add(time.Minute))
	env.createNotif(t, env.alice.ID, "already-read", true, base.Add(2*time.Minute))

	w := env.do(t, "GET", "/api/v1/notifications?unreadOnly=1", token)
	if w.Code != http.StatusOK {
		t.Fatalf("unreadOnly 列表应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	data := decodeList(t, w)
	if len(data.Notifications) != 2 {
		t.Fatalf("unreadOnly 应只返回 2 条未读，实际 %d", len(data.Notifications))
	}
	for _, n := range data.Notifications {
		if n.ReadAt != nil {
			t.Fatalf("unreadOnly 结果不应含已读通知：%+v", n)
		}
	}
	if data.UnreadCount != 2 {
		t.Fatalf("未读数应为 2，实际 %d", data.UnreadCount)
	}
}

// TestListCursorPagination：limit=2 三页翻完，不重不漏，末页 nextCursor 为空。
func TestListCursorPagination(t *testing.T) {
	env := setupNotificationTestRouter(t)
	token := env.tokenOf(t, env.alice)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	want := []string{"n5", "n4", "n3", "n2", "n1"}
	for i, title := range want {
		// want 首元素最新：created_at = base + (len-1-i) 分钟。
		env.createNotif(t, env.alice.ID, title, false, base.Add(time.Duration(len(want)-1-i)*time.Minute))
	}

	var got []string
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/api/v1/notifications?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w := env.do(t, "GET", path, token)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 页应 200，实际 %d: %s", page+1, w.Code, w.Body.String())
		}
		data := decodeList(t, w)
		for _, n := range data.Notifications {
			got = append(got, n.Title)
		}
		cursor = data.NextCursor
		if cursor == "" {
			break
		}
	}

	if len(got) != len(want) {
		t.Fatalf("分页应取回 %d 条，实际 %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条应为 %q，实际 %q（全部：%v）", i+1, want[i], got[i], got)
		}
	}

	// 非法 cursor 必须 400，而不是静默返回首页（否则客户端可能死循环）。
	if w := env.do(t, "GET", "/api/v1/notifications?cursor=ntf_missing", token); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 cursor 应 400，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestMarkReadIdempotent：重复标记已读不改首次已读时间。
func TestMarkReadIdempotent(t *testing.T) {
	env := setupNotificationTestRouter(t)
	token := env.tokenOf(t, env.alice)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	n := env.createNotif(t, env.alice.ID, "to-read", false, base)

	w := env.do(t, "POST", "/api/v1/notifications/"+n.ID+"/read", token)
	if w.Code != http.StatusOK {
		t.Fatalf("标记已读应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var first model.Notification
	if err := env.db.Where("id = ?", n.ID).First(&first).Error; err != nil {
		t.Fatalf("读取已读记录: %v", err)
	}
	if first.ReadAt == nil {
		t.Fatalf("标记后 read_at 应落库，实际 nil")
	}
	firstReadAt := *first.ReadAt

	// 第二次（幂等）：仍 200，且 read_at 不被覆盖。
	if w := env.do(t, "POST", "/api/v1/notifications/"+n.ID+"/read", token); w.Code != http.StatusOK {
		t.Fatalf("重复标记已读应仍 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var second model.Notification
	if err := env.db.Where("id = ?", n.ID).First(&second).Error; err != nil {
		t.Fatalf("二次读取已读记录: %v", err)
	}
	if second.ReadAt == nil || !second.ReadAt.Equal(firstReadAt) {
		t.Fatalf("重复标记已读不应覆盖首次已读时间：first=%v second=%v", firstReadAt, second.ReadAt)
	}

	// 未读数归零。
	var unread int64
	env.db.Model(&model.Notification{}).Where("user_id = ? AND read_at IS NULL", env.alice.ID).Count(&unread)
	if unread != 0 {
		t.Fatalf("已读后未读数应为 0，实际 %d", unread)
	}
}

// TestMarkReadCrossUserForbidden：不能标记别人的通知（403），也不能泄露其存在。
func TestMarkReadCrossUserForbidden(t *testing.T) {
	env := setupNotificationTestRouter(t)
	bobToken := env.tokenOf(t, env.bob)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	bobsNotif := env.createNotif(t, env.alice.ID, "alices-notif", false, base)

	if w := env.do(t, "POST", "/api/v1/notifications/"+bobsNotif.ID+"/read", bobToken); w.Code != http.StatusForbidden {
		t.Fatalf("跨用户标记已读应 403，实际 %d: %s", w.Code, w.Body.String())
	}
	var still model.Notification
	if err := env.db.Where("id = ?", bobsNotif.ID).First(&still).Error; err != nil {
		t.Fatalf("读取被越权的通知: %v", err)
	}
	if still.ReadAt != nil {
		t.Fatalf("越权请求不得修改 read_at，实际 %v", still.ReadAt)
	}
}

// TestMarkReadNotFound：不存在的通知 -> 404。
func TestMarkReadNotFound(t *testing.T) {
	env := setupNotificationTestRouter(t)
	token := env.tokenOf(t, env.alice)

	w := env.do(t, "POST", "/api/v1/notifications/ntf_does_not_exist/read", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的通知应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestMarkAllReadIdempotent：read-all 只影响本人未读，重复调用 updated=0。
func TestMarkAllReadIdempotent(t *testing.T) {
	env := setupNotificationTestRouter(t)
	aliceToken := env.tokenOf(t, env.alice)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	env.createNotif(t, env.alice.ID, "a1", false, base)
	env.createNotif(t, env.alice.ID, "a2", false, base.Add(time.Minute))
	env.createNotif(t, env.alice.ID, "a3-read", true, base.Add(2*time.Minute))
	bobsNotif := env.createNotif(t, env.bob.ID, "b1", false, base.Add(3*time.Minute))

	w := env.do(t, "POST", "/api/v1/notifications/read-all", aliceToken)
	if w.Code != http.StatusOK {
		t.Fatalf("read-all 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var env1 notifActionEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env1); err != nil {
		t.Fatalf("解析 read-all 响应: %v (%s)", err, w.Body.String())
	}
	if env1.Data.Updated != 2 {
		t.Fatalf("read-all 应影响 2 条未读，实际 %d", env1.Data.Updated)
	}

	// 幂等：第二次没有未读，updated=0。
	w = env.do(t, "POST", "/api/v1/notifications/read-all", aliceToken)
	env1 = notifActionEnvelope{}
	if err := json.Unmarshal(w.Body.Bytes(), &env1); err != nil {
		t.Fatalf("解析第二次 read-all 响应: %v (%s)", err, w.Body.String())
	}
	if env1.Data.Updated != 0 {
		t.Fatalf("重复 read-all 应影响 0 条，实际 %d", env1.Data.Updated)
	}

	// bob 的通知不受影响。
	var bobs model.Notification
	if err := env.db.Where("id = ?", bobsNotif.ID).First(&bobs).Error; err != nil {
		t.Fatalf("读取 bob 通知: %v", err)
	}
	if bobs.ReadAt != nil {
		t.Fatalf("read-all 不得影响其他用户的通知，bob 的 read_at=%v", bobs.ReadAt)
	}
}

// TestNotifyMentionForMessage：@提及通知的落库内容（类型/引用/摘要/Space 反查）。
func TestNotifyMentionForMessage(t *testing.T) {
	env := setupNotificationTestRouter(t)
	svc := NewService(env.db)

	channel := &model.Channel{ID: idgen.NextString(), SpaceID: "space-notif", Name: "general", Type: "TEXT", CreatedBy: env.alice.ID}
	if err := env.db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	msg := &model.Message{
		ID:                idgen.NextString(),
		ChannelID:         channel.ID,
		UserID:            env.alice.ID,
		AuthorUsername:    "notif-alice",
		AuthorDisplayName: "Alice",
		Content:           "  hello @bob  ",
	}
	if err := svc.NotifyMentionForMessage(msg, env.bob.ID, "Alice"); err != nil {
		t.Fatalf("落库 mention 通知失败: %v", err)
	}

	var n model.Notification
	if err := env.db.Where("user_id = ?", env.bob.ID).First(&n).Error; err != nil {
		t.Fatalf("查询 mention 通知: %v", err)
	}
	if n.Type != TypeMention || n.RefType != RefTypeMessage || n.RefID != msg.ID {
		t.Fatalf("mention 通知字段不符：type=%q refType=%q refID=%q", n.Type, n.RefType, n.RefID)
	}
	if n.SpaceID != "space-notif" {
		t.Fatalf("space_id 应由频道反查得到 space-notif，实际 %q", n.SpaceID)
	}
	if n.Body != "hello @bob" {
		t.Fatalf("正文应去首尾空白后为 %q，实际 %q", "hello @bob", n.Body)
	}
	if n.Title != "Alice 提到了你" {
		t.Fatalf("标题应为 %q，实际 %q", "Alice 提到了你", n.Title)
	}
	if n.ReadAt != nil {
		t.Fatalf("新通知应为未读（read_at=null）")
	}
}

// TestPreviewTextTruncatesByRune：摘要按 rune 截断，不切坏多字节字符。
func TestPreviewTextTruncatesByRune(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "字"
	}
	got := previewText(long)
	runes := []rune(got)
	if len(runes) != previewMaxRunes+1 { // 80 个字符 + 省略号
		t.Fatalf("摘要应为 %d 个 rune（含省略号），实际 %d", previewMaxRunes+1, len(runes))
	}
	if runes[len(runes)-1] != '…' {
		t.Fatalf("超长摘要应以省略号结尾，实际 %q", string(runes[len(runes)-1]))
	}
}
