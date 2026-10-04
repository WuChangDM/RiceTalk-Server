package emoji

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

// ---- handler 集成：CRUD 全链 ----

func TestHandler_CreateAndListAndDelete_FullChain(t *testing.T) {
	e := setup(t)

	// 空间管理员上传（默认身份 admin-usr）
	w := e.postEmoji(t, "admin-usr", "rice_face", validPNG(64))
	if w.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeResp(t, w)
	data, _ := resp["data"].(map[string]interface{})
	if data == nil || data["name"] != "rice_face" || data["spaceId"] != e.spaceA {
		t.Fatalf("unexpected create payload: %v", resp)
	}
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatal("missing emoji id")
	}
	if url, _ := data["url"].(string); url != "/api/v1/emojis/"+id+"/file" {
		t.Fatalf("unexpected url %v", data["url"])
	}

	// 空间成员可见列表
	req := httptest.NewRequest("GET", "/api/v1/emojis", nil)
	req.Header.Set("X-Test-Space", e.spaceA)
	w = httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", w.Code)
	}
	list := decodeResp(t, w)
	items, _ := list["data"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 emoji, got %d", len(items))
	}

	// 文件访问：Content-Type / Cache-Control / 字节一致
	req = httptest.NewRequest("GET", "/api/v1/emojis/"+id+"/file", nil)
	w = httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("file: expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %s, want image/png", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %s", cc)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != string(validPNG(64)) {
		t.Fatal("file content mismatch")
	}

	// 创建者本人（空间管理员）可删除自己的表情
	w = e.deleteEmoji(t, "admin-usr", id)
	if w.Code != http.StatusOK {
		t.Fatalf("creator delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 删除后 404 + 列表为空
	req = httptest.NewRequest("GET", "/api/v1/emojis/"+id+"/file", nil)
	w = httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("file after delete: expected 404, got %d", w.Code)
	}
	if code := respCode(t, w); code != "EMOJI_NOT_FOUND" {
		t.Fatalf("error code = %s, want EMOJI_NOT_FOUND", code)
	}
}

func TestHandler_Create_PermissionDenied(t *testing.T) {
	e := setup(t)
	// 普通成员（非管理员）上传 → 403
	w := e.postEmoji(t, "member-usr", "nope", validPNG(64))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if code := respCode(t, w); code != "EMOJI_PERMISSION_DENIED" {
		t.Fatalf("code = %s", code)
	}
	// 非成员（不属于任何空间）上传 → 403
	w = e.postEmoji(t, "ghost-usr", "nope", validPNG(64))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	// 普通成员列表可见（成员可读），但非成员不可读
	req := httptest.NewRequest("GET", "/api/v1/emojis", nil)
	req.Header.Set("X-Test-User", "ghost-usr")
	w = httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("ghost list: expected 403, got %d", w.Code)
	}
}

func TestHandler_Create_ValidationErrors(t *testing.T) {
	e := setup(t)
	// name 冲突 → 409
	if w := e.postEmoji(t, "admin-usr", "dup", validPNG(64)); w.Code != http.StatusOK {
		t.Fatalf("seed: %d", w.Code)
	}
	w := e.postEmoji(t, "admin-usr", "dup", validPNG(64))
	if w.Code != http.StatusConflict || respCode(t, w) != "EMOJI_NAME_TAKEN" {
		t.Fatalf("expected 409 EMOJI_NAME_TAKEN, got %d %s", w.Code, w.Body.String())
	}
	// name 非法 → 400
	w = e.postEmoji(t, "admin-usr", "BAD NAME", validPNG(64))
	if w.Code != http.StatusBadRequest || respCode(t, w) != "EMOJI_NAME_INVALID" {
		t.Fatalf("expected 400 EMOJI_NAME_INVALID, got %d %s", w.Code, w.Body.String())
	}
	// 大小超限 → 400
	w = e.postEmoji(t, "admin-usr", "big", validPNG(MaxEmojiFileSize+1))
	if w.Code != http.StatusBadRequest || respCode(t, w) != "EMOJI_FILE_TOO_LARGE" {
		t.Fatalf("expected 400 EMOJI_FILE_TOO_LARGE, got %d %s", w.Code, w.Body.String())
	}
	// 类型非法 → 400
	w = e.postEmoji(t, "admin-usr", "jpg", jpegBytes(256))
	if w.Code != http.StatusBadRequest || respCode(t, w) != "EMOJI_FILE_INVALID" {
		t.Fatalf("expected 400 EMOJI_FILE_INVALID, got %d %s", w.Code, w.Body.String())
	}
	// 缺 file 字段 → 400
	body := "name=nofile"
	req := httptest.NewRequest("POST", "/api/v1/emojis", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w2 := httptest.NewRecorder()
	e.r.ServeHTTP(w2, req)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("missing file: expected 400, got %d", w2.Code)
	}
}

