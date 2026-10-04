package schedule

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func init() {
	_ = idgen.Init(1, 1)
}

func setupTestHandler() (*Handler, *gin.Engine, *gorm.DB, *model.Space) {
	return setupTestHandlerWithRole("MEMBER")
}

// setupTestHandlerWithRole creates a test handler with the specified role for all routes.
func setupTestHandlerWithRole(role string) (*Handler, *gin.Engine, *gorm.DB, *model.Space) {
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: idgen.NextString(), Name: "test-space", OwnerID: "user-1"}
	if err := db.Create(space).Error; err != nil {
		panic("create space: " + err.Error())
	}
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	sc := api.Group("/schedule")
	{
		sc.GET("/events", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", role)
			c.Set("space_id", space.ID)
			h.GetEvents(c)
		})
		sc.POST("/events", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", role)
			c.Set("space_id", space.ID)
			h.CreateEvent(c)
		})
		sc.PUT("/events/:id", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", role)
			c.Set("space_id", space.ID)
			h.UpdateEvent(c)
		})
		sc.DELETE("/events/:id", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", role)
			c.Set("space_id", space.ID)
			h.DeleteEvent(c)
		})
	}
	return h, r, db, space
}

// TestCreateEventFillsEventDate verifies L25: EventDate is filled from StartTime date part
func TestCreateEventFillsEventDate(t *testing.T) {
	_, r, db, _ := setupTestHandler()

	// 使用具体的时间（含时分秒）
	startTime := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":     "测试事件",
		"startTime": startTime.Format(time.RFC3339),
		"endTime":   startTime.Add(time.Hour).Format(time.RFC3339),
		"allDay":    false,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/schedule/events", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var event model.ScheduleEvent
	db.First(&event)

	// L25: EventDate 应该是 StartTime 的日期部分（时分秒为零）
	expectedDate := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !event.EventDate.Equal(expectedDate) {
		t.Errorf("expected EventDate=%v, got %v", expectedDate, event.EventDate)
	}
}

// TestGetEventsWithDateRange verifies L25: GetEvents supports date range query
func TestGetEventsWithDateRange(t *testing.T) {
	_, r, db, space := setupTestHandler()

	// 创建 3 个不同日期的事件
	events := []model.ScheduleEvent{
		{
			ID:        idgen.NextString(),
			Title:     "6月10日事件",
			EventDate: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
		{
			ID:        idgen.NextString(),
			Title:     "6月15日事件",
			EventDate: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC),
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
		{
			ID:        idgen.NextString(),
			Title:     "6月20日事件",
			EventDate: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC),
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
	}
	for _, e := range events {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create event: %v", err)
		}
	}

	// 查询 6月12日 到 6月18日 范围内的事件（应只返回 6月15日事件）
	req := httptest.NewRequest("GET", "/api/v1/schedule/events?startDate=2026-06-12&endDate=2026-06-18", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 event in date range, got %d", len(data))
	}
}

