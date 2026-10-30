package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

// TestHandlerGetRealtimeMetrics 验证 M2 实时指标端点的聚合口径：
//   - 在线用户来自 hub（RegisterTestUser 注入）；
//   - rooms 只统计 left_at IS NULL 的在房成员，按 room_id 分组；
//   - activeScreenshares 只统计 active=true 的会话。
func TestHandlerGetRealtimeMetrics(t *testing.T) {
	handler, router, owner, token, hub := setupAdminHandlerWithHub(t)

	// 1 个在线用户（owner）。
	hub.RegisterTestUser(owner.ID)
	time.Sleep(100 * time.Millisecond)

	// 两个语音房间：room-a 2 人、room-b 1 人；另造 1 条已离开记录（不计）。
	roomA := &model.VoiceRoom{ID: idgen.NextString(), SpaceID: "space-1", LiveKitRoom: "lk-a"}
	roomB := &model.VoiceRoom{ID: idgen.NextString(), SpaceID: "space-1", LiveKitRoom: "lk-b"}
	for _, r := range []*model.VoiceRoom{roomA, roomB} {
		if err := handler.db.Create(r).Error; err != nil {
			t.Fatalf("create voice room: %v", err)
		}
	}
	now := time.Now()
	participants := []*model.VoiceParticipant{
		{ID: idgen.NextString(), RoomID: roomA.ID, UserID: "u1", JoinedAt: now, LastActiveAt: now},
		{ID: idgen.NextString(), RoomID: roomA.ID, UserID: "u2", JoinedAt: now, LastActiveAt: now},
		{ID: idgen.NextString(), RoomID: roomB.ID, UserID: owner.ID, JoinedAt: now, LastActiveAt: now},
		{ID: idgen.NextString(), RoomID: roomB.ID, UserID: "u3", JoinedAt: now, LastActiveAt: now, LeftAt: &now},
	}
	for _, p := range participants {
		if err := handler.db.Create(p).Error; err != nil {
			t.Fatalf("create voice participant: %v", err)
		}
	}

	// 屏幕共享：1 个进行中（active）+ 1 个已结束（不计）。
	active := &model.ScreenShareSession{
		ID: idgen.NextString(), UserID: owner.ID, SpaceID: "space-1", ChannelID: "ch-a",
		Active: true, Status: "active", StartedAt: now,
	}
	ended := &model.ScreenShareSession{
		ID: idgen.NextString(), UserID: owner.ID, SpaceID: "space-1", ChannelID: "ch-b",
		Active: false, Status: "ended", StartedAt: now, EndedAt: &now,
	}
	for _, s := range []*model.ScreenShareSession{active, ended} {
		if err := handler.db.Create(s).Error; err != nil {
			t.Fatalf("create screenshare session: %v", err)
		}
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/metrics/realtime", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object, got: %v", resp)
	}

	if got := int(toFloat(data["onlineUsers"])); got != 1 {
		t.Errorf("onlineUsers = %v, want 1", data["onlineUsers"])
	}
	if got := int(toFloat(data["voiceRooms"])); got != 2 {
		t.Errorf("voiceRooms = %v, want 2", data["voiceRooms"])
	}
	if got := int(toFloat(data["activeScreenshares"])); got != 1 {
		t.Errorf("activeScreenshares = %v, want 1", data["activeScreenshares"])
	}
	roomsRaw, ok := data["rooms"].([]interface{})
	if !ok {
		t.Fatalf("expected rooms array, got: %T", data["rooms"])
	}
	if len(roomsRaw) != 2 {
		t.Fatalf("expected 2 rooms, got %d: %v", len(roomsRaw), data["rooms"])
	}
	countByRoom := map[string]int{}
	for _, raw := range roomsRaw {
		room, _ := raw.(map[string]interface{})
		rid, _ := room["roomId"].(string)
		if rid == "" {
			t.Errorf("room entry missing roomId: %v", room)
		}
		countByRoom[rid] = int(toFloat(room["participantCount"]))
	}
	if countByRoom[roomA.ID] != 2 {
		t.Errorf("room-a participantCount = %d, want 2", countByRoom[roomA.ID])
	}
	if countByRoom[roomB.ID] != 1 {
		t.Errorf("room-b participantCount = %d, want 1 (left_at IS NULL filter)", countByRoom[roomB.ID])
	}
}

// TestHandlerGetRealtimeMetricsEmpty 验证空库/hub 在线为 0 时返回稳定结构
// （rooms 为 [] 而非 null，各计数字段存在且为 0）。
func TestHandlerGetRealtimeMetricsEmpty(t *testing.T) {
	_, router, _, token, hub := setupAdminHandlerWithHub(t)
	_ = hub // 不注册任何在线用户

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/metrics/realtime", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object, got: %v", resp)
	}
	roomsRaw, ok := data["rooms"].([]interface{})
	if !ok || len(roomsRaw) != 0 {
		t.Errorf("expected empty rooms array, got: %T %v", data["rooms"], data["rooms"])
	}
	for _, field := range []string{"onlineUsers", "voiceRooms", "activeScreenshares"} {
		if got := toFloat(data[field]); got != 0 {
			t.Errorf("field %q = %v, want 0", field, data[field])
		}
	}
}

// toFloat 将 JSON 解出的数值统一为 float64（JSON number → float64）。
func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
