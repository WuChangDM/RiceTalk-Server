package whiteboard

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"os"
	"path/filepath"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func setupTest(t *testing.T) (*Handler, *gin.Engine, *config.Config, *gorm.DB, *model.Whiteboard) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: idgen.NextString(), Name: "test-space", OwnerID: "user-1"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	cfg := &config.Config{
		JWTSecret:     "test-jwt-secret-for-whiteboard-encryption-32b",
		EncryptionKey: "test-encryption-key-for-whiteboard-32bytes!",
	}
	h := NewHandler(db, cfg, nil)

	svc := NewService(db)
	wb, err := svc.Create(space.ID, "Test Whiteboard", "user-1")
	if err != nil {
		t.Fatalf("create whiteboard: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	wbGroup := api.Group("/whiteboards")
	{
		wbGroup.GET("", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.List(c)
		})
		wbGroup.POST("", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.Create(c)
		})
		wbGroup.PATCH("/:id", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "ADMIN", space.ID)
			h.Update(c)
		})
		wbGroup.DELETE("/:id", func(c *gin.Context) {
			setTestContext(c, "admin-1", "admin-user", "ADMIN", space.ID)
			h.Delete(c)
		})
		wbGroup.GET("/:id/strokes", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.GetStrokes(c)
		})
		wbGroup.POST("/:id/strokes", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.CreateStroke(c)
		})
		wbGroup.DELETE("/:id/strokes", func(c *gin.Context) {
			setTestContext(c, "admin-1", "admin-user", "ADMIN", space.ID)
			h.ClearStrokes(c)
		})
		wbGroup.DELETE("/:id/strokes/:strokeId", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.DeleteStroke(c)
		})
		wbGroup.POST("/:id/thumbnail", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.UploadThumbnail(c)
		})
		wbGroup.GET("/:id/thumbnail", func(c *gin.Context) {
			setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
			h.GetThumbnail(c)
		})
	}
	return h, r, cfg, db, wb
}

func setTestContext(c *gin.Context, userID, username, role, spaceID string) {
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("role", role)
	c.Set("space_id", spaceID)
}

func TestListWhiteboards(t *testing.T) {
	_, r, _, _, wb := setupTest(t)

	req := httptest.NewRequest("GET", "/api/v1/whiteboards", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["data"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 whiteboard, got %d", len(items))
	}
	item := items[0].(map[string]interface{})
	if item["id"] != wb.ID {
		t.Errorf("expected whiteboard id %s, got %s", wb.ID, item["id"])
	}
}

func TestCreateWhiteboard(t *testing.T) {
	_, r, _, _, _ := setupTest(t)

	body := map[string]interface{}{"name": "New Board"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/whiteboards", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	if data["name"] != "New Board" {
		t.Errorf("unexpected name: %v", data["name"])
	}
}

func TestArchiveWhiteboard(t *testing.T) {
	_, r, _, _, wb := setupTest(t)

	body := map[string]interface{}{"archived": true}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", fmt.Sprintf("/api/v1/whiteboards/%s", wb.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAndGetStroke(t *testing.T) {
	_, r, cfg, db, wb := setupTest(t)

	original := `{"points":[[0,0],[1,1]],"color":"#ff0000"}`
	body := map[string]interface{}{
		"tool":  "pen",
		"data":  original,
		"color": "#ff0000",
		"width": 2.0,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", wb.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify DB stores encrypted data
	var stroke model.WhiteboardStroke
	db.Last(&stroke)
	if stroke.WhiteboardID != wb.ID {
		t.Errorf("expected whiteboard_id %s, got %s", wb.ID, stroke.WhiteboardID)
	}
	if stroke.Data == original {
		t.Error("expected stored data to be encrypted")
	}
	decrypted, err := crypto.DecryptAES(stroke.Data, cfg.EncryptionKey)
	if err != nil || decrypted != original {
		t.Fatalf("decryption mismatch: %v", err)
	}

	// GET should return decrypted data
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", wb.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["data"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 stroke, got %d", len(items))
	}
	item := items[0].(map[string]interface{})
	if item["data"] != original {
		t.Errorf("expected decrypted data, got %v", item["data"])
	}
}

