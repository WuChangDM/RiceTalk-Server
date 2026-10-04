package message

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func setupAuthContext(c *gin.Context, userID, username, role string) {
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("email", username+"@example.com")
	c.Set("role", role)
}

func TestHandlerCreateMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		hub := realtime.NewHub(testutil.TestLogger())
		handler := NewHandler(db, cfg, hub, nil)

		router := gin.New()
		router.POST("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.CreateMessage)

		body, _ := json.Marshal(map[string]string{
			"content": "Hello, world!",
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/channels/ch-1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
	})

	t.Run("broadcasts websocket event", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		hub := realtime.NewHub(testutil.TestLogger())
		go hub.Run()
		// Give hub time to start
		time.Sleep(50 * time.Millisecond)

		handler := NewHandler(db, cfg, hub, nil)

		router := gin.New()
		router.POST("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.CreateMessage)

		body, _ := json.Marshal(map[string]string{
			"content": "Hello, world!",
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/channels/ch-1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}
	})
	t.Run("JSON parentId binds and is echoed", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		handler := NewHandler(db, cfg, nil, nil)

		parent, err := handler.service.CreateMessage("ch-1", "user-1", "testuser", middleware.RoleMember, "parent", idgen.NextString(), "", nil)
		if err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		router := gin.New()
		router.POST("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.CreateMessage)

		body, _ := json.Marshal(map[string]interface{}{
			"content":  "thread reply via JSON",
			"parentId": parent.ID,
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/channels/ch-1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		data, _ := resp["data"].(map[string]interface{})
		if data == nil {
			t.Fatalf("expected data object, got %v", resp)
		}
		if data["parentId"] != parent.ID {
			t.Errorf("expected response parentId %s, got %v", parent.ID, data["parentId"])
		}
	})
}

func TestHandlerGetMessages(t *testing.T) {
	t.Run("pagination", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		handler := NewHandler(db, cfg, nil, nil)

		// Create some messages
		channelID := "ch-1"
		for i := 0; i < 15; i++ {
			_, err := handler.service.CreateMessage(channelID, "user-1", "testuser", middleware.RoleMember, "msg"+idgen.NextString(), idgen.NextString(), "", nil)
			if err != nil {
				t.Fatalf("failed to create message: %v", err)
			}
		}

		router := gin.New()
		router.GET("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.GetMessages)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/channels/ch-1/messages?limit=5", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
		data, ok := resp["data"].(map[string]interface{})
		if !ok {
			t.Fatal("expected data object in response")
		}
		items, ok := data["items"].([]interface{})
		if !ok {
			t.Fatal("expected items array in data")
		}
		if len(items) != 5 {
			t.Errorf("expected 5 items, got %d", len(items))
		}
		hasMore, ok := data["hasMore"].(bool)
		if !ok {
			t.Fatal("expected hasMore boolean in data")
		}
		if !hasMore {
			t.Error("expected hasMore to be true")
		}
	})

	t.Run("invalid cursor returns 400", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		handler := NewHandler(db, cfg, nil, nil)

		router := gin.New()
		router.GET("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.GetMessages)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/channels/ch-1/messages?cursor=invalid-cursor-id", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})

	t.Run("around returns neighbourhood page with target", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		handler := NewHandler(db, cfg, nil, nil)

		// 10 messages with strictly increasing timestamps, contents m0..m9.
		base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
		for i := 0; i < 10; i++ {
			msg := &model.Message{
				ID:        idgen.NextString(),
				ChannelID: "ch-1",
				UserID:    "user-1",
				Content:   fmt.Sprintf("m%d", i),
				Type:      "text",
				CreatedAt: base.Add(time.Duration(i) * time.Minute),
				UpdatedAt: base.Add(time.Duration(i) * time.Minute),
			}
			if err := db.Create(msg).Error; err != nil {
				t.Fatalf("failed to seed message m%d: %v", i, err)
			}
		}
		var target model.Message
		if err := db.First(&target, "channel_id = ? AND content = ?", "ch-1", "m5").Error; err != nil {
			t.Fatalf("failed to load target: %v", err)
		}

		router := gin.New()
		router.GET("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.GetMessages)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/channels/ch-1/messages?around="+target.ID+"&limit=5", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
		data := resp["data"].(map[string]interface{})
		items := data["items"].([]interface{})
		if len(items) != 5 {
			t.Fatalf("expected 5 items, got %d", len(items))
		}
		want := []string{"m3", "m4", "m5", "m6", "m7"}
		for i, item := range items {
			content := item.(map[string]interface{})["content"].(string)
			if content != want[i] {
				t.Errorf("position %d: expected %s, got %s", i, want[i], content)
			}
		}
		if hasMore, _ := data["hasMore"].(bool); !hasMore {
			t.Error("expected hasMore to be true")
		}
	})

	t.Run("around unknown message returns 404 MESSAGE_NOT_FOUND", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		handler := NewHandler(db, cfg, nil, nil)

		router := gin.New()
		router.GET("/channels/:id/messages", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.GetMessages)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/channels/ch-1/messages?around=missing-message-id", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "MESSAGE_NOT_FOUND" {
			t.Errorf("expected code MESSAGE_NOT_FOUND, got %v", resp["code"])
		}
	})
}

func TestHandlerSearchAllMessagesValidationAndScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: "space-search-handler", Name: "Search", OwnerID: "owner-search-handler"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	textChannel := &model.Channel{ID: "channel-search-handler-text", SpaceID: space.ID, Name: "general", Type: "text", Visibility: "public"}
	dmChannel := &model.Channel{ID: "channel-search-handler-dm", SpaceID: space.ID, Name: "legacy-dm", Type: "DM", Visibility: "public"}
	voiceChannel := &model.Channel{ID: "channel-search-handler-voice", SpaceID: space.ID, Name: "voice", Type: "voice", Visibility: "public"}
	for _, ch := range []*model.Channel{textChannel, dmChannel, voiceChannel} {
		if err := db.Create(ch).Error; err != nil {
			t.Fatalf("create channel %s: %v", ch.ID, err)
		}
	}
	if err := db.Create(&model.Message{
		ID:        "message-search-handler",
		ChannelID: textChannel.ID,
		UserID:    "user-search-handler",
		Content:   "visible keyword",
		Type:      "text",
	}).Error; err != nil {
		t.Fatalf("create searchable message: %v", err)
	}

	handler := NewHandler(db, config.DefaultConfig(), nil, nil)
	request := func(rawQuery string) (int, map[string]interface{}) {
		router := gin.New()
		router.GET("/messages/search", func(c *gin.Context) {
			setupAuthContext(c, "user-search-handler", "searcher", middleware.RoleMember)
			c.Next()
		}, handler.SearchAllMessages)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/messages/search?"+rawQuery, nil))
		var body map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response for %q: %v; body=%s", rawQuery, err, w.Body.String())
		}
		return w.Code, body
	}

	status, body := request("q=keyword")
	if status != http.StatusOK || body["code"] != "OK" {
		t.Fatalf("expected successful visible search, status=%d body=%v", status, body)
	}
	data, ok := body["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected search data, got %v", body["data"])
	}
	if total, ok := data["total"].(float64); !ok || total != 1 {
		t.Fatalf("expected one visible result, got %v", data["total"])
	}

	cases := []struct {
		name      string
		query     string
		status    int
		errorCode string
	}{
		{name: "short query", query: "q=x", status: http.StatusBadRequest, errorCode: "SEARCH_QUERY_TOO_SHORT"},
		{name: "invalid limit", query: "q=keyword&limit=101", status: http.StatusBadRequest, errorCode: "SYSTEM_BAD_REQUEST"},
		{name: "non numeric limit", query: "q=keyword&limit=nope", status: http.StatusBadRequest, errorCode: "SYSTEM_BAD_REQUEST"},
		{name: "invalid before", query: "q=keyword&before=not-time", status: http.StatusBadRequest, errorCode: "SYSTEM_BAD_REQUEST"},
		{name: "invalid after", query: "q=keyword&after=not-time", status: http.StatusBadRequest, errorCode: "SYSTEM_BAD_REQUEST"},
		{name: "DM denied", query: "q=keyword&channel_id=" + dmChannel.ID, status: http.StatusForbidden, errorCode: "CHANNEL_ACCESS_DENIED"},
		{name: "voice denied", query: "q=keyword&channel_id=" + voiceChannel.ID, status: http.StatusForbidden, errorCode: "CHANNEL_ACCESS_DENIED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := request(tc.query)
			if status != tc.status {
				t.Fatalf("expected status %d, got %d body=%v", tc.status, status, body)
			}
			if body["code"] != tc.errorCode {
				t.Fatalf("expected error code %s, got %v", tc.errorCode, body["code"])
			}
		})
	}
}

