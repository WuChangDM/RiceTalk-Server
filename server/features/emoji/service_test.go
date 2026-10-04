package emoji

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/storage"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// ---- 测试素材：最小魔数合法的图片字节 ----

func validPNG(n int) []byte {
	b := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
	return append(b, bytes.Repeat([]byte{0}, n-len(b))...)
}

func validGIF(n int) []byte {
	b := []byte("GIF89a")
	return append(b, bytes.Repeat([]byte{0}, n-len(b))...)
}

func validWebP(n int) []byte {
	b := []byte("RIFF")
	b = append(b, 0x00, 0x00, 0x00, 0x00) // 占位 size
	b = append(b, []byte("WEBP")...)
	return append(b, bytes.Repeat([]byte{0}, n-len(b))...)
}

func jpegBytes(n int) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	return append(b, bytes.Repeat([]byte{0}, n-len(b))...)
}

// ---- 测试装配 ----

type testEnv struct {
	h      *Handler
	r      *gin.Engine
	svc    *Service
	spaceA string
	spaceB string
}

// setup 构造 handler + gin engine。与 whiteboard handler_test 相同的模式：
// 不挂 AuthRequired，由包装函数直接注入用户上下文。
func setup(t *testing.T) *testEnv {
	t.Helper()
	db := testutil.MustSetupTestDB()
	st, err := storage.NewLocalStorage(t.TempDir(), "http://test/api/v1/files")
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	cfg := &config.Config{JWTSecret: "test-jwt-secret-for-emoji-tests-only-32b"}
	h := NewHandler(db, st, cfg, nil)

	// 空间 A：owner-usr(空间OWNER) / admin-usr(空间ADMIN) / member-usr(普通成员) /
	// creator-usr(表情创建者,普通成员)；空间 B：member-b(仅空间 B 成员)。
	spaceA, spaceB := "space_a", "space_b"
	for _, m := range []model.Membership{
		{ID: idgen.NextString(), UserID: "owner-usr", SpaceID: spaceA, Role: "OWNER"},
		{ID: idgen.NextString(), UserID: "admin-usr", SpaceID: spaceA, Role: "ADMIN"},
		{ID: idgen.NextString(), UserID: "member-usr", SpaceID: spaceA, Role: "MEMBER"},
		{ID: idgen.NextString(), UserID: "creator-usr", SpaceID: spaceA, Role: "MEMBER"},
		{ID: idgen.NextString(), UserID: "member-b", SpaceID: spaceB, Role: "MEMBER"},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}
	// 全局角色用户（不属于任何空间）：isGlobalOwner 的判定对象（R-4 放宽 OWNER||ADMIN）。
	// gmember-usr 为全局 MEMBER，用于验证放宽不外溢到普通全局用户。
	for _, u := range []model.User{
		{ID: "gowner-usr", Username: "gowner", Email: "gowner@test.local", PasswordHash: "x", Role: "OWNER"},
		{ID: "gadmin-usr", Username: "gadmin", Email: "gadmin@test.local", PasswordHash: "x", Role: "ADMIN"},
		{ID: "gmember-usr", Username: "gmember", Email: "gmember@test.local", PasswordHash: "x", Role: "MEMBER"},
	} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	{
		api.GET("/emojis", func(c *gin.Context) {
			setTestContext(c, currentUser(c), "MEMBER", queryOr(c, spaceA))
			h.List(c)
		})
		api.POST("/emojis", func(c *gin.Context) {
			setTestContext(c, currentUser(c), currentRole(c), spaceA)
			h.Create(c)
		})
		api.DELETE("/emojis/:id", func(c *gin.Context) {
			setTestContext(c, currentUser(c), currentRole(c), spaceA)
			h.Delete(c)
		})
		api.GET("/emojis/:id/file", func(c *gin.Context) {
			setTestContext(c, "member-usr", "MEMBER", spaceA)
			h.GetFile(c)
		})
	}
	return &testEnv{h: h, r: r, svc: NewService(db, st), spaceA: spaceA, spaceB: spaceB}
}

