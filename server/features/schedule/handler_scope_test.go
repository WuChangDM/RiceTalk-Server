package schedule

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"

	"gorm.io/gorm"
)

// setupScopeFixture 构建 N4/N5 测试基线：1 空间 + 4 用户（OWNER/ADMIN/MEMBER/停用）+ 成员关系
func setupScopeFixture(t *testing.T) (*Handler, *gorm.DB, *model.Space) {
	t.Helper()
	h, _, db, space := setupTestHandler()
	// 用户：owner-1(OWNER)、admin-1(ADMIN)、member-1(MEMBER)
	users := []model.User{
		{ID: "owner-1", Username: "owner-1", Email: "owner-1@test.local", Role: "OWNER", IsActive: true},
		{ID: "admin-1", Username: "admin-1", Email: "admin-1@test.local", Role: "ADMIN", IsActive: true},
		{ID: "member-1", Username: "member-1", Email: "member-1@test.local", Role: "MEMBER", IsActive: true},
	// 停用成员：不应收到任何提醒
	{ID: "member-gone", Username: "member-gone", Email: "member-gone@test.local", Role: "MEMBER", IsActive: true},
	}
	for _, u := range users {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("seed user %s: %v", u.ID, err)
		}
	}
	// IsActive 带 default:true，Create 时零值 false 会被 GORM 跳过落库为 true，
	// 因此停用状态必须在创建后显式 Update
	if err := db.Model(&model.User{}).Where("id = ?", "member-gone").Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate member-gone: %v", err)
	}
	// 空间成员：owner-1、admin-1、member-1、member-gone（停用）
	for _, uid := range []string{"owner-1", "admin-1", "member-1", "member-gone"} {
		m := model.Membership{ID: idgen.NextString(), UserID: uid, SpaceID: space.ID}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed membership %s: %v", uid, err)
		}
	}
	// 非本空间成员（scope=all 事件不应通知他）
	outsider := model.Membership{ID: idgen.NextString(), UserID: "admin-1", SpaceID: "other-space"}
	if err := db.Create(&outsider).Error; err != nil {
		t.Fatalf("seed outsider membership: %v", err)
	}
	return h, db, space
}