func TestHandler_Delete_PermissionMatrix(t *testing.T) {
	e := setup(t)
	seed := func(name string) string {
		w := e.postEmoji(t, "admin-usr", name, validPNG(64))
		if w.Code != http.StatusOK {
			t.Fatalf("seed %s: %d %s", name, w.Code, w.Body.String())
		}
		return decodeResp(t, w)["data"].(map[string]interface{})["id"].(string)
	}

	// 普通成员删除他人表情 → 403
	id1 := seed("face_a")
	w := e.deleteEmoji(t, "member-usr", id1)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member delete other's: expected 403, got %d", w.Code)
	}

	// 创建者分支：普通成员删除「自己名下」的表情 → 200。
	// 普通成员无上传权（EMOJI_PERMISSION_DENIED），此分支覆盖的是
	// 管理员上传后被降权、或空间管理员易主后的历史归属场景，
	// 因此直接以 service 层插入归属行再走 handler 删除。
	own := &model.ServerEmoji{
		ID:        idgen.GenerateID(idgen.PrefixEmoji),
		SpaceID:   e.spaceA,
		Name:      "mine_face",
		FileID:    "emojis/xx/yy.png",
		CreatorID: "member-usr",
	}
	if err := e.svc.db.Create(own).Error; err != nil {
		t.Fatalf("seed own row: %v", err)
	}
	w = e.deleteEmoji(t, "member-usr", own.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("creator delete own: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 空间管理员删除他人表情 → 200
	w = e.deleteEmoji(t, "admin-usr", id1)
	if w.Code != http.StatusOK {
		t.Fatalf("admin delete other's: expected 200, got %d", w.Code)
	}
	// 不存在的表情 → 404
	w = e.deleteEmoji(t, "admin-usr", "emoji_missing")
	if w.Code != http.StatusNotFound || respCode(t, w) != "EMOJI_NOT_FOUND" {
		t.Fatalf("expected 404 EMOJI_NOT_FOUND, got %d %s", w.Code, w.Body.String())
	}
}

// TestHandler_Delete_NonMemberCreatorDenied 覆盖审核 R-3：创建者被移出空间后
// （此处以「仅空间 B 成员」的 member-b 模拟 spaceA 的非成员归属——权限面与
// 「曾是成员后被移除」等价：对 spaceA 无 membership）不再保留删除权，与
// CanView 拒其读取对等。
func TestHandler_Delete_NonMemberCreatorDenied(t *testing.T) {
	e := setup(t)
	own := &model.ServerEmoji{
		ID:        idgen.GenerateID(idgen.PrefixEmoji),
		SpaceID:   e.spaceA,
		Name:      "gone_face",
		FileID:    "emojis/xx/gone.png",
		CreatorID: "member-b", // member-b 不在 spaceA：非成员创建者
	}
	if err := e.svc.db.Create(own).Error; err != nil {
		t.Fatalf("seed own row: %v", err)
	}
	w := e.deleteEmoji(t, "member-b", own.ID)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-member creator delete: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if code := respCode(t, w); code != "EMOJI_PERMISSION_DENIED" {
		t.Fatalf("code = %s, want EMOJI_PERMISSION_DENIED", code)
	}
	// 表情未被删除（拒绝是硬拒绝，不产生半删除状态）
	var count int64
	e.svc.db.Model(&model.ServerEmoji{}).Where("id = ?", own.ID).Count(&count)
	if count != 1 {
		t.Fatal("emoji row must survive a denied delete")
	}
}

// TestHandler_GlobalAdmin_ManagesPlatformEmojis 覆盖审核 R-4：全局 ADMIN
// （非空间成员）与全局 OWNER 同权管理平台资源；全局 MEMBER 仍 403。
func TestHandler_GlobalAdmin_ManagesPlatformEmojis(t *testing.T) {
	e := setup(t)
	// 全局 ADMIN 上传 → 200
	w := e.postEmoji(t, "gadmin-usr", "plat_face", validPNG(64))
	if w.Code != http.StatusOK {
		t.Fatalf("global admin create: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	id := decodeResp(t, w)["data"].(map[string]interface{})["id"].(string)
	// 全局 ADMIN 删除他人表情 → 200
	w = e.deleteEmoji(t, "gadmin-usr", id)
	if w.Code != http.StatusOK {
		t.Fatalf("global admin delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// 全局 MEMBER（无任何空间身份）上传 → 403
	w = e.postEmoji(t, "gmember-usr", "nope", validPNG(64))
	if w.Code != http.StatusForbidden || respCode(t, w) != "EMOJI_PERMISSION_DENIED" {
		t.Fatalf("global member create: expected 403, got %d %s", w.Code, w.Body.String())
	}
}

func TestHandler_File_CrossSpaceDenied(t *testing.T) {
	e := setup(t)
	// 空间 B 成员上传
	svcEmoji, err := e.svc.Create(e.spaceB, "b_face", "member-b", validPNG(64))
	if err != nil {
		t.Fatalf("create in space B: %v", err)
	}
	// 空间 A 成员（测试路由固定 space A 上下文）访问空间 B 表情文件 → 403
	req := httptest.NewRequest("GET", "/api/v1/emojis/"+svcEmoji.ID+"/file", nil)
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-space file: expected 403, got %d", w.Code)
	}
	if code := respCode(t, w); code != "EMOJI_PERMISSION_DENIED" {
		t.Fatalf("code = %s", code)
	}
}
