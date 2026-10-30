package message

// R-03（安全批次 task-17）附件下载权限回归测试：
//   - 私有频道无权限 → 403 CHANNEL_ACCESS_DENIED（正式下载与 ?thumb=1 同一受控路径，
//     先校验后读文件）；
//   - 有权限 → 200 返回文件内容 / 缩略图；
//   - 附件不存在 → 404 FILE_NOT_FOUND；
//   - 所属消息已软删 → 404 MESSAGE_NOT_FOUND；
//   - 历史无 message_id 附件 → fail-closed 403。
//
// 真 SQLite + httptest；archiver 注入真实 cloudfs service（非 mock）。

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/features/cloudfs"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

func seedAuthAttachment(t *testing.T, db *gorm.DB, id, messageID string) *model.MessageAttachment {
	t.Helper()
	att := &model.MessageAttachment{
		ID:        id,
		MessageID: messageID,
		Type:      "file",
		FileName:  "a.txt",
		MimeType:  "text/plain",
	}
	if err := db.Create(att).Error; err != nil {
		t.Fatalf("create attachment %s: %v", id, err)
	}
	return att
}

func TestAttachmentDownloadRequiresChannelAccess(t *testing.T) {
	t.Run("私有频道无权限 403（正式下载与 ?thumb=1 同一受控路径）", func(t *testing.T) {
		h, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r3-admin", "space-r3", "admin-only")
		msg := seedAuthMessage(t, h.db, "msg-r3-admin", "ch-r3-admin", "user-2")
		att := seedAuthAttachment(t, h.db, "att-r3-admin", msg.ID)

		for _, path := range []string{
			"/channel-files/" + att.ID,
			"/channel-files/" + att.ID + "?thumb=1",
		} {
			w := doJSON(r, http.MethodGet, path, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s: expected 403, got %d body=%s", path, w.Code, w.Body.String())
			}
			if code := respCode(t, w); code != string(errors.CHANNEL_ACCESS_DENIED) {
				t.Fatalf("%s: expected code CHANNEL_ACCESS_DENIED, got %s", path, code)
			}
		}
		if len(rec.channelCalls) != 0 {
			t.Fatalf("must not broadcast for attachment download, got %d calls", len(rec.channelCalls))
		}
	})

	t.Run("有权限 200 返回文件内容（含 ?thumb=1 缩略图）", func(t *testing.T) {
		h, r, _ := newResourceAuthRouter(t, middleware.RoleMember)
		ch := seedAuthChannel(t, h.db, "ch-r3-grant", "space-r3", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-r3", ChannelID: ch.ID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("channel role permission: %v", err)
		}
		// service 层二次防线要求 Space 成员
		if err := h.db.Create(&model.Membership{
			ID: "mb-r3", UserID: "user-1", SpaceID: "space-r3", Role: middleware.RoleMember,
		}).Error; err != nil {
			t.Fatalf("membership: %v", err)
		}
		msg := seedAuthMessage(t, h.db, "msg-r3-grant", ch.ID, "user-2")
		att := seedAuthAttachment(t, h.db, "att-r3-grant", msg.ID)

		dir := t.TempDir()
		h.service.SetLocalDataPath(dir)
		// service.DownloadAttachment 以 archiver 是否注入作为「附件存储已启用」的前置条件，
		// 这里注入真实 cloudfs service（非 mock），保证走完整下载链路。
		h.service.SetArchiver(cloudfs.NewService(h.db, &config.Config{LocalDataPath: dir}))
		cloudDir := filepath.Join(dir, "cloudfs")
		if err := os.MkdirAll(cloudDir, 0o755); err != nil {
			t.Fatalf("mkdir cloudfs: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cloudDir, "phys-r3.bin"), []byte("attachment-bytes"), 0o644); err != nil {
			t.Fatalf("write physical file: %v", err)
		}
		if err := h.db.Create(&model.SharedFileEntry{
			ID: "cf-r3", SpaceID: "space-r3", FolderID: "folder-r3",
			FileName: "a.txt", PhysicalName: "phys-r3.bin", MimeType: "text/plain",
		}).Error; err != nil {
			t.Fatalf("shared file entry: %v", err)
		}
		if err := h.db.Model(&model.MessageAttachment{}).Where("id = ?", att.ID).
			Update("cloud_file_id", "cf-r3").Error; err != nil {
			t.Fatalf("set cloud file id: %v", err)
		}

		w := doJSON(r, http.MethodGet, "/channel-files/"+att.ID, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		if got := w.Body.String(); got != "attachment-bytes" {
			t.Fatalf("body = %q, want %q", got, "attachment-bytes")
		}

		// 缩略图：写 _thumb.jpg 后 ?thumb=1 走同一校验并返回缩略图
		thumbDir := filepath.Join(dir, "channel-thumbs")
		if err := os.MkdirAll(thumbDir, 0o755); err != nil {
			t.Fatalf("mkdir thumbs: %v", err)
		}
		if err := os.WriteFile(filepath.Join(thumbDir, att.ID+"_thumb.jpg"), []byte("thumb-bytes"), 0o644); err != nil {
			t.Fatalf("write thumb: %v", err)
		}
		wThumb := doJSON(r, http.MethodGet, "/channel-files/"+att.ID+"?thumb=1", nil)
		if wThumb.Code != http.StatusOK {
			t.Fatalf("thumb: expected 200, got %d body=%s", wThumb.Code, wThumb.Body.String())
		}
		if ct := wThumb.Header().Get("Content-Type"); ct != "image/jpeg" {
			t.Fatalf("thumb content-type = %q, want image/jpeg", ct)
		}
	})

	t.Run("附件不存在 404 FILE_NOT_FOUND", func(t *testing.T) {
		_, r, _ := newResourceAuthRouter(t, middleware.RoleMember)
		w := doJSON(r, http.MethodGet, "/channel-files/att-missing", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.FILE_NOT_FOUND) {
			t.Fatalf("expected code FILE_NOT_FOUND, got %s", code)
		}
	})

	t.Run("所属消息已软删 404 MESSAGE_NOT_FOUND", func(t *testing.T) {
		h, r, _ := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r3-del", "space-r3", "public")
		msg := seedAuthMessage(t, h.db, "msg-r3-del", "ch-r3-del", "user-2")
		att := seedAuthAttachment(t, h.db, "att-r3-del", msg.ID)
		if err := h.db.Delete(&model.Message{}, "id = ?", msg.ID).Error; err != nil {
			t.Fatalf("soft delete message: %v", err)
		}

		w := doJSON(r, http.MethodGet, "/channel-files/"+att.ID, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.MESSAGE_NOT_FOUND) {
			t.Fatalf("expected code MESSAGE_NOT_FOUND, got %s", code)
		}
	})

	t.Run("历史无 message_id 附件 fail-closed 403", func(t *testing.T) {
		h, r, _ := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r3-legacy", "space-r3", "public")
		att := seedAuthAttachment(t, h.db, "att-r3-legacy", "")

		w := doJSON(r, http.MethodGet, "/channel-files/"+att.ID, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
	})
}