// TestReminderRecipientsSpaceEvent N4: scope=all 事件的收件人是本空间全部活跃成员
func TestReminderRecipientsSpaceEvent(t *testing.T) {
	h, db, space := setupScopeFixture(t)

	event := model.ScheduleEvent{ID: idgen.NextString(), SpaceID: space.ID, Title: "全员事件", Scope: "all"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	recipients, err := h.reminderRecipients(event)
	if err != nil {
		t.Fatalf("reminderRecipients: %v", err)
	}
	got := map[string]bool{}
	for _, id := range recipients {
		got[id] = true
	}
	// 空间事件成员收得到：owner-1 / admin-1 / member-1 均应收到
	for _, want := range []string{"owner-1", "admin-1", "member-1"} {
		if !got[want] {
			t.Errorf("expected recipient %s for scope=all event, got %v", want, recipients)
		}
	}
	// 停用用户收不到
	if got["member-gone"] {
		t.Errorf("inactive user member-gone should not receive reminder, got %v", recipients)
	}
}

// TestReminderRecipientsAdminOnlyEvent N4: scope=admin_only 事件的收件人仅 OWNER/ADMIN，普通成员收不到
func TestReminderRecipientsAdminOnlyEvent(t *testing.T) {
	h, db, space := setupScopeFixture(t)

	event := model.ScheduleEvent{ID: idgen.NextString(), SpaceID: space.ID, Title: "受限事件", Scope: "admin_only"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	recipients, err := h.reminderRecipients(event)
	if err != nil {
		t.Fatalf("reminderRecipients: %v", err)
	}
	got := map[string]bool{}
	for _, id := range recipients {
		got[id] = true
	}
	// 受限事件管理员收得到
	if !got["owner-1"] || !got["admin-1"] {
		t.Errorf("expected owner-1/admin-1 to receive admin_only reminder, got %v", recipients)
	}
	// 受限事件非管理员收不到（越权泄露点）
	if got["member-1"] {
		t.Errorf("member-1 must NOT receive admin_only event reminder, got %v", recipients)
	}
	if got["member-gone"] {
		t.Errorf("inactive member-gone must NOT receive admin_only event reminder, got %v", recipients)
	}
}

// TestUpdateEventPartialDoesNotClearFields N5: 部分更新只改传入字段，未传字段保持原值
func TestUpdateEventPartialDoesNotClearFields(t *testing.T) {
	_, r, db, space := setupTestHandler()

	event := model.ScheduleEvent{
		ID:          idgen.NextString(),
		SpaceID:     space.ID,
		Title:       "原标题",
		Description: "原描述",
		Location:    "原地点",
		EventDate:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime:   time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
		Scope:       "all",
		CreatedBy:   "user-1",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	// 只传 reminderMinutes（典型部分更新场景：旧客户端或仅改提醒）
	reminder := 45
	body := map[string]interface{}{"reminderMinutes": reminder}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var updated model.ScheduleEvent
	db.First(&updated, event.ID)
	if updated.Title != "原标题" {
		t.Errorf("partial update must keep title, got %q", updated.Title)
	}
	if updated.Description != "原描述" {
		t.Errorf("partial update must keep description, got %q", updated.Description)
	}
	if updated.Location != "原地点" {
		t.Errorf("partial update must keep location, got %q", updated.Location)
	}
	if !updated.StartTime.Equal(event.StartTime) {
		t.Errorf("partial update must keep startTime, got %v want %v", updated.StartTime, event.StartTime)
	}
	if updated.ReminderMinutes != 45 {
		t.Errorf("expected ReminderMinutes=45, got %d", updated.ReminderMinutes)
	}
}

// TestUpdateEventExplicitEmptyLocation N5: 显式传 location:"" 表示清空地点，且不影响其他字段
func TestUpdateEventExplicitEmptyLocation(t *testing.T) {
	_, r, db, space := setupTestHandler()

	event := model.ScheduleEvent{
		ID:          idgen.NextString(),
		SpaceID:     space.ID,
		Title:       "原标题",
		Description: "原描述",
		Location:    "原地点",
		EventDate:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime:   time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
		Scope:       "all",
		CreatedBy:   "user-1",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	body := map[string]interface{}{"location": ""}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var updated model.ScheduleEvent
	db.First(&updated, event.ID)
	if updated.Location != "" {
		t.Errorf("explicit empty location should clear the field, got %q", updated.Location)
	}
	if updated.Title != "原标题" {
		t.Errorf("clearing location must not affect title, got %q", updated.Title)
	}
}

// TestUpdateEventEmptyTitleRejected N5: 显式传空标题返回 400（与 CreateEvent 校验口径一致）
func TestUpdateEventEmptyTitleRejected(t *testing.T) {
	_, r, db, space := setupTestHandler()

	event := model.ScheduleEvent{
		ID:        idgen.NextString(),
		SpaceID:   space.ID,
		Title:     "原标题",
		EventDate: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		Scope:     "all",
		CreatedBy: "user-1",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	body := map[string]interface{}{"title": "   "}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for blank title, got %d body=%s", w.Code, w.Body.String())
	}
	var updated model.ScheduleEvent
	db.First(&updated, event.ID)
	if updated.Title != "原标题" {
		t.Errorf("rejected update must not modify title, got %q", updated.Title)
	}
}

// TestUpdateEventNoFields N5: 空更新体返回 400，不做无意义写入
func TestUpdateEventNoFields(t *testing.T) {
	_, r, db, space := setupTestHandler()

	event := model.ScheduleEvent{
		ID:        idgen.NextString(),
		SpaceID:   space.ID,
		Title:     "原标题",
		EventDate: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		Scope:     "all",
		CreatedBy: "user-1",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	jsonBody, _ := json.Marshal(map[string]interface{}{})
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty update body, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestShouldSendReminderGraceWindow 补发宽限窗边界：提醒时间已过 15 分钟内补发，超过不补发
func TestShouldSendReminderGraceWindow(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name          string
		reminderTime  time.Time
		expectSend    bool
	}{
		{"未到提醒时间", now.Add(5 * time.Minute), false},
		{"刚过提醒时间", now.Add(-1 * time.Minute), true},
		{"宽限窗边界内", now.Add(-15 * time.Minute), true},
		{"超过宽限窗", now.Add(-15*time.Minute - time.Second), false},
		{"严重过期", now.Add(-2 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSendReminder(now, tc.reminderTime); got != tc.expectSend {
				t.Errorf("shouldSendReminder(now, %v) = %v, want %v", tc.reminderTime, got, tc.expectSend)
			}
		})
	}
}

// TestScanAndSendRemindersStaleSkipped N4 附加项：超过宽限窗的陈旧提醒不补发，但仍标记 reminder_sent 防止重复扫描
func TestScanAndSendRemindersStaleSkipped(t *testing.T) {
	h, _, db, space := setupTestHandler()

	now := time.Now().UTC()
	// 陈旧提醒：提前 180 分钟提醒、1 小时后开始 → 提醒时间在 2 小时前（远超 15 分钟宽限窗）
	stale := model.ScheduleEvent{
		ID:              idgen.NextString(),
		SpaceID:         space.ID,
		Title:           "停机前的旧事件",
		EventDate:       time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StartTime:       now.Add(1 * time.Hour),
		EndTime:         now.Add(2 * time.Hour),
		Scope:           "all",
		ReminderMinutes: 180,
		ReminderSent:    false,
		CreatedBy:       "user-1",
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatalf("create stale event: %v", err)
	}

	h.scanAndSendReminders()

	var updated model.ScheduleEvent
	db.First(&updated, stale.ID)
	// 超窗陈旧提醒被静默标记为已发送，不会在后续扫描中反复出现
	if !updated.ReminderSent {
		t.Error("stale reminder should be marked reminder_sent=true to avoid rescanning")
	}
}
