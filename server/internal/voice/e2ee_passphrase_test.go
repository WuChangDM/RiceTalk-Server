package voice

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// DES-20261006-01 §二（E2EE 二期·房间口令/零知识密钥协商）服务端单测。
// 覆盖四个关键用例：设置 / 清除 / 权限 / 口令房不回 key，
// 以及口令房 token metadata 门控扩展（e2ee_enabled 与全局开关解耦）。

// makeMagicBlob 造一个结构合法的 magic blob（base64(12B salt || 33B 密文)）。
// 内容是随机字节即可：服务端只做 base64/长度校验，不解释密文。
func makeMagicBlob(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 45) // salt12 + GCM 密文(17B 明文 + 16B tag)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand failed: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// setupPassphraseService 建库 + service + 空间/成员/房间，返回常用句柄。
// 返回值：路由、handler、service（直断言用）、测试房间、成员 userID。
func setupPassphraseService(t *testing.T) (*gin.Engine, *Handler, *Service, *model.VoiceRoom, string) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.LiveKitE2EEEnabled = false // 口令房语义必须独立于全局开关
	h := NewHandler(db, cfg, nil)

	spaceID := idgen.NextString()
	userID := idgen.NextString()
	createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleMember)

	room := &model.VoiceRoom{
		ID:      idgen.NextString(),
		SpaceID: spaceID,
		Quality: "standard",
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	// 路由：注入认证上下文 + 真实 RequireAdmin（与生产防线一致）。
	// GET key 走 MEMBER（成员可取）；PUT passphrase 挂 OWNER（中间件要求 ADMIN+）。
	router := gin.New()
	inject := func(role string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Set("user_id", userID)
			c.Set("username", "passphrase-user")
			c.Set("role", role)
			c.Next()
		}
	}
	router.GET("/api/v1/voice/e2ee/key", inject(middleware.RoleMember), h.GetE2EEKey)
	router.PUT("/api/v1/voice/e2ee/passphrase", inject(middleware.RoleOwner), middleware.RequireAdmin(), h.SetE2EEPassphrase)
	return router, h, h.service, room, userID
}

// doPassphraseJSON 发起 JSON 请求并返回 recorder。
func doPassphraseJSON(t *testing.T, router *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body failed: %v", err)
		}
		req, _ = http.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// passphraseResponse 解出标准 data 包装响应。
func passphraseResponse(t *testing.T, w *httptest.ResponseRecorder) (code string, data map[string]interface{}) {
	t.Helper()
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v, body: %s", err, w.Body.String())
	}
	return resp.Code, resp.Data
}

// 用例 1：设置口令 → 落库（enabled+magic）且 GET key 进入 passphrase 模式。
func TestSetE2EEPassphrase(t *testing.T) {
	router, _, _, room, _ := setupPassphraseService(t)
	blob := makeMagicBlob(t)

	w := doPassphraseJSON(t, router, "PUT", "/api/v1/voice/e2ee/passphrase",
		map[string]interface{}{"roomId": room.ID, "magic": blob})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	code, data := passphraseResponse(t, w)
	if code != "OK" {
		t.Fatalf("expected OK, got %s", code)
	}
	if data["passphraseEnabled"] != true {
		t.Errorf("expected passphraseEnabled=true, got %v", data["passphraseEnabled"])
	}

	// 落库断言：enabled=true、magic 原样保存（服务端不解码/改写密文）
	// 注：直接读库需要 db 句柄，这里经 GET key 间接断言（见用例 4），
	// 并用非法 blob 验证校验逻辑真的在拦。
	t.Run("invalid blobs rejected", func(t *testing.T) {
		router2, _, _, room2, _ := setupPassphraseService(t)
		cases := map[string]string{
			"not base64":      "!!!not-base64!!!",
			"too short":       base64.StdEncoding.EncodeToString(make([]byte, 8)),
			"empty after enc": "",
		}
		for name, blob := range cases {
			w := doPassphraseJSON(t, router2, "PUT", "/api/v1/voice/e2ee/passphrase",
				map[string]interface{}{"roomId": room2.ID, "magic": blob})
			if name == "empty after enc" {
				// 空串 = 清除语义，应 200
				if w.Code != http.StatusOK {
					t.Errorf("[%s] expected 200 (clear semantics), got %d", name, w.Code)
				}
				continue
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("[%s] expected 400, got %d, body: %s", name, w.Code, w.Body.String())
			}
		}
		// 超长（>4KB）必须被拒
		huge := strings.Repeat("A", e2eeMagicMaxBase64Len+8)
		w := doPassphraseJSON(t, router2, "PUT", "/api/v1/voice/e2ee/passphrase",
			map[string]interface{}{"roomId": room2.ID, "magic": huge})
		if w.Code != http.StatusBadRequest {
			t.Errorf("[too long] expected 400, got %d", w.Code)
		}
	})
}

