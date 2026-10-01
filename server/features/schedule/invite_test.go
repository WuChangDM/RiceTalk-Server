package schedule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// setupInviteFixture 构建 A8 测试基线：
//   - 空间 + 创建者 user-1（默认请求上下文身份，MEMBER）
//   - inv-1 / inv-2：本空间成员（可被邀请）
//   - member-2：本空间成员（非受邀，用于可见性反例）
//   - outsider-user：已注册但非本空间成员（成员校验反例）
//   - owner-1（OWNER）/ admin-1（ADMIN）：有 users 行、无本空间 membership
//     （admin_only scope 口径依赖全局角色，scope=all 口径依赖 membership）
func setupInviteFixture(t *testing.T) (*Handler, *ginTestEnv, *gorm.DB, *model.Space) {
	t.Helper()
	h, _, db, space := setupTestHandler()
	users := []model.User{
		{ID: "user-1", Username: "user-1", DisplayName: "创建者", Email: "user-1@test.local", Role: "MEMBER", IsActive: true},
		{ID: "inv-1", Username: "inv-1", DisplayName: "受邀人甲", Email: "inv-1@test.local", Role: "MEMBER", IsActive: true},
		{ID: "inv-2", Username: "inv-2", DisplayName: "受邀人乙", Email: "inv-2@test.local", Role: "MEMBER", IsActive: true},
		{ID: "member-2", Username: "member-2", DisplayName: "路人丙", Email: "member-2@test.local", Role: "MEMBER", IsActive: true},
		{ID: "outsider-user", Username: "outsider-user", Email: "outsider@test.local", Role: "MEMBER", IsActive: true},
		{ID: "owner-1", Username: "owner-1", Email: "owner-1@test.local", Role: "OWNER", IsActive: true},
		{ID: "admin-1", Username: "admin-1", Email: "admin-1@test.local", Role: "ADMIN", IsActive: true},
	}
	for _, u := range users {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("seed user %s: %v", u.ID, err)
		}
	}
	for _, uid := range []string{"user-1", "inv-1", "inv-2", "member-2"} {
		m := model.Membership{ID: idgen.NextString(), UserID: uid, SpaceID: space.ID}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed membership %s: %v", uid, err)
		}
	}
	// 默认路由器：创建者（user-1, MEMBER）视角，注册事件 CRUD + 邀请两端点
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	sc := api.Group("/schedule")
	inject := func(hfn func(c *gin.Context)) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "user-1")
			c.Set("role", "MEMBER")
			c.Set("space_id", space.ID)
			hfn(c)
		}
	}
	{
		sc.POST("/events", inject(h.CreateEvent))
		sc.PUT("/events/:id", inject(h.UpdateEvent))
		sc.DELETE("/events/:id", inject(h.DeleteEvent))
		sc.GET("/events/:id/invites", inject(h.GetInvites))
		sc.PUT("/events/:id/invite", inject(h.RespondInvite))
	}
	return h, &ginTestEnv{r: r}, db, space
}

// ginTestEnv 包装测试路由器，提供带上下文的请求辅助。
type ginTestEnv struct {
	r interface{ ServeHTTP(w http.ResponseWriter, req *http.Request) }
}

// do 以 user-1 身份发送 JSON 请求（创建者视角）。
func (e *ginTestEnv) do(t *testing.T, method, path string, body interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	return doAs(t, e.r, method, path, body, "user-1")
}

// doAs 以指定 userId/role 发送 JSON 请求。
func doAs(t *testing.T, r interface{ ServeHTTP(w http.ResponseWriter, req *http.Request) }, method, path string, body interface{}, userID string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	var reader *bytes.Buffer
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewBuffer(raw)
	} else {
		reader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", userID)
	// 直接复用 setupTestHandler 的固定上下文注入（user_id 固定 user-1），
	// 身份切换通过 makeInviteRouter 的注入函数完成。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func respCode(t *testing.T, resp map[string]interface{}) string {
	t.Helper()
	code, _ := resp["code"].(string)
	return code
}

func mustOK(t *testing.T, w *httptest.ResponseRecorder, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object, got %s", w.Body.String())
	}
	return data
}