func TestClearStrokes(t *testing.T) {
	_, r, _, db, wb := setupTest(t)

	stroke := model.WhiteboardStroke{
		ID:           idgen.NextString(),
		WhiteboardID: wb.ID,
		UserID:       "user-1",
		AuthorName:   "test",
		Tool:         "pen",
		Data:         `{"points":[]}`,
		Encrypted:    false,
	}
	db.Create(&stroke)

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", wb.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&model.WhiteboardStroke{}).Where("whiteboard_id = ?", wb.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 strokes, got %d", count)
	}
}

func TestInvalidTool(t *testing.T) {
	_, r, _, _, wb := setupTest(t)

	body := map[string]interface{}{
		"tool":  "invalid_tool",
		"data":  `{"points":[[0,0]]}`,
		"color": "#ff0000",
		"width": 1.0,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", wb.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestConcurrentStrokes(t *testing.T) {
	_, r, _, db, wb := setupTest(t)

	var wg sync.WaitGroup
	errCh := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// data 必须是合法的 pen 结构（N3 校验后非法 JSON 会被 400 拒绝），
			// 本测试只关注并发正确性，与 TestCreateStrokeAllTools 使用同构数据。
			data := fmt.Sprintf(`{"points":[[0,0],[%d,1],[2,2]]}`, idx)
			body := map[string]interface{}{
				"tool":  "pen",
				"data":  data,
				"color": "#ff0000",
				"width": 2.0,
			}
			jsonBody, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", wb.ID), bytes.NewBuffer(jsonBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				errCh <- fmt.Errorf("stroke %d status %d", idx, w.Code)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	var errCount int
	for e := range errCh {
		t.Log(e)
		errCount++
	}
	if errCount > 0 {
		t.Errorf("got %d concurrent errors", errCount)
	}

	var count int64
	db.Model(&model.WhiteboardStroke{}).Where("whiteboard_id = ?", wb.ID).Count(&count)
	if count != 100 {
		t.Errorf("expected 100 strokes, got %d", count)
	}
}

func TestBroadcastWithHubDoesNotPanic(t *testing.T) {
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: idgen.NextString(), Name: "test-space", OwnerID: "user-1"}
	db.Create(space)
	cfg := &config.Config{
		JWTSecret:     "test-jwt-secret-for-whiteboard-encryption-32b",
		EncryptionKey: "test-encryption-key-for-whiteboard-32bytes!",
	}
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	go hub.Run()

	h := NewHandler(db, cfg, hub)
	svc := NewService(db)
	wb, _ := svc.Create(space.ID, "Hub Board", "user-1")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	wbGroup := api.Group("/whiteboards")
	wbGroup.POST("/:id/strokes", func(c *gin.Context) {
		setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
		h.CreateStroke(c)
	})

	body := map[string]interface{}{
		"tool":  "pen",
		"data":  `{"points":[[0,0]]}`,
		"color": "#00ff00",
		"width": 1.5,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", wb.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("panicked: %v", rec)
		}
	}()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUploadAndGetThumbnail(t *testing.T) {
	_, r, cfg, _, wb := setupTest(t)

	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	body := map[string]interface{}{"image": "data:image/png;base64," + b64}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/whiteboards/%s/thumbnail", wb.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	url, _ := data["url"].(string)
	if url == "" {
		t.Fatalf("expected thumbnail url, got empty")
	}

	// Verify file was written
	filePath := filepath.Join(cfg.LocalDataPath, "whiteboard", wb.ID+".png")
	defer os.RemoveAll(filepath.Join(cfg.LocalDataPath, "whiteboard"))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("expected thumbnail file to exist")
	}

	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/whiteboards/%s/thumbnail", wb.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for thumbnail get, got %d", w.Code)
	}
}