func setTestContext(c *gin.Context, userID, role, spaceID string) {
	c.Set("user_id", userID)
	c.Set("role", role)
	c.Set("space_id", spaceID)
}

// currentUser/currentRole/currentSpace 允许单测通过 query 头部切换身份：
// X-Test-User / X-Test-Role。gin 的 query 读取在 ServeHTTP 前仍可用。
func currentUser(c *gin.Context) string {
	if u := c.Request.Header.Get("X-Test-User"); u != "" {
		return u
	}
	return "admin-usr"
}

func currentRole(c *gin.Context) string {
	if r := c.Request.Header.Get("X-Test-Role"); r != "" {
		return r
	}
	return "ADMIN"
}

func queryOr(c *gin.Context, fallback string) string {
	if s := c.Request.Header.Get("X-Test-Space"); s != "" {
		return s
	}
	return fallback
}

// postEmoji 以 multipart 表单调用 POST /emojis。
func (e *testEnv) postEmoji(t *testing.T, user, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("name", name)
	part, err := writer.CreateFormFile("file", name+".png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/emojis", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Test-User", user)
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func (e *testEnv) deleteEmoji(t *testing.T, user, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/emojis/%s", id), nil)
	req.Header.Set("X-Test-User", user)
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return resp
}

func respCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	resp := decodeResp(t, w)
	code, _ := resp["code"].(string)
	return code
}

// ---- service 层：嗅探与校验 ----

func TestSniffImage(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", validPNG(64), "image/png"},
		{"gif87a", append([]byte("GIF87a"), bytes.Repeat([]byte{0}, 58)...), "image/gif"},
		{"gif89a", validGIF(64), "image/gif"},
		{"webp", validWebP(64), "image/webp"},
		{"jpeg rejected", jpegBytes(64), ""},
		{"riff non-webp rejected", append([]byte("RIFF"), append([]byte("WAVE"), bytes.Repeat([]byte{0}, 56)...)...), ""},
		{"empty rejected", nil, ""},
		{"short rejected", []byte("PNG"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mime, _, ok := sniffImage(tc.data)
			if !ok && tc.want != "" {
				t.Fatalf("sniffImage(%s) rejected, want %s", tc.name, tc.want)
			}
			if ok && mime != tc.want {
				t.Fatalf("sniffImage(%s) = %s, want %s", tc.name, mime, tc.want)
			}
		})
	}
}

func TestCreate_Success_AllTypes(t *testing.T) {
	e := setup(t)
	for i, data := range [][]byte{validPNG(128), validGIF(128), validWebP(128)} {
		emoji, err := e.svc.Create(e.spaceA, fmt.Sprintf("face_%d", i), "admin-usr", data)
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		if emoji.ID == "" || emoji.SpaceID != e.spaceA || emoji.Name != fmt.Sprintf("face_%d", i) {
			t.Fatalf("unexpected emoji: %+v", emoji)
		}
		if !e.svc.storage.Exists(emoji.FileID) {
			t.Fatalf("storage object %s missing", emoji.FileID)
		}
	}
}

func TestCreate_NameInvalid(t *testing.T) {
	e := setup(t)
	for _, name := range []string{"", "a", "UPPER", "has-dash", "has space", "emoji😀", strings.Repeat("x", 33), "café"} {
		_, err := e.svc.Create(e.spaceA, name, "admin-usr", validPNG(64))
		if err == nil {
			t.Fatalf("name %q should be rejected", name)
		}
		if code := err.(*errors.AppError).Code; code != "EMOJI_NAME_INVALID" {
			t.Fatalf("name %q: got code %s, want EMOJI_NAME_INVALID", name, code)
		}
	}
}

