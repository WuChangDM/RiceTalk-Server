// Package emoji implements the server-side surface of B7「自定义服务器表情·轻量版」
// (DES-20261002-01 §4.7).
//
// 范围（做）：空间维度自定义表情——空间管理员上传 PNG/GIF/WebP（单个 ≤128KB，
// 每空间 ≤50 个），消息中 `:name:` 的渲染在客户端完成，服务端只存原文。
// 不做：商店/付费/全局市场/表情包整包。
package emoji

import (
	"bytes"
	"io"
	"regexp"
	"strings"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/storage"
)

const (
	// MaxEmojiFileSize 单个表情文件大小上限（128KB）。
	MaxEmojiFileSize = 128 * 1024
	// MaxEmojisPerSpace 每个空间的自定义表情数量上限。
	MaxEmojisPerSpace = 50
	// storagePrefix 表情对象在 object storage 中的前缀。
	storagePrefix = "emojis"
)

// emojiNamePattern 表情名称：[a-z0-9_] 2-32 位。消息中用 `:name:` 引用，
// 服务端存原文不做渲染，因此名称集必须收敛以避免误伤普通冒号文本。
var emojiNamePattern = regexp.MustCompile(`^[a-z0-9_]{2,32}$`)

// sniffImage 按内容嗅探 PNG/GIF/WebP 魔数，返回规范 MIME 与扩展名。
// 刻意不使用 storage.ValidateFileType：它对 WebP 只认 RIFF 头（与 WAV/AVI
// 共用魔数），会把音频/视频文件误判为 WebP 放行；这里对 WebP 额外校验
// offset 8-11 的 "WEBP" 四字节。
func sniffImage(data []byte) (mimeType, ext string, ok bool) {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}) {
		return "image/png", ".png", true
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		// GIF87a/GIF89a 均为动画兼容格式，原样存储保留动画帧。
		return "image/gif", ".gif", true
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp", ".webp", true
	}
	return "", "", false
}

// Service carries the emoji business logic.
type Service struct {
	db      *gorm.DB
	storage storage.Storage
}

// NewService creates a new emoji service.
func NewService(db *gorm.DB, st storage.Storage) *Service {
	return &Service{db: db, storage: st}
}

// isSpaceAdminTx 判定用户在空间内的角色是否为 OWNER/ADMIN（空间管理员）。
// 读取走传入的 db（池），与 cloudfs.isSpaceAdminTx 同名同义；本服务不使用
// 事务写路径，故不提供事务变体。
func (s *Service) isSpaceAdminTx(db *gorm.DB, spaceID, userID string) bool {
	var membership model.Membership
	if err := db.Where("space_id = ? AND user_id = ?", spaceID, userID).First(&membership).Error; err != nil {
		return false
	}
	return membership.Role == "OWNER" || membership.Role == "ADMIN"
}

// CanView 判定用户能否读取空间表情列表：空间成员，或全局 Owner/Admin（部署管理
// 角色，对齐 ws_callbacks.go AuthorizeWhiteboard 的放行先例）。
func (s *Service) CanView(spaceID, userID string) bool {
	if spaceID == "" || userID == "" {
		return false
	}
	var count int64
	if err := s.db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id = ?", spaceID, userID).
		Count(&count).Error; err != nil {
		return false // fail-closed
	}
	if count > 0 {
		return true
	}
	return s.isGlobalOwner(userID)
}

// CanManage 判定用户能否上传/删除空间表情：全局 Owner/Admin（isGlobalOwner），
// 或空间内 Membership.Role ∈ {OWNER, ADMIN}（DES §4.7「Owner/空间管理员」）。
func (s *Service) CanManage(spaceID, userID string) bool {
	if spaceID == "" || userID == "" {
		return false
	}
	if s.isGlobalOwner(userID) {
		return true
	}
	return s.isSpaceAdminTx(s.db, spaceID, userID)
}

// isGlobalOwner 判定用户是否为全局管理角色（User.Role ∈ {OWNER, ADMIN}）。
// 全局 ADMIN 与 OWNER 同权管理平台级资源（表情跨空间管理/读取），与客户端
// canManageEmojis 的 owner||admin 口径对齐（审核 R-4 决策：服务端放宽）。
// 全局 MEMBER 不放行。
func (s *Service) isGlobalOwner(userID string) bool {
	var u model.User
	if err := s.db.Select("id, role").First(&u, "id = ?", userID).Error; err != nil {
		return false
	}
	return u.Role == "OWNER" || u.Role == "ADMIN"
}

// List returns all emojis of the given space, ordered by creation time.
func (s *Service) List(spaceID string) ([]model.ServerEmoji, error) {
	var list []model.ServerEmoji
	if err := s.db.Where("space_id = ?", spaceID).
		Order("created_at ASC").
		Find(&list).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return list, nil
}