// postStroke 向测试路由提交一笔笔迹并返回响应码。
func postStroke(t *testing.T, r http.Handler, whiteboardID, payload string) int {
	t.Helper()
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/whiteboards/%s/strokes", whiteboardID), bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// N3 修复：客户端 8 工具中会产生笔迹的 7 种都必须被放行
// （旧白名单缺 circle/arrow/line/text，这 4 种笔迹曾被 400 静默丢弃）。
// 各工具的 data 结构与客户端 WhiteboardPanel.handleMouseUp 写入的逐字对应。
func TestCreateStrokeAllTools(t *testing.T) {
	_, r, _, _, wb := setupTest(t)

	cases := []struct {
		tool string
		data string
	}{
		{"pen", `{"points":[[0,0],[10,10]]}`},
		{"eraser", `{"points":[[5,5]]}`},
		{"rect", `{"startX":0,"startY":0,"endX":10,"endY":10}`},
		{"circle", `{"startX":0,"startY":0,"endX":10,"endY":10}`},
		{"arrow", `{"startX":0,"startY":0,"endX":10,"endY":20}`},
		{"line", `{"startX":0,"startY":0,"endX":10,"endY":20}`},
		{"text", `{"startX":3,"startY":4,"text":"你好白板"}`},
		// 遗留别名：旧客户端的 ellipse（字段结构与 circle 相同）仍需接受
		{"ellipse", `{"startX":0,"startY":0,"endX":8,"endY":8}`},
	}
	for _, tc := range cases {
		payload := fmt.Sprintf(`{"tool":%q,"data":%q,"color":"#123456","width":2.5}`, tc.tool, tc.data)
		if code := postStroke(t, r, wb.ID, payload); code != http.StatusOK {
			t.Errorf("tool %q: expected 200, got %d", tc.tool, code)
		}
	}
}

// N3 修复：按工具做最小字段校验——必需字段缺失/非法时返回 400，
// 不再落库为永远无法渲染的脏数据。
func TestCreateStrokeInvalidDataByTool(t *testing.T) {
	_, r, _, _, wb := setupTest(t)

	cases := []struct {
		name string
		tool string
		data string
	}{
		{"pen 缺 points", "pen", `{"color":"#000"}`},
		{"pen points 为空数组", "pen", `{"points":[]}`},
		{"pen points 元素不是坐标对", "pen", `{"points":[[1],["a","b"]]}`},
		{"line 缺 endX", "line", `{"startX":0,"startY":0,"endY":5}`},
		{"rect endY 非数值", "rect", `{"startX":0,"startY":0,"endX":5,"endY":"x"}`},
		{"text 缺 text 字段", "text", `{"startX":0,"startY":0}`},
		{"text 为空白字符串", "text", `{"startX":0,"startY":0,"text":"   "}`},
		{"data 不是 JSON 对象", "pen", `"just-a-string"`},
		{"data 为空", "circle", ``},
	}
	for _, tc := range cases {
		payload := fmt.Sprintf(`{"tool":%q,"data":%q,"color":"#123456","width":2}`, tc.tool, tc.data)
		if code := postStroke(t, r, wb.ID, payload); code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tc.name, code)
		}
	}
}

// S-3：单笔迹删除——删除一笔后其余笔迹保留，DB 行真实消失。
func TestDeleteStroke(t *testing.T) {
	_, r, _, db, wb := setupTest(t)

	strokes := []model.WhiteboardStroke{
		{ID: idgen.NextString(), WhiteboardID: wb.ID, UserID: "user-1", AuthorName: "test", Tool: "pen", Data: `{"points":[[0,0],[1,1]]}`},
		{ID: idgen.NextString(), WhiteboardID: wb.ID, UserID: "user-2", AuthorName: "other", Tool: "rect", Data: `{"startX":0,"startY":0,"endX":9,"endY":9}`},
	}
	for i := range strokes {
		if err := db.Create(&strokes[i]).Error; err != nil {
			t.Fatalf("create stroke %d: %v", i, err)
		}
	}

	// MEMBER（非 OWNER/ADMIN）删除他人笔迹：与笔迹提交同权限，应当放行
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/whiteboards/%s/strokes/%s", wb.ID, strokes[0].ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&model.WhiteboardStroke{}).Where("whiteboard_id = ?", wb.ID).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 remaining stroke, got %d", count)
	}
	var remaining model.WhiteboardStroke
	if err := db.Where("whiteboard_id = ?", wb.ID).First(&remaining).Error; err != nil {
		t.Fatalf("remaining stroke vanished: %v", err)
	}
	if remaining.ID != strokes[1].ID {
		t.Errorf("wrong stroke deleted: expected %s kept, got %s", strokes[1].ID, remaining.ID)
	}
}