// seedEvent 直接落库一个事件（绕过 HTTP），用于可见性/应答类测试。
func seedEvent(t *testing.T, db *gorm.DB, spaceID, title, scope, creator string, start time.Time) model.ScheduleEvent {
	t.Helper()
	event := model.ScheduleEvent{
		ID:        idgen.GenerateID(idgen.PrefixEvent),
		SpaceID:   spaceID,
		Title:     title,
		EventDate: time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC),
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		Scope:     scope,
		CreatedBy: creator,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return event
}

func seedInvite(t *testing.T, db *gorm.DB, eventID, userID, status string) model.ScheduleEventInvite {
	t.Helper()
	inv := model.ScheduleEventInvite{
		ID:      idgen.GenerateID(idgen.PrefixInvite),
		EventID: eventID,
		UserID:  userID,
		Status:  status,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed invite %s: %v", userID, err)
	}
	return inv
}

// ─────────────────────────────────────────────────────────────
// A8-S1: TestCreateEventWithInvites
// ─────────────────────────────────────────────────────────────

// TestCreateEventWithInvites 创建带受邀人的事件：建 pending 邀请、快照写入、
// 各类校验（非成员/超50/含创建者）失败 400、去重、无 inviteeIds 完全兼容。
func TestCreateEventWithInvites(t *testing.T) {
	_, env, db, _ := setupInviteFixture(t)
	start := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	path := "/api/v1/schedule/events"

	t.Run("正常创建：建 pending 邀请且 responded_at 为空", func(t *testing.T) {
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":     "评审会",
			"startTime": start.Format(time.RFC3339),
			"inviteeIds": []string{"inv-1", "inv-2"},
		})
		data := mustOK(t, w, resp)
		eventID, _ := data["id"].(string)

		// 快照 inviteeIds 返回数组
		snapshot, ok := data["inviteeIds"].([]interface{})
		if !ok || len(snapshot) != 2 {
			t.Fatalf("expected inviteeIds array of 2, got %v", data["inviteeIds"])
		}

		var invites []model.ScheduleEventInvite
		db.Where("event_id = ?", eventID).Order("user_id").Find(&invites)
		if len(invites) != 2 {
			t.Fatalf("expected 2 invite rows, got %d", len(invites))
		}
		for _, inv := range invites {
			if inv.Status != InviteStatusPending {
				t.Errorf("expected pending status for %s, got %s", inv.UserID, inv.Status)
			}
			if inv.RespondedAt != nil {
				t.Errorf("expected nil responded_at for %s", inv.UserID)
			}
		}
	})

	t.Run("去重：重复 id 只建一条邀请", func(t *testing.T) {
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":      "去重会",
			"startTime":  start.Format(time.RFC3339),
			"inviteeIds": []string{"inv-1", "inv-1", "inv-2", "inv-1"},
		})
		data := mustOK(t, w, resp)
		eventID, _ := data["id"].(string)
		var count int64
		db.Model(&model.ScheduleEventInvite{}).Where("event_id = ?", eventID).Count(&count)
		if count != 2 {
			t.Errorf("expected 2 invite rows after dedup, got %d", count)
		}
	})

	t.Run("非空间成员 400 SCHEDULE_INVITE_INVALID", func(t *testing.T) {
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":      "混入外人",
			"startTime":  start.Format(time.RFC3339),
			"inviteeIds": []string{"inv-1", "outsider-user"},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
		}
		if got := respCode(t, resp); got != string(errors.SCHEDULE_INVITE_INVALID) {
			t.Errorf("expected code %s, got %s", errors.SCHEDULE_INVITE_INVALID, got)
		}
	})

	t.Run("超过 50 人 400", func(t *testing.T) {
		ids := make([]string, 0, 51)
		for i := 0; i < 51; i++ {
			ids = append(ids, fmt.Sprintf("bulk-user-%02d", i))
		}
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":      "超编会",
			"startTime":  start.Format(time.RFC3339),
			"inviteeIds": ids,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for 51 invitees, got %d", w.Code)
		}
		if got := respCode(t, resp); got != string(errors.SCHEDULE_INVITE_INVALID) {
			t.Errorf("expected code %s, got %s", errors.SCHEDULE_INVITE_INVALID, got)
		}
	})

	t.Run("包含创建者 400", func(t *testing.T) {
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":      "自邀会",
			"startTime":  start.Format(time.RFC3339),
			"inviteeIds": []string{"inv-1", "user-1"},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for self invite, got %d", w.Code)
		}
		if got := respCode(t, resp); got != string(errors.SCHEDULE_INVITE_INVALID) {
			t.Errorf("expected code %s, got %s", errors.SCHEDULE_INVITE_INVALID, got)
		}
	})

	t.Run("无 inviteeIds 完全兼容：不建邀请且快照为空数组", func(t *testing.T) {
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":     "普通事件",
			"startTime": start.Format(time.RFC3339),
		})
		data := mustOK(t, w, resp)
		eventID, _ := data["id"].(string)
		snapshot, ok := data["inviteeIds"].([]interface{})
		if !ok || len(snapshot) != 0 {
			t.Errorf("expected empty inviteeIds array, got %v (type %T)", data["inviteeIds"], data["inviteeIds"])
		}
		var count int64
		db.Model(&model.ScheduleEventInvite{}).Where("event_id = ?", eventID).Count(&count)
		if count != 0 {
			t.Errorf("expected 0 invite rows for plain event, got %d", count)
		}
	})

	t.Run("空数组等价无邀请", func(t *testing.T) {
		w, resp := env.do(t, "POST", path, map[string]interface{}{
			"title":      "空数组事件",
			"startTime":  start.Format(time.RFC3339),
			"inviteeIds": []string{},
		})
		data := mustOK(t, w, resp)
		eventID, _ := data["id"].(string)
		var count int64
		db.Model(&model.ScheduleEventInvite{}).Where("event_id = ?", eventID).Count(&count)
		if count != 0 {
			t.Errorf("expected 0 invite rows, got %d", count)
		}
	})
}