// TestUnreadChangedPayloadContract 锁定 unread_count_changed 广播 payload 的 JSON 键名：
// T5 起 firstUnreadMessageId 参与广播，客户端据此更新「首条未读」定位锚点
// （mark-read 场景未读清零 → 空串；通用场景传真实首条未读 ID）。
func TestUnreadChangedPayloadContract(t *testing.T) {
	cases := []struct {
		name            string
		payload         map[string]interface{}
		wantFirstUnread string
	}{
		{"mark-read 后未读清零", unreadChangedPayload("ch-1", "user-1", 0, 0, ""), ""},
		{"存在未读时携带锚点", unreadChangedPayload("ch-1", "user-1", 3, 1, "msg_first_unread"), "msg_first_unread"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]interface{}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, key := range []string{"channelId", "userId", "unreadCount", "mentionCount", "firstUnreadMessageId"} {
				if _, ok := got[key]; !ok {
					t.Errorf("payload missing key %q, got %v", key, string(data))
				}
			}
			if got["firstUnreadMessageId"] != tc.wantFirstUnread {
				t.Errorf("expected firstUnreadMessageId %q, got %v", tc.wantFirstUnread, got["firstUnreadMessageId"])
			}
			if got["channelId"] != "ch-1" || got["userId"] != "user-1" {
				t.Errorf("unexpected channelId/userId: %v", string(data))
			}
		})
	}
}