// S-3：删除不存在的笔迹返回 404（WHITEBOARD_NOT_FOUND），而非静默成功。
func TestDeleteStrokeNotFound(t *testing.T) {
	_, r, _, _, wb := setupTest(t)

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/whiteboards/%s/strokes/no-such-stroke", wb.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// S-3：白板本身不存在时直接 404，且不产出任何笔迹行变化。
func TestDeleteStrokeWhiteboardNotFound(t *testing.T) {
	_, r, _, _, _ := setupTest(t)

	req := httptest.NewRequest("DELETE", "/api/v1/whiteboards/no-such-wb/strokes/some-stroke", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// S-3：带 Hub 时删除广播（whiteboard_stroke + payload.deleted）不 panic，
// 与 TestBroadcastWithHubDoesNotPanic 同一套路覆盖广播路径。
func TestDeleteStrokeBroadcastWithHubDoesNotPanic(t *testing.T) {
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: idgen.NextString(), Name: "test-space", OwnerID: "user-1"}
	db.Create(space)
	cfg := &config.Config{
		JWTSecret:     "test-jwt-secret-for-whiteboard-encryption-32b",
		EncryptionKey: "test-encryption-key-for-whiteboard-32bytes!",
	}
	hub := realtime.NewHub(testutil.TestLogger())
	go hub.Run()

	h := NewHandler(db, cfg, hub)
	stroke := model.WhiteboardStroke{
		ID:           idgen.NextString(),
		WhiteboardID: idgen.NextString(),
		UserID:       "user-1",
		AuthorName:   "test",
		Tool:         "pen",
		Data:         `{"points":[[0,0]]}`,
	}
	// 白板必须真实存在（handler 先校验白板存在性）
	wb := &model.Whiteboard{ID: stroke.WhiteboardID, SpaceID: space.ID, Name: "hub board", CreatedBy: "user-1"}
	if err := db.Create(wb).Error; err != nil {
		t.Fatalf("create whiteboard: %v", err)
	}
	if err := db.Create(&stroke).Error; err != nil {
		t.Fatalf("create stroke: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.DELETE("/whiteboards/:id/strokes/:strokeId", func(c *gin.Context) {
		setTestContext(c, "user-1", "test-user", "MEMBER", space.ID)
		h.DeleteStroke(c)
	})

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("panicked: %v", rec)
		}
	}()
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/whiteboards/%s/strokes/%s", wb.ID, stroke.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// N9 修复：白板列表事件的收件人解析——spaceMemberIDs 只返回该空间的成员，
// 其他空间/未加入的用户不得进入收件人集合。
func TestSpaceMemberIDsOnlyReturnsSpaceMembers(t *testing.T) {
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: idgen.NextString(), Name: "space-a", OwnerID: "user-1"}
	other := &model.Space{ID: idgen.NextString(), Name: "space-b", OwnerID: "user-3"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("create other space: %v", err)
	}
	members := []model.Membership{
		{ID: idgen.NextString(), UserID: "user-1", SpaceID: space.ID, Role: "OWNER"},
		{ID: idgen.NextString(), UserID: "user-2", SpaceID: space.ID, Role: "MEMBER"},
		{ID: idgen.NextString(), UserID: "user-3", SpaceID: other.ID, Role: "OWNER"},
	}
	for i := range members {
		if err := db.Create(&members[i]).Error; err != nil {
			t.Fatalf("create membership %d: %v", i, err)
		}
	}

	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)

	ids := h.spaceMemberIDs(space.ID)
	if len(ids) != 2 {
		t.Fatalf("expected 2 member ids, got %v", ids)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["user-1"] || !got["user-2"] {
		t.Errorf("expected user-1 and user-2, got %v", ids)
	}
	if got["user-3"] {
		t.Errorf("user-3 belongs to another space and must not be included: %v", ids)
	}

	// 异常输入 fail-closed：空空间 ID 返回 nil；不存在的空间返回空集
	if got2 := h.spaceMemberIDs(""); got2 != nil {
		t.Errorf("empty space id should return nil, got %v", got2)
	}
	if got3 := h.spaceMemberIDs("no-such-space"); len(got3) != 0 {
		t.Errorf("unknown space should return empty set, got %v", got3)
	}
}