func TestCreate_NameTaken(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.Create(e.spaceA, "rice", "admin-usr", validPNG(64)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := e.svc.Create(e.spaceA, "rice", "admin-usr", validPNG(64))
	if err == nil {
		t.Fatal("duplicate name should be rejected")
	}
	if code := err.(*errors.AppError).Code; code != "EMOJI_NAME_TAKEN" {
		t.Fatalf("got code %s, want EMOJI_NAME_TAKEN", code)
	}
	// 同名在不同空间互不影响
	if _, err := e.svc.Create(e.spaceB, "rice", "member-b", validPNG(64)); err != nil {
		t.Fatalf("same name in another space should pass: %v", err)
	}
}

func TestCreate_FileTooLarge(t *testing.T) {
	e := setup(t)
	_, err := e.svc.Create(e.spaceA, "big", "admin-usr", bytes.Repeat([]byte{0x89, 'P'}, MaxEmojiFileSize))
	if err == nil {
		t.Fatal("oversize file should be rejected")
	}
	if code := err.(*errors.AppError).Code; code != "EMOJI_FILE_TOO_LARGE" {
		t.Fatalf("got code %s, want EMOJI_FILE_TOO_LARGE", code)
	}
}

func TestCreate_FileTypeRejected(t *testing.T) {
	e := setup(t)
	_, err := e.svc.Create(e.spaceA, "jpg", "admin-usr", jpegBytes(256))
	if err == nil {
		t.Fatal("jpeg content should be rejected")
	}
	if code := err.(*errors.AppError).Code; code != "EMOJI_FILE_INVALID" {
		t.Fatalf("got code %s, want EMOJI_FILE_INVALID", code)
	}
	// RIFF 但非 WEBP（如 WAV）同样拒绝——精确嗅探的回归用例
	_, err = e.svc.Create(e.spaceA, "wav", "admin-usr", append([]byte("RIFFxxxxWAVE"), bytes.Repeat([]byte{0}, 64)...))
	if err == nil || err.(*errors.AppError).Code != "EMOJI_FILE_INVALID" {
		t.Fatalf("wav content should be rejected with EMOJI_FILE_INVALID, got %v", err)
	}
}

func TestCreate_LimitReached(t *testing.T) {
	e := setup(t)
	for i := 0; i < MaxEmojisPerSpace; i++ {
		if _, err := e.svc.Create(e.spaceA, fmt.Sprintf("e%02d", i), "admin-usr", validPNG(32)); err != nil {
			t.Fatalf("seed #%d: %v", i, err)
		}
	}
	_, err := e.svc.Create(e.spaceA, "overflow", "admin-usr", validPNG(32))
	if err == nil {
		t.Fatal("limit should be enforced")
	}
	if code := err.(*errors.AppError).Code; code != "EMOJI_LIMIT_REACHED" {
		t.Fatalf("got code %s, want EMOJI_LIMIT_REACHED", code)
	}
	// 上限按空间隔离：空间 B 不受影响
	if _, err := e.svc.Create(e.spaceB, "fine", "member-b", validPNG(32)); err != nil {
		t.Fatalf("space B should be independent: %v", err)
	}
}

func TestDelete_RemovesRowAndObject(t *testing.T) {
	e := setup(t)
	emoji, err := e.svc.Create(e.spaceA, "bye", "admin-usr", validPNG(64))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !e.svc.storage.Exists(emoji.FileID) {
		t.Fatal("object should exist before delete")
	}
	if err := e.svc.Delete(emoji.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if e.svc.storage.Exists(emoji.FileID) {
		t.Fatal("storage object should be removed")
	}
	err = e.svc.Delete(emoji.ID)
	if err == nil || err.(*errors.AppError).Code != "EMOJI_NOT_FOUND" {
		t.Fatalf("second delete should be EMOJI_NOT_FOUND, got %v", err)
	}
}

// ---- 权限口径 ----

func TestCanManage_Roles(t *testing.T) {
	e := setup(t)
	cases := []struct {
		user string
		want bool
	}{
		{"owner-usr", true},   // 空间 OWNER
		{"admin-usr", true},   // 空间 ADMIN
		{"member-usr", false}, // 普通成员
		{"ghost-usr", false},  // 非成员（且非全局管理角色）
	}
	for _, tc := range cases {
		if got := e.svc.CanManage(e.spaceA, tc.user); got != tc.want {
			t.Fatalf("CanManage(%s) = %v, want %v", tc.user, got, tc.want)
		}
	}
}

// TestCanManage_GlobalRoles 验证 R-4 放宽口径：isGlobalOwner 认 OWNER||ADMIN，
// 与客户端 canManageEmojis（owner||admin）对齐；全局 MEMBER 不放行。
func TestCanManage_GlobalRoles(t *testing.T) {
	e := setup(t)
	if !e.svc.CanManage(e.spaceA, "gowner-usr") {
		t.Fatal("global OWNER should manage any space's emojis")
	}
	if !e.svc.CanManage(e.spaceA, "gadmin-usr") {
		t.Fatal("global ADMIN should manage any space's emojis (R-4)")
	}
	if e.svc.CanManage(e.spaceA, "gmember-usr") {
		t.Fatal("global MEMBER should not manage")
	}
	// 放宽同步作用于 CanView：全局 Owner/Admin 可读任意空间表情列表
	if !e.svc.CanView(e.spaceA, "gadmin-usr") {
		t.Fatal("global ADMIN should view any space's emojis")
	}
	if e.svc.CanView(e.spaceA, "gmember-usr") {
		t.Fatal("global MEMBER should not view")
	}
}

func TestCanView_Roles(t *testing.T) {
	e := setup(t)
	if !e.svc.CanView(e.spaceA, "member-usr") {
		t.Fatal("space member should view")
	}
	if e.svc.CanView(e.spaceA, "member-b") {
		t.Fatal("member of another space should not view")
	}
	if e.svc.CanView(e.spaceA, "ghost-usr") {
		t.Fatal("non-member should not view")
	}
}

func TestList_SpaceScoped(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.Create(e.spaceA, "a_only", "admin-usr", validPNG(32)); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := e.svc.Create(e.spaceB, "b_only", "member-b", validPNG(32)); err != nil {
		t.Fatalf("create b: %v", err)
	}
	listA, err := e.svc.List(e.spaceA)
	if err != nil || len(listA) != 1 || listA[0].Name != "a_only" {
		t.Fatalf("space A list = %+v err %v, want only a_only", listA, err)
	}
	listB, err := e.svc.List(e.spaceB)
	if err != nil || len(listB) != 1 || listB[0].Name != "b_only" {
		t.Fatalf("space B list = %+v err %v, want only b_only", listB, err)
	}
}

// ---- 广播 payload 契约 ----

func TestBuildEmojiEventPayload(t *testing.T) {
	emoji := &model.ServerEmoji{ID: "emoji_1", SpaceID: "space_a", Name: "rice", CreatorID: "usr_c"}

	etype, created := buildEmojiEvent("emoji_created", emoji)
	if etype != "emoji_created" {
		t.Fatalf("event type = %s", etype)
	}
	if created["id"] != "emoji_1" || created["name"] != "rice" || created["spaceId"] != "space_a" || created["creatorId"] != "usr_c" {
		t.Fatalf("created payload = %v", created)
	}

	etype, deleted := buildEmojiEvent("emoji_deleted", emoji)
	if etype != "emoji_deleted" {
		t.Fatalf("event type = %s", etype)
	}
	if _, has := deleted["creatorId"]; has {
		t.Fatal("deleted payload should not carry creatorId")
	}
	if deleted["id"] != "emoji_1" || deleted["name"] != "rice" {
		t.Fatalf("deleted payload = %v", deleted)
	}
}

func TestBroadcastWithHubDoesNotPanic(t *testing.T) {
	db := testutil.MustSetupTestDB()
	st, err := storage.NewLocalStorage(t.TempDir(), "http://test/api/v1/files")
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	go hub.Run()

	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: "u1", SpaceID: "sp1", Role: "ADMIN"}).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	cfg := &config.Config{JWTSecret: "test-jwt-secret-for-emoji-tests-only-32b"}
	h := NewHandler(db, st, cfg, hub)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/emojis", func(c *gin.Context) {
		setTestContext(c, "u1", "ADMIN", "sp1")
		h.Create(c)
	})

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("name", "hub_face")
	part, _ := writer.CreateFormFile("file", "hub_face.png")
	_, _ = part.Write(validPNG(64))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/emojis", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
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
