package emoji

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/storage"
	"ridgericetalk/middleware"
)

// Handler handles emoji HTTP requests (B7 轻量版，DES-20261002-01 §4.7).
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new emoji handler.
func NewHandler(db *gorm.DB, st storage.Storage, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{
		service: NewService(db, st),
		db:      db,
		cfg:     cfg,
		hub:     hub,
	}
}

// RegisterRoutes registers emoji routes under the given API group.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/emojis")
	g.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.GET("/:id/file", h.GetFile)
		g.DELETE("/:id", h.Delete)
	}
}

// spaceMemberIDs 查询空间全部成员的用户 ID。查询失败返回 nil：调用方跳过
// 广播，宁可少一次实时通知也不把事件泄露给非成员（fail-closed，审计 N9）。
func (h *Handler) spaceMemberIDs(spaceID string) []string {
	if spaceID == "" || h.db == nil {
		return nil
	}
	var ids []string
	if err := h.db.Model(&model.Membership{}).Where("space_id = ?", spaceID).Pluck("user_id", &ids).Error; err != nil {
		return nil
	}
	return ids
}

// broadcastToSpace 将表情事件（emoji_created/emoji_deleted）定向送达空间
// 全部在线成员（N9 先例：spaceMemberIDs + BroadcastToUsers，替代全局广播）。
// 客户端收到事件后刷新 picker 与 `:name:` 渲染表；离线成员下次拉取列表时
// 自然同步。
func (h *Handler) broadcastToSpace(spaceID string, eventType string, payload map[string]interface{}) {
	if h.hub == nil {
		return
	}
	members := h.spaceMemberIDs(spaceID)
	if len(members) == 0 {
		return
	}
	data, err := json.Marshal(map[string]interface{}{
		"type":    eventType,
		"payload": payload,
	})
	if err != nil {
		return
	}
	h.hub.BroadcastToUsers(members, data)
}

// buildEmojiEvent 构造表情事件的 payload（广播契约，客户端按此刷新）。
func buildEmojiEvent(eventType string, emoji *model.ServerEmoji) (string, map[string]interface{}) {
	payload := map[string]interface{}{
		"id":      emoji.ID,
		"name":    emoji.Name,
		"spaceId": emoji.SpaceID,
	}
	if eventType == "emoji_created" {
		payload["creatorId"] = emoji.CreatorID
	}
	return eventType, payload
}

// requireSpaceIDMultipart 为 multipart 请求解析空间 ID：JWT claim →
// query → 表单字段。RequireSpaceID 只认 JSON body，multipart 场景单独处理。
func requireSpaceIDMultipart(c *gin.Context) (string, error) {
	if sid := middleware.GetSpaceID(c); sid != "" {
		return sid, nil
	}
	if sid := c.Query("spaceId"); sid != "" {
		return sid, nil
	}
	if sid := c.PostForm("spaceId"); sid != "" {
		return sid, nil
	}
	return "", errors.New(errors.SYSTEM_BAD_REQUEST, "spaceId is required")
}

// List returns all emojis of the current space.
func (h *Handler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if !h.service.CanView(spaceID, userID) {
		errors.JSONError(c, errors.New(errors.EMOJI_PERMISSION_DENIED, "no permission to view emojis"))
		return
	}
	list, err := h.service.List(spaceID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, e := range list {
		items = append(items, emojiItem(&e))
	}
	errors.Success(c, items)
}

// Create uploads a new space emoji (multipart: name + file [+ spaceId]).
// 仅全局 OWNER / 空间 OWNER / 空间管理员可调用。
func (h *Handler) Create(c *gin.Context) {
	userID := middleware.GetUserID(c)
	spaceID, err := requireSpaceIDMultipart(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if !h.service.CanManage(spaceID, userID) {
		errors.JSONError(c, errors.New(errors.EMOJI_PERMISSION_DENIED, "only space admins can upload emojis"))
		return
	}
	name := c.PostForm("name")
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "file required"))
		return
	}
	defer file.Close()

	// 先做声明大小检查避免把超大文件读进内存；实际大小以读到的字节为准
	//（Create 内再校验一次，防声明值造假）。多读 1 字节用于判定超限。
	if header.Size > int64(MaxEmojiFileSize) {
		errors.JSONError(c, errors.New(errors.EMOJI_FILE_TOO_LARGE, "emoji file exceeds 128KB"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxEmojiFileSize+1))
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	emoji, err := h.service.Create(spaceID, name, userID, data)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	eventType, payload := buildEmojiEvent("emoji_created", emoji)
	h.broadcastToSpace(spaceID, eventType, payload)
	errors.Success(c, emojiItem(emoji))
}