// TestUpdateEventSyncsEventDate verifies L25: UpdateEvent syncs EventDate when StartTime changes
func TestUpdateEventSyncsEventDate(t *testing.T) {
	_, r, db, space := setupTestHandler()

	// 创建一个 6月10日的事件
	event := model.ScheduleEvent{
		ID:        idgen.NextString(),
		Title:     "原事件",
		EventDate: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
		SpaceID:   space.ID,
		CreatedBy: "user-1",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("failed to create event: %v", err)
	}

	// 更新 StartTime 到 6月25日
	newStart := time.Date(2026, 6, 25, 14, 0, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":     "更新后事件",
		"startTime": newStart.Format(time.RFC3339),
		"endTime":   newStart.Add(time.Hour).Format(time.RFC3339),
		"allDay":    false,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 验证 EventDate 已同步更新
	var updated model.ScheduleEvent
	db.First(&updated, event.ID)
	expectedDate := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	if !updated.EventDate.Equal(expectedDate) {
		t.Errorf("expected EventDate=%v after update, got %v", expectedDate, updated.EventDate)
	}
}

// TestCreateEventWithScope verifies M24: CreateEvent accepts scope field and stores it
func TestCreateEventWithScope(t *testing.T) {
	_, r, db, _ := setupTestHandlerWithRole("ADMIN")

	startTime := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":     "管理员事件",
		"startTime": startTime.Format(time.RFC3339),
		"endTime":   startTime.Add(time.Hour).Format(time.RFC3339),
		"allDay":    false,
		"scope":     "admin_only",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/schedule/events", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var event model.ScheduleEvent
	db.First(&event)
	if event.Scope != "admin_only" {
		t.Errorf("expected Scope=admin_only, got %q", event.Scope)
	}
}

// TestCreateEventDefaultScope verifies M24: scope defaults to "all" when not provided
func TestCreateEventDefaultScope(t *testing.T) {
	_, r, db, _ := setupTestHandler()

	startTime := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":     "默认事件",
		"startTime": startTime.Format(time.RFC3339),
		"endTime":   startTime.Add(time.Hour).Format(time.RFC3339),
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/schedule/events", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var event model.ScheduleEvent
	db.First(&event)
	if event.Scope != "all" {
		t.Errorf("expected default Scope=all, got %q", event.Scope)
	}
}

// TestCreateEventInvalidScope verifies M24: invalid scope returns 400
func TestCreateEventInvalidScope(t *testing.T) {
	_, r, _, _ := setupTestHandler()

	startTime := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":     "无效事件",
		"startTime": startTime.Format(time.RFC3339),
		"endTime":   startTime.Add(time.Hour).Format(time.RFC3339),
		"scope":     "private",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/schedule/events", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid scope, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestGetEventsScopeFilterForMember verifies M24: non-admin users only see scope=all events
func TestGetEventsScopeFilterForMember(t *testing.T) {
	_, r, db, space := setupTestHandlerWithRole("MEMBER")

	// 创建 2 个事件：一个 scope=all，一个 scope=admin_only
	events := []model.ScheduleEvent{
		{
			ID:        idgen.NextString(),
			Title:     "公开事件",
			EventDate: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC),
			Scope:     "all",
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
		{
			ID:        idgen.NextString(),
			Title:     "管理员事件",
			EventDate: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC),
			Scope:     "admin_only",
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
	}
	for _, e := range events {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create event: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/schedule/events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 event for member (only scope=all), got %d", len(data))
	}
}

// TestGetEventsScopeFilterForAdmin verifies M24: admin users see all events
func TestGetEventsScopeFilterForAdmin(t *testing.T) {
	_, r, db, space := setupTestHandlerWithRole("ADMIN")

	events := []model.ScheduleEvent{
		{
			ID:        idgen.NextString(),
			Title:     "公开事件",
			EventDate: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC),
			Scope:     "all",
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
		{
			ID:        idgen.NextString(),
			Title:     "管理员事件",
			EventDate: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC),
			StartTime: time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC),
			Scope:     "admin_only",
			SpaceID:   space.ID,
			CreatedBy: "user-1",
		},
	}
	for _, e := range events {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create event: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/schedule/events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 2 {
		t.Errorf("expected 2 events for admin (all scopes), got %d", len(data))
	}
}

// TestUpdateEventScope verifies M24: UpdateEvent can update scope field
func TestUpdateEventScope(t *testing.T) {
	_, r, db, space := setupTestHandlerWithRole("ADMIN")

	event := model.ScheduleEvent{
		ID:        idgen.NextString(),
		Title:     "原事件",
		EventDate: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
		Scope:     "all",
		SpaceID:   space.ID,
		CreatedBy: "user-1",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("failed to create event: %v", err)
	}

	body := map[string]interface{}{
		"title":     "更新后事件",
		"startTime": event.StartTime.Format(time.RFC3339),
		"endTime":   event.EndTime.Format(time.RFC3339),
		"scope":     "admin_only",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var updated model.ScheduleEvent
	db.First(&updated, event.ID)
	if updated.Scope != "admin_only" {
		t.Errorf("expected Scope=admin_only after update, got %q", updated.Scope)
	}
}

// ===== L26: 日程提醒功能测试 =====

// TestCreateEventWithReminder verifies L26: CreateEvent stores reminderMinutes
func TestCreateEventWithReminder(t *testing.T) {
	_, r, db, _ := setupTestHandler()

	startTime := time.Date(2026, 7, 1, 14, 30, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":           "带提醒的事件",
		"startTime":       startTime.Format(time.RFC3339),
		"endTime":         startTime.Add(time.Hour).Format(time.RFC3339),
		"reminderMinutes": 30,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/schedule/events", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var event model.ScheduleEvent
	db.First(&event, "title = ?", "带提醒的事件")
	if event.ReminderMinutes != 30 {
		t.Errorf("expected ReminderMinutes=30, got %d", event.ReminderMinutes)
	}
	if event.ReminderSent {
		t.Error("expected ReminderSent=false for new event")
	}
}

// TestCreateEventInvalidReminder verifies L26: invalid reminderMinutes returns 400
func TestCreateEventInvalidReminder(t *testing.T) {
	_, r, _, _ := setupTestHandler()

	startTime := time.Date(2026, 7, 1, 14, 30, 0, 0, time.UTC)
	body := map[string]interface{}{
		"title":           "无效提醒",
		"startTime":       startTime.Format(time.RFC3339),
		"reminderMinutes": 2000, // 超过1440
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/schedule/events", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for reminderMinutes=2000, got %d", w.Code)
	}
}

// TestUpdateEventReminder verifies L26: UpdateEvent updates reminderMinutes and resets reminder_sent
func TestUpdateEventReminder(t *testing.T) {
	_, r, db, space := setupTestHandlerWithRole("ADMIN")

	// 创建一个已发送提醒的事件
	event := model.ScheduleEvent{
		ID:              idgen.NextString(),
		SpaceID:         space.ID,
		Title:           "原事件",
		EventDate:       time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		StartTime:       time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		EndTime:         time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
		Scope:           "all",
		ReminderMinutes: 15,
		ReminderSent:    true, // 已发送
		CreatedBy:       "user-1",
	}
	db.Create(&event)

	// 更新提醒分钟数
	newReminder := 60
	body := map[string]interface{}{
		"title":           event.Title,
		"startTime":       event.StartTime.Format(time.RFC3339),
		"reminderMinutes": newReminder,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/v1/schedule/events/"+event.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var updated model.ScheduleEvent
	db.First(&updated, event.ID)
	if updated.ReminderMinutes != 60 {
		t.Errorf("expected ReminderMinutes=60, got %d", updated.ReminderMinutes)
	}
	if updated.ReminderSent {
		t.Error("expected ReminderSent=false after updating reminderMinutes")
	}
}

// TestScanAndSendReminders verifies L26: scanAndSendReminders sends reminders for due events
func TestScanAndSendReminders(t *testing.T) {
	h, _, db, space := setupTestHandler()

	// 创建一个即将到达提醒时间的事件（5分钟后开始，提前10分钟提醒 -> 提醒时间已过）
	now := time.Now().UTC()
	event := model.ScheduleEvent{
		ID:              idgen.NextString(),
		SpaceID:         space.ID,
		Title:           "即将开始的事件",
		EventDate:       time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StartTime:       now.Add(5 * time.Minute), // 5分钟后开始
		EndTime:         now.Add(6 * time.Minute),
		Scope:           "all",
		ReminderMinutes: 10, // 提前10分钟提醒 -> 提醒时间 = now - 5min（已过）
		ReminderSent:    false,
		CreatedBy:       "user-1",
	}
	db.Create(&event)

	// 创建一个不需要提醒的事件（reminderMinutes=0）
	event2 := model.ScheduleEvent{
		ID:              idgen.NextString(),
		SpaceID:         space.ID,
		Title:           "无提醒事件",
		EventDate:       time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		StartTime:       now.Add(5 * time.Minute),
		EndTime:         now.Add(6 * time.Minute),
		Scope:           "all",
		ReminderMinutes: 0,
		ReminderSent:    false,
		CreatedBy:       "user-1",
	}
	db.Create(&event2)

	// 执行扫描
	h.scanAndSendReminders()

	// 验证第一个事件已标记为已发送
	var updated1 model.ScheduleEvent
	db.First(&updated1, event.ID)
	if !updated1.ReminderSent {
		t.Error("expected ReminderSent=true for event with due reminder")
	}

	// 验证第二个事件未被标记（reminderMinutes=0 不触发）
	var updated2 model.ScheduleEvent
	db.First(&updated2, event2.ID)
	if updated2.ReminderSent {
		t.Error("expected ReminderSent=false for event with reminderMinutes=0")
	}
}