// Create validates and stores a new space emoji. data is the full file
// content (the caller has already capped the read at MaxEmojiFileSize+1).
func (s *Service) Create(spaceID, name, creatorID string, data []byte) (*model.ServerEmoji, error) {
	name = strings.TrimSpace(name)
	if !emojiNamePattern.MatchString(name) {
		return nil, errors.New(errors.EMOJI_NAME_INVALID, "emoji name must match [a-z0-9_]{2,32}")
	}
	if len(data) == 0 {
		return nil, errors.New(errors.EMOJI_FILE_INVALID, "emoji file is empty")
	}
	if len(data) > MaxEmojiFileSize {
		return nil, errors.New(errors.EMOJI_FILE_TOO_LARGE, "emoji file exceeds 128KB")
	}
	mimeType, ext, ok := sniffImage(data)
	if !ok {
		return nil, errors.New(errors.EMOJI_FILE_INVALID, "only PNG/GIF/WebP images are allowed")
	}

	// 每空间数量上限（≤50）。计数与插入之间存在竞态窗口，但并发上传同一
	// 空间在自托管场景可忽略；唯一索引兜底 name 冲突。
	var count int64
	if err := s.db.Model(&model.ServerEmoji{}).Where("space_id = ?", spaceID).Count(&count).Error; err != nil {
		return nil, errors.ErrInternal
	}
	if count >= MaxEmojisPerSpace {
		return nil, errors.New(errors.EMOJI_LIMIT_REACHED, "space emoji limit reached (max 50)")
	}

	// 空间内名称唯一：先查给 409 语义化错误，唯一索引兜底并发。
	var existing model.ServerEmoji
	if err := s.db.Where("space_id = ? AND name = ?", spaceID, name).First(&existing).Error; err == nil {
		return nil, errors.New(errors.EMOJI_NAME_TAKEN, "emoji name already taken in this space")
	}

	// 存储对象 key：emojis/ 前缀 + 哈希路径，扩展名按嗅探结果规范化
	//（不信任用户文件名）。随机串必须在扩展名之前——GenerateKey 用
	// filepath.Ext 取扩展名，拼在后面会被一并吸进扩展名段。
	// 文件内容不可变，删除前同一 key 字节流恒定。
	key := storage.GenerateKey(storagePrefix, name+"_"+idgen.NextString()+ext)
	if _, err := s.storage.Put(key, bytes.NewReader(data), int64(len(data)), mimeType); err != nil {
		return nil, errors.ErrInternal
	}

	emoji := &model.ServerEmoji{
		ID:        idgen.GenerateID(idgen.PrefixEmoji),
		SpaceID:   spaceID,
		Name:      name,
		FileID:    key,
		CreatorID: creatorID,
	}
	if err := s.db.Create(emoji).Error; err != nil {
		_ = s.storage.Delete(key) // 行写入失败不留孤儿对象
		if isUniqueViolation(err) {
			return nil, errors.New(errors.EMOJI_NAME_TAKEN, "emoji name already taken in this space")
		}
		return nil, errors.ErrInternal
	}
	return emoji, nil
}

// Delete removes the emoji row and its storage object. Permission
// (creator / space admin / global owner) is enforced by the caller.
func (s *Service) Delete(id string) error {
	var emoji model.ServerEmoji
	if err := s.db.First(&emoji, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.EMOJI_NOT_FOUND, "emoji not found")
		}
		return errors.ErrInternal
	}
	if err := s.db.Delete(&emoji).Error; err != nil {
		return errors.ErrInternal
	}
	// 行已删除，存储对象清理失败只留下孤儿文件，不影响接口正确性。
	_ = s.storage.Delete(emoji.FileID)
	return nil
}

// GetFile loads the emoji row and its stored object. The caller owns the
// returned reader.
func (s *Service) GetFile(id string) (*model.ServerEmoji, io.ReadCloser, int64, string, error) {
	var emoji model.ServerEmoji
	if err := s.db.First(&emoji, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, 0, "", errors.New(errors.EMOJI_NOT_FOUND, "emoji not found")
		}
		return nil, nil, 0, "", errors.ErrInternal
	}
	reader, size, contentType, err := s.storage.Get(emoji.FileID)
	if err != nil {
		return nil, nil, 0, "", errors.New(errors.EMOJI_NOT_FOUND, "emoji file missing")
	}
	return &emoji, reader, size, contentType, nil
}

// isUniqueViolation reports whether err is a unique-constraint violation
// (best-effort across SQLite/PG drivers; the pre-check in Create is the
// primary path, this is just the concurrent fallback).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "unique constraint")
}