// Delete removes an emoji. 删除权 =（仍是空间成员 && 创建者本人）或管理权
// （空间 Owner/Admin 或全局 Owner/Admin）。创建者被移出空间后不再保留删除权
// （CanView 已拒其读取，读写权限对等——审核 R-3）。
func (h *Handler) Delete(c *gin.Context) {
	userID := middleware.GetUserID(c)
	id := c.Param("id")

	var emoji model.ServerEmoji
	if err := h.db.First(&emoji, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.EMOJI_NOT_FOUND, "emoji not found"))
		} else {
			errors.JSONError(c, errors.ErrInternal)
		}
		return
	}
	isCreatorMember := emoji.CreatorID == userID && h.service.CanView(emoji.SpaceID, userID)
	if !isCreatorMember && !h.service.CanManage(emoji.SpaceID, userID) {
		errors.JSONError(c, errors.New(errors.EMOJI_PERMISSION_DENIED, "only the creator or space admins can delete this emoji"))
		return
	}
	if err := h.service.Delete(id); err != nil {
		errors.JSONError(c, err)
		return
	}
	eventType, payload := buildEmojiEvent("emoji_deleted", &emoji)
	h.broadcastToSpace(emoji.SpaceID, eventType, payload)
	errors.Success(c, gin.H{"deleted": true, "id": emoji.ID})
}

// GetFile serves the emoji image with immutable caching. GIF 动画帧原样
// 透传（内容不可变，浏览器可无限期缓存）。
func (h *Handler) GetFile(c *gin.Context) {
	userID := middleware.GetUserID(c)
	id := c.Param("id")

	emoji, reader, size, contentType, err := h.service.GetFile(id)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	defer reader.Close()
	if !h.service.CanView(emoji.SpaceID, userID) {
		errors.JSONError(c, errors.New(errors.EMOJI_PERMISSION_DENIED, "no permission to view this emoji"))
		return
	}

	// Content-Type 按上传时的封闭扩展名集（png/gif/webp）规范确定：
	// storage.Get 走 mime.TypeByExtension，其结果依赖系统 mime 表，
	// 缺映射时会退化为 application/octet-stream（Windows 实测），
	// 因此这里以上传校验过的类型为准。
	if ct := contentTypeByExt(emoji.FileID); ct != "" {
		contentType = ct
	}

	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	c.Status(http.StatusOK)
	io.Copy(c.Writer, reader)
}

// contentTypeByExt 返回表情对象 key 的规范 MIME；非 emoji 扩展名返回空串。
func contentTypeByExt(key string) string {
	switch {
	case strings.HasSuffix(key, ".png"):
		return "image/png"
	case strings.HasSuffix(key, ".gif"):
		return "image/gif"
	case strings.HasSuffix(key, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}

// emojiItem 组装列表/创建响应条目：url 指向鉴权文件接口（相对路径，由
// 客户端按部署地址补全）。
func emojiItem(e *model.ServerEmoji) gin.H {
	return gin.H{
		"id":        e.ID,
		"name":      e.Name,
		"spaceId":   e.SpaceID,
		"creatorId": e.CreatorID,
		"url":       fmt.Sprintf("/api/v1/emojis/%s/file", e.ID),
		"createdAt": e.CreatedAt,
	}
}