// TestHandlerMarkChannelRead 走通 M16 路由契约：
//   - 空 body（缺省取频道最新一条）返回 200；
//   - 带 lastMessageId（读到中间某条）后，GetUnreadCount 只统计该条之后的消息
//     ——这是客户端「进入频道定位到未读处即 mark-read 到定位点」语义的服务端支撑。
func TestHandlerMarkChannelRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	handler := NewHandler(db, cfg, nil, nil)

	base := time.Now().UTC().Add(-time.Hour)
	msgs := []model.Message{
		{ID: idgen.NextString(), ChannelID: "ch-mr", UserID: "user-1", Content: "first", CreatedAt: base, UpdatedAt: base},
		{ID: idgen.NextString(), ChannelID: "ch-mr", UserID: "user-2", Content: "second", CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
		{ID: idgen.NextString(), ChannelID: "ch-mr", UserID: "user-2", Content: "third", CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute)},
	}
	for i := range msgs {
		if err := db.Create(&msgs[i]).Error; err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
	}

	router := gin.New()
	router.POST("/channels/:id/mark-read", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.MarkChannelRead)
	router.GET("/channels/:id/unread", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.GetUnreadCount)

	// 1) mark-read 到中间消息（定位点语义）
	body := []byte(`{"lastMessageId":"` + msgs[0].ID + `"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/channels/ch-mr/mark-read", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 未读 = 定位点之后的消息数（second/third 共 2 条），首条未读 = second
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/channels/ch-mr/unread", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unread expected 200, got %d", w.Code)
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			UnreadCount          int64  `json:"unreadCount"`
			FirstUnreadMessageID string `json:"firstUnreadMessageId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal unread: %v", err)
	}
	if resp.Data.UnreadCount != 2 {
		t.Errorf("expected unreadCount 2, got %d", resp.Data.UnreadCount)
	}
	if resp.Data.FirstUnreadMessageID != msgs[1].ID {
		t.Errorf("expected firstUnreadMessageId %s, got %q", msgs[1].ID, resp.Data.FirstUnreadMessageID)
	}

	// 2) 空 body：缺省取频道最新一条，未读清零
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/channels/ch-mr/mark-read", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("empty-body mark-read expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/channels/ch-mr/unread", nil)
	router.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.UnreadCount != 0 {
		t.Errorf("expected unreadCount 0 after latest mark-read, got %d", resp.Data.UnreadCount)
	}
}