// 用例 2：清除口令 → 退回一期 server 模式（GET key 回 key）。
func TestClearE2EEPassphrase(t *testing.T) {
	router, _, _, room, _ := setupPassphraseService(t)
	blob := makeMagicBlob(t)

	// 先设置
	w := doPassphraseJSON(t, router, "PUT", "/api/v1/voice/e2ee/passphrase",
		map[string]interface{}{"roomId": room.ID, "magic": blob})
	if w.Code != http.StatusOK {
		t.Fatalf("setup: expected 200, got %d", w.Code)
	}

	// null 清除
	w = doPassphraseJSON(t, router, "PUT", "/api/v1/voice/e2ee/passphrase",
		map[string]interface{}{"roomId": room.ID, "magic": nil})
	if w.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	_, data := passphraseResponse(t, w)
	if data["passphraseEnabled"] != false {
		t.Errorf("expected passphraseEnabled=false after clear, got %v", data["passphraseEnabled"])
	}

	// 清除后 GET key 应回 server 模式（一期兼容：无 key 记录则现生成）
	w = doPassphraseJSON(t, router, "GET", "/api/v1/voice/e2ee/key?roomId="+room.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get key: expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	code, data := passphraseResponse(t, w)
	if code != "OK" || data["mode"] != E2EEModeServer {
		t.Fatalf("expected mode=%s after clear, got code=%s data=%v", E2EEModeServer, code, data)
	}
	if key, _ := data["key"].(string); key == "" {
		t.Error("expected non-empty key in server mode after clear")
	}
}

// 用例 3：权限——MEMBER 触碰 PUT passphrase 必须 403，ADMIN/OWNER 放行。
func TestSetE2EEPassphrasePermission(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	h := NewHandler(db, cfg, nil)

	spaceID := idgen.NextString()
	memberID := idgen.NextString()
	createSpaceAndMembership(t, db, spaceID, memberID, middleware.RoleMember)
	room := &model.VoiceRoom{ID: idgen.NextString(), SpaceID: spaceID, Quality: "standard"}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	newRouter := func(role string) *gin.Engine {
		r := gin.New()
		r.PUT("/api/v1/voice/e2ee/passphrase", func(c *gin.Context) {
			c.Set("user_id", memberID)
			c.Set("username", "perm-user")
			c.Set("role", role)
			c.Next()
		}, middleware.RequireAdmin(), h.SetE2EEPassphrase)
		return r
	}

	blob := makeMagicBlob(t)
	body := map[string]interface{}{"roomId": room.ID, "magic": blob}

	// MEMBER：403，且房间状态未被改动
	w := doPassphraseJSON(t, newRouter(middleware.RoleMember), "PUT", "/api/v1/voice/e2ee/passphrase", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member: expected 403, got %d, body: %s", w.Code, w.Body.String())
	}
	var stored model.VoiceRoom
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil {
		t.Fatalf("reload room failed: %v", err)
	}
	if stored.E2EEPassphraseEnabled || stored.E2EEMagic != "" {
		t.Error("member request must not modify passphrase state")
	}

	// ADMIN：放行
	w = doPassphraseJSON(t, newRouter(middleware.RoleAdmin), "PUT", "/api/v1/voice/e2ee/passphrase", body)
	if w.Code != http.StatusOK {
		t.Fatalf("admin: expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// OWNER：放行
	w = doPassphraseJSON(t, newRouter(middleware.RoleOwner), "PUT", "/api/v1/voice/e2ee/passphrase", body)
	if w.Code != http.StatusOK {
		t.Fatalf("owner: expected 200, got %d", w.Code)
	}
}

// 用例 4：口令房 GET key 只回 magic，绝不回 key（零知识边界）。
func TestGetE2EEKeyPassphraseRoomNeverReturnsKey(t *testing.T) {
	router, _, svc, room, userID := setupPassphraseService(t)
	blob := makeMagicBlob(t)
	if err := svc.SetE2EEPassphrase(room.ID, &blob); err != nil {
		t.Fatalf("set passphrase failed: %v", err)
	}

	// handler 层 JSON 断言：mode=passphrase、magic 原样、无 key 字段
	w := doPassphraseJSON(t, router, "GET", "/api/v1/voice/e2ee/key?roomId="+room.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	code, data := passphraseResponse(t, w)
	if code != "OK" {
		t.Fatalf("expected OK, got %s", code)
	}
	if data["mode"] != E2EEModePassphrase {
		t.Fatalf("expected mode=%s, got %v", E2EEModePassphrase, data["mode"])
	}
	if data["magic"] != blob {
		t.Errorf("expected magic blob echoed verbatim, got %v", data["magic"])
	}
	if _, hasKey := data["key"]; hasKey {
		t.Error("passphrase room must never return a key")
	}

	// 服务层直断言（与 handler 序列化无关）
	info, err := svc.GetE2EEKey(room.ID, userID)
	if err != nil {
		t.Fatalf("service GetE2EEKey failed: %v", err)
	}
	if info.Mode != E2EEModePassphrase || info.Magic != blob || info.Key != "" {
		t.Errorf("unexpected service result: %+v", info)
	}

	// 一期回归保护：非口令房仍是 server 模式且回 key
	other := &model.VoiceRoom{ID: idgen.NextString(), SpaceID: room.SpaceID, Quality: "standard"}
	if err := svc.db.Create(other).Error; err != nil {
		t.Fatalf("create control room failed: %v", err)
	}
	info2, err := svc.GetE2EEKey(other.ID, userID)
	if err != nil {
		t.Fatalf("control room GetE2EEKey failed: %v", err)
	}
	if info2.Mode != E2EEModeServer || info2.Key == "" {
		t.Errorf("expected server mode with key for non-passphrase room, got %+v", info2)
	}
}

// 门控扩展：全局开关关闭的口令房，token metadata 也必须写 e2ee_enabled=true，
// 且房间落库的 E2EEEnabled 标记同步为 true（录音守卫等据此拦截）。
func TestGenerateTokenPassphraseRoomWritesE2EEMetadata(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.LiveKitE2EEEnabled = false
	svc := NewService(db, cfg, nil)

	spaceID := idgen.NextString()
	userID := idgen.NextString()
	createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleOwner)

	blob := makeMagicBlob(t)
	room := &model.VoiceRoom{
		ID:                    idgen.NextString(),
		SpaceID:               spaceID,
		Quality:               "standard",
		E2EEPassphraseEnabled: true,
		E2EEMagic:             blob,
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	token, err := svc.GenerateToken(userID, "tester", middleware.RoleOwner, room.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(token, ".") {
		t.Fatal("expected a JWT-shaped token")
	}

	var stored model.VoiceRoom
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil {
		t.Fatalf("reload room failed: %v", err)
	}
	if !stored.E2EEEnabled {
		t.Error("passphrase room must be marked E2EEEnabled=true after token generation")
	}

	// metadata 断言：解 JWT payload（不验签，与客户端 decodeJwtPayload 同口径）
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3-part JWT, got %d", len(parts))
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	var claims struct {
		Metadata string `json:"metadata"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		t.Fatalf("parse claims failed: %v", err)
	}
	var metadataMap map[string]string
	if err := json.Unmarshal([]byte(claims.Metadata), &metadataMap); err != nil {
		t.Fatalf("parse metadata failed: %v", err)
	}
	if metadataMap["e2ee_enabled"] != "true" {
		t.Errorf("expected e2ee_enabled=true in metadata, got %v", metadataMap)
	}

	// 反向对照：清除口令后（模拟）回到全局关闭语义
	room.E2EEPassphraseEnabled = false
	room.E2EEMagic = ""
	if err := db.Save(room).Error; err != nil {
		t.Fatalf("clear passphrase failed: %v", err)
	}
	if _, err := svc.GenerateToken(userID, "tester", middleware.RoleOwner, room.ID); err != nil {
		t.Fatalf("regen token failed: %v", err)
	}
	var reloaded model.VoiceRoom
	if err := db.First(&reloaded, "id = ?", room.ID).Error; err != nil {
		t.Fatalf("reload room failed: %v", err)
	}
	if reloaded.E2EEEnabled {
		t.Error("E2EEEnabled must fall back to global switch (false) after passphrase cleared")
	}
}

// 口令房同样禁止录音（录音守卫扩展）。
func TestStartRecordingRejectedInPassphraseRoom(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, config.DefaultConfig(), nil)

	room := &model.VoiceRoom{
		ID:                    idgen.NextString(),
		SpaceID:               idgen.NextString(),
		E2EEPassphraseEnabled: true,
		E2EEMagic:             makeMagicBlob(t),
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	_, err := svc.StartRecording(idgen.NextString(), room.ID)
	if err == nil {
		t.Fatal("expected StartRecording to be rejected in passphrase room")
	}
	appErr, ok := err.(*errors.AppError)
	if !ok || appErr.Code != errors.VOICE_E2EE_RECORDING_UNSUPPORTED {
		t.Fatalf("expected VOICE_E2EE_RECORDING_UNSUPPORTED, got %v", err)
	}
}