// ─────────────────────────────────────────────────────────────
// A8-S2: TestRespondInvite
// ─────────────────────────────────────────────────────────────

// TestRespondInvite 应答邀请：仅本人（非受邀 404）、幂等、非法 status 400、responded_at 写入。
func TestRespondInvite(t *testing.T) {
	h, _, db, space := setupInviteFixture(t)

	// 身份切换路由：覆盖 PUT /events/:id/invite 与 GET /events/:id/invites
	inviteRouter := func(userID string) http.Handler {
		return makeInviteRouter(h, userID)
	}

	t.Run("本人应答 accepted：状态与 responded_at 写入", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "应答会", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		r := inviteRouter("inv-1")
		w, resp := doAs(t, r, "PUT", "/api/v1/schedule/events/"+event.ID+"/invite",
			map[string]interface{}{"status": "accepted"}, "inv-1")
		data := mustOK(t, w, resp)
		if data["status"] != "accepted" || data["userId"] != "inv-1" || data["eventId"] != event.ID {
			t.Errorf("unexpected respond payload: %v", data)
		}
		if data["respondedAt"] == nil {
			t.Error("expected respondedAt to be written")
		}

		var inv model.ScheduleEventInvite
		db.Where("event_id = ? AND user_id = ?", event.ID, "inv-1").First(&inv)
		if inv.Status != InviteStatusAccepted {
			t.Errorf("expected accepted in DB, got %s", inv.Status)
		}
		if inv.RespondedAt == nil {
			t.Error("expected responded_at in DB")
		}
	})

	t.Run("非受邀人 404（不泄露存在性）", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "别人家的会", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		// member-2 是空间成员但非受邀；事件存在但对他不可见
		r := inviteRouter("member-2")
		w, resp := doAs(t, r, "PUT", "/api/v1/schedule/events/"+event.ID+"/invite",
			map[string]interface{}{"status": "accepted"}, "member-2")
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for non-invitee, got %d body=%s", w.Code, w.Body.String())
		}
		if got := respCode(t, resp); got != string(errors.SCHEDULE_NOT_FOUND) {
			t.Errorf("expected code %s, got %s", errors.SCHEDULE_NOT_FOUND, got)
		}
	})

	t.Run("幂等：重复同 status 均 200", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "幂等会", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		r := inviteRouter("inv-1")
		for i := 0; i < 3; i++ {
			w, _ := doAs(t, r, "PUT", "/api/v1/schedule/events/"+event.ID+"/invite",
				map[string]interface{}{"status": "declined"}, "inv-1")
			if w.Code != http.StatusOK {
				t.Fatalf("idempotent respond #%d expected 200, got %d", i+1, w.Code)
			}
		}
		var inv model.ScheduleEventInvite
		db.Where("event_id = ? AND user_id = ?", event.ID, "inv-1").First(&inv)
		if inv.Status != InviteStatusDeclined {
			t.Errorf("expected declined, got %s", inv.Status)
		}
		// 仍只有一条邀请行（未重复建行）
		var count int64
		db.Model(&model.ScheduleEventInvite{}).Where("event_id = ? AND user_id = ?", event.ID, "inv-1").Count(&count)
		if count != 1 {
			t.Errorf("expected still 1 invite row, got %d", count)
		}
	})

	t.Run("非法 status 400 SCHEDULE_INVITE_INVALID", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "状态会", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		r := inviteRouter("inv-1")
		for _, status := range []string{"maybe", "PENDING", ""} {
			w, resp := doAs(t, r, "PUT", "/api/v1/schedule/events/"+event.ID+"/invite",
				map[string]interface{}{"status": status}, "inv-1")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status %q: expected 400, got %d", status, w.Code)
			}
			if got := respCode(t, resp); got != string(errors.SCHEDULE_INVITE_INVALID) {
				t.Errorf("status %q: expected code %s, got %s", status, errors.SCHEDULE_INVITE_INVALID, got)
			}
		}
		// DB 状态未被非法请求改动
		var inv model.ScheduleEventInvite
		db.Where("event_id = ? AND user_id = ?", event.ID, "inv-1").First(&inv)
		if inv.Status != InviteStatusPending || inv.RespondedAt != nil {
			t.Errorf("invalid status must not modify invite, got status=%s respondedAt=%v", inv.Status, inv.RespondedAt)
		}
	})

	t.Run("事件不存在 404", func(t *testing.T) {
		r := inviteRouter("inv-1")
		w, _ := doAs(t, r, "PUT", "/api/v1/schedule/events/event_nope/invite",
			map[string]interface{}{"status": "accepted"}, "inv-1")
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for missing event, got %d", w.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────
// A8-S1: TestInviteVisibility
// ─────────────────────────────────────────────────────────────

// TestInviteVisibility GET invites 可见性：非受邀非创建者 404；受邀人/创建者 200；
// admin_only 事件维持 N4 口径（全局 admin 可见）；scope=all 事件 admin 不豁免。
func TestInviteVisibility(t *testing.T) {
	h, env, db, space := setupInviteFixture(t)

	t.Run("创建者 200", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "创建者视图", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		w, resp := env.do(t, "GET", "/api/v1/schedule/events/"+event.ID+"/invites", nil)
		data := mustOK(t, w, resp)
		invites, _ := data["invites"].([]interface{})
		if len(invites) != 1 {
			t.Fatalf("expected 1 invite, got %v", data["invites"])
		}
		first, _ := invites[0].(map[string]interface{})
		if first["userId"] != "inv-1" || first["status"] != InviteStatusPending {
			t.Errorf("unexpected invite entry: %v", first)
		}
		if first["displayName"] != "受邀人甲" {
			t.Errorf("expected displayName 受邀人甲, got %v", first["displayName"])
		}
		if first["respondedAt"] != nil {
			t.Errorf("expected nil respondedAt, got %v", first["respondedAt"])
		}
	})

	t.Run("受邀人 200", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "受邀人视图", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-2", InviteStatusPending)

		r := makeInviteRouter(h, "inv-2")
		w, resp := doAs(t, r, "GET", "/api/v1/schedule/events/"+event.ID+"/invites", nil, "inv-2")
		if w.Code != http.StatusOK {
			t.Fatalf("invitee expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		data, _ := resp["data"].(map[string]interface{})
		if data == nil || data["invites"] == nil {
			t.Fatalf("expected invites payload, got %s", w.Body.String())
		}
	})

	t.Run("非受邀非创建者 404（不泄露存在性）", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "隐身会", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		r := makeInviteRouter(h, "member-2")
		w, resp := doAs(t, r, "GET", "/api/v1/schedule/events/"+event.ID+"/invites", nil, "member-2")
		if w.Code != http.StatusNotFound {
			t.Fatalf("outsider expected 404, got %d", w.Code)
		}
		if got := respCode(t, resp); got != string(errors.SCHEDULE_NOT_FOUND) {
			t.Errorf("expected code %s, got %s", errors.SCHEDULE_NOT_FOUND, got)
		}
	})

	t.Run("admin_only 事件：全局 admin 非受邀仍可见（N4 口径）", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "受限会", "admin_only", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		r2 := makeInviteRouterWithRole(h, "admin-viewer", "ADMIN")
		w, resp := doAs(t, r2, "GET", "/api/v1/schedule/events/"+event.ID+"/invites", nil, "admin-viewer")
		if w.Code != http.StatusOK {
			t.Fatalf("admin expected 200 on admin_only event invites, got %d body=%s", w.Code, w.Body.String())
		}
		data, _ := resp["data"].(map[string]interface{})
		if data == nil || data["invites"] == nil {
			t.Fatalf("expected invites payload, got %s", w.Body.String())
		}
	})

	t.Run("scope=all 事件：admin 非受邀非创建者不可见（不豁免）", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "普通会", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)

		r := makeInviteRouterWithRole(h, "admin-viewer", "ADMIN")
		w, _ := doAs(t, r, "GET", "/api/v1/schedule/events/"+event.ID+"/invites", nil, "admin-viewer")
		if w.Code != http.StatusNotFound {
			t.Errorf("scope=all invites must not be visible to unrelated admin, got %d", w.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────
// A8-S2: TestReminderRecipientsWithInvites
// ─────────────────────────────────────────────────────────────

// TestReminderRecipientsWithInvites 提醒收件人 = scope 口径 ∪ 受邀人(status != declined)：
// declined 不提醒、accepted/pending 提醒、scope 原有收件人不变。
func TestReminderRecipientsWithInvites(t *testing.T) {
	h, _, db, space := setupInviteFixture(t)

	// admin_only 事件：scope 口径只有 owner-1/admin-1；受邀人见下
	event := model.ScheduleEvent{ID: idgen.GenerateID(idgen.PrefixEvent), SpaceID: space.ID, Title: "受限邀约", Scope: "admin_only"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}
	seedInvite(t, db, event.ID, "inv-1", InviteStatusAccepted)  // 受邀已接受 → 提醒
	seedInvite(t, db, event.ID, "inv-2", InviteStatusPending)   // 受邀待答 → 提醒
	seedInvite(t, db, event.ID, "member-2", InviteStatusDeclined) // 已拒绝 → 不提醒

	recipients, err := h.reminderRecipients(event)
	if err != nil {
		t.Fatalf("reminderRecipients: %v", err)
	}
	got := map[string]bool{}
	for _, id := range recipients {
		got[id] = true
	}
	// scope 口径（admin_only → owner-1/admin-1）不变
	if !got["owner-1"] || !got["admin-1"] {
		t.Errorf("scope recipients owner-1/admin-1 must remain, got %v", recipients)
	}
	// 受邀人 accepted/pending 收到提醒
	if !got["inv-1"] || !got["inv-2"] {
		t.Errorf("accepted/pending invitees must receive reminder, got %v", recipients)
	}
	// declined 不提醒
	if got["member-2"] {
		t.Errorf("declined invitee member-2 must NOT receive reminder, got %v", recipients)
	}
	// 无关成员不提醒
	if got["outsider-user"] {
		t.Errorf("non-invitee outsider must not receive admin_only reminder, got %v", recipients)
	}

	// scope=all 事件：原有空间成员收件人完全不变（declined 的成员仍走 scope 口径收到）
	plainEvent := model.ScheduleEvent{ID: idgen.GenerateID(idgen.PrefixEvent), SpaceID: space.ID, Title: "全员会", Scope: "all"}
	if err := db.Create(&plainEvent).Error; err != nil {
		t.Fatalf("create plain event: %v", err)
	}
	seedInvite(t, db, plainEvent.ID, "inv-1", InviteStatusDeclined) // declined，但 inv-1 是空间成员

	plainRecipients, err := h.reminderRecipients(plainEvent)
	if err != nil {
		t.Fatalf("reminderRecipients(plain): %v", err)
	}
	gotPlain := map[string]bool{}
	for _, id := range plainRecipients {
		gotPlain[id] = true
	}
	for _, want := range []string{"user-1", "inv-1", "inv-2", "member-2"} {
		if !gotPlain[want] {
			t.Errorf("scope=all recipients must include space member %s (unchanged), got %v", want, plainRecipients)
		}
	}
	if gotPlain["outsider-user"] {
		t.Errorf("outsider must not receive scope=all reminder, got %v", plainRecipients)
	}
}

// ─────────────────────────────────────────────────────────────
// A8-S2: TestInviteEventsRouting
// ─────────────────────────────────────────────────────────────

// TestInviteEventsRouting 两条 WS 事件的收件人与 payload：
// schedule_invite_updated → 全部受邀人；schedule_invite_responded → 创建者。
func TestInviteEventsRouting(t *testing.T) {
	_, _, db, space := setupInviteFixture(t)

	event := seedEvent(t, db, space.ID, "路由会", "all", "user-1", time.Now().Add(24*time.Hour))
	invites := []model.ScheduleEventInvite{
		seedInvite(t, db, event.ID, "inv-1", InviteStatusAccepted),
		seedInvite(t, db, event.ID, "inv-2", InviteStatusPending),
		seedInvite(t, db, event.ID, "member-2", InviteStatusDeclined),
	}

	t.Run("updated 收件人=全部受邀人（含 declined）", func(t *testing.T) {
		got := inviteUpdatedRecipients(invites)
		set := map[string]bool{}
		for _, id := range got {
			set[id] = true
		}
		for _, want := range []string{"inv-1", "inv-2", "member-2"} {
			if !set[want] {
				t.Errorf("expected invitee %s in updated recipients, got %v", want, got)
			}
		}
		if len(got) != 3 {
			t.Errorf("expected exactly 3 recipients, got %v", got)
		}
		if got2 := inviteUpdatedRecipients(nil); len(got2) != 0 {
			t.Errorf("empty invites should yield empty recipients, got %v", got2)
		}
	})

	t.Run("updated payload 结构", func(t *testing.T) {
		raw := buildInviteUpdatedMessage(event, invites)
		var msg struct {
			Type    string `json:"type"`
			Payload struct {
				EventID  string `json:"eventId"`
				Title    string `json:"title"`
				StartsAt string `json:"startsAt"`
				Invites  []struct {
					UserID string `json:"userId"`
					Status string `json:"status"`
				} `json:"invites"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal: %v raw=%s", err, raw)
		}
		if msg.Type != "schedule_invite_updated" {
			t.Errorf("expected type schedule_invite_updated, got %s", msg.Type)
		}
		if msg.Payload.EventID != event.ID || msg.Payload.Title != event.Title {
			t.Errorf("unexpected payload head: %+v", msg.Payload)
		}
		if msg.Payload.StartsAt == "" {
			t.Error("expected startsAt in payload")
		}
		if len(msg.Payload.Invites) != 3 {
			t.Fatalf("expected 3 invite entries, got %d", len(msg.Payload.Invites))
		}
		if msg.Payload.Invites[0].UserID != "inv-1" || msg.Payload.Invites[0].Status != InviteStatusAccepted {
			t.Errorf("unexpected invite entry: %+v", msg.Payload.Invites[0])
		}
	})

	t.Run("responded 收件人=创建者", func(t *testing.T) {
		got := inviteRespondedRecipients(event)
		if len(got) != 1 || got[0] != event.CreatedBy {
			t.Errorf("expected recipients [creator=%s], got %v", event.CreatedBy, got)
		}
	})

	t.Run("responded payload 结构", func(t *testing.T) {
		raw := buildInviteRespondedMessage(event.ID, "inv-1", "accepted")
		var msg struct {
			Type    string `json:"type"`
			Payload struct {
				EventID string `json:"eventId"`
				UserID  string `json:"userId"`
				Status  string `json:"status"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Type != "schedule_invite_responded" {
			t.Errorf("expected type schedule_invite_responded, got %s", msg.Type)
		}
		if msg.Payload.EventID != event.ID || msg.Payload.UserID != "inv-1" || msg.Payload.Status != "accepted" {
			t.Errorf("unexpected payload: %+v", msg.Payload)
		}
	})
}

// TestUpdateEventResponseCarriesInvites 编辑带邀请的事件：响应体带 invites；无邀请事件保持原形状。
func TestUpdateEventResponseCarriesInvites(t *testing.T) {
	_, env, db, space := setupInviteFixture(t)

	t.Run("带邀请事件的编辑响应含 invites", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "编辑我", "all", "user-1", time.Now().Add(24*time.Hour))
		seedInvite(t, db, event.ID, "inv-1", InviteStatusAccepted)

		w, resp := env.do(t, "PUT", "/api/v1/schedule/events/"+event.ID, map[string]interface{}{"title": "改个名"})
		data := mustOK(t, w, resp)
		invites, ok := data["invites"].([]interface{})
		if !ok || len(invites) != 1 {
			t.Fatalf("expected invites array of 1 in update response, got %v", data["invites"])
		}
		first, _ := invites[0].(map[string]interface{})
		if first["userId"] != "inv-1" || first["status"] != InviteStatusAccepted {
			t.Errorf("unexpected invites entry: %v", first)
		}
	})

	t.Run("无邀请事件响应形状不变（不带 invites 键）", func(t *testing.T) {
		event := seedEvent(t, db, space.ID, "别动我", "all", "user-1", time.Now().Add(24*time.Hour))

		w, resp := env.do(t, "PUT", "/api/v1/schedule/events/"+event.ID, map[string]interface{}{"title": "改名2"})
		data := mustOK(t, w, resp)
		if _, exists := data["invites"]; exists {
			t.Errorf("plain event update response must not carry invites, got %v", data)
		}
		if data["message"] != "updated" {
			t.Errorf("expected message=updated, got %v", data["message"])
		}
	})
}

// TestDeleteEventCascadesInvites 删除事件级联清理邀请行（迁移 000039 无外键，业务层保证）。
func TestDeleteEventCascadesInvites(t *testing.T) {
	_, env, db, space := setupInviteFixture(t)

	event := seedEvent(t, db, space.ID, "即将消亡", "all", "user-1", time.Now().Add(24*time.Hour))
	seedInvite(t, db, event.ID, "inv-1", InviteStatusPending)
	seedInvite(t, db, event.ID, "inv-2", InviteStatusAccepted)

	w, _ := env.do(t, "DELETE", "/api/v1/schedule/events/"+event.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&model.ScheduleEventInvite{}).Where("event_id = ?", event.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected invites cascaded to 0 rows, got %d", count)
	}
}

// ─────────────────────────────────────────────────────────────
// 测试路由辅助：以指定 userId/role 注入 gin 上下文的邀请路由
// ─────────────────────────────────────────────────────────────

// makeInviteRouter 构建以固定 userId（MEMBER 角色）注入上下文的邀请路由。
func makeInviteRouter(h *Handler, userID string) http.Handler {
	return makeInviteRouterWithRole(h, userID, "MEMBER")
}

// makeInviteRouterWithRole 构建以固定 userId+role 注入上下文的邀请路由。
func makeInviteRouterWithRole(h *Handler, userID, role string) http.Handler {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	sc := api.Group("/schedule")
	{
		sc.GET("/events/:id/invites", func(c *gin.Context) {
			c.Set("user_id", userID)
			c.Set("username", userID)
			c.Set("role", role)
			c.Set("space_id", "space-test")
			h.GetInvites(c)
		})
		sc.PUT("/events/:id/invite", func(c *gin.Context) {
			c.Set("user_id", userID)
			c.Set("username", userID)
			c.Set("role", role)
			c.Set("space_id", "space-test")
			h.RespondInvite(c)
		})
	}
	return r
}
