package whiteboard

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// 白板笔迹工具枚举：与客户端 WhiteboardPanel 的 TOOLS 定义逐字对齐。
// select 仅作画布选择光标、不产生笔迹，服务端不接受；
// ellipse 为遗留别名（旧客户端使用），字段结构与 circle 相同。
const (
	toolPen     = "pen"
	toolEraser  = "eraser"
	toolRect    = "rect"
	toolCircle  = "circle"
	toolEllipse = "ellipse" // 遗留别名
	toolArrow   = "arrow"
	toolLine    = "line"
	toolText    = "text"
)

// Handler handles whiteboard HTTP requests
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new whiteboard handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{
		service: NewService(db),
		db:      db,
		cfg:     cfg,
		hub:     hub,
	}
}

// RegisterRoutes registers whiteboard routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	wb := r.Group("/whiteboards")
	wb.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		wb.GET("", h.List)
		wb.POST("", h.Create)
		wb.PATCH("/:id", h.Update)
		wb.DELETE("/:id", h.Delete)
		wb.GET("/:id/strokes", h.GetStrokes)
		wb.POST("/:id/strokes", h.CreateStroke)
		// S-3：单笔迹删除（选中后删除）。与下方 CLEAR 路由（整板清空）并存，
		// gin 对 /strokes 与 /strokes/:strokeId 两个静态/参数段可正常路由。
		wb.DELETE("/:id/strokes/:strokeId", h.DeleteStroke)
		wb.DELETE("/:id/strokes", h.ClearStrokes)
		wb.POST("/:id/thumbnail", h.UploadThumbnail)
		wb.GET("/:id/thumbnail", h.GetThumbnail)
	}
}

// spaceMemberIDs 查询空间全部成员的用户 ID（空间创建者在建空间时已写入
// Membership，因此包含 Owner）。查询失败返回 nil：调用方跳过广播，
// 宁可少一次实时通知也不把事件泄露给非成员（fail-closed，审计 N9）。
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

// broadcastToSpace 将白板列表类事件（created/archived/deleted）定向送达
// 空间全部在线成员，替代此前的全局 Broadcast（审计 N9：跨空间可见）。
// 客户端「白板列表实时更新」行为不变：在线成员仍即时收到事件，
// 离线成员下次进入白板页拉取列表时自然同步。
func (h *Handler) broadcastToSpace(spaceID string, data []byte) {
	if h.hub == nil {
		return
	}
	members := h.spaceMemberIDs(spaceID)
	if len(members) == 0 {
		return
	}
	h.hub.BroadcastToUsers(members, data)
}

// List returns all whiteboards in the current space.
func (h *Handler) List(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	includeArchived, _ := strconv.ParseBool(c.Query("archived"))
	list, err := h.service.List(spaceID, includeArchived)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, list)
}

// Create creates a new whiteboard.
func (h *Handler) Create(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	var body struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("name is required"))
		return
	}
	wb, err := h.service.Create(spaceID, body.Name, middleware.GetUserID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "whiteboard_created",
			"payload": map[string]interface{}{
				"whiteboard": wb,
			},
		})
		h.broadcastToSpace(spaceID, data)
	}
	errors.Success(c, wb)
}

// Update renames or archives/unarchives a whiteboard.
func (h *Handler) Update(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Name     *string `json:"name,omitempty"`
		Archived *bool   `json:"archived,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	wb, err := h.service.Update(id, middleware.GetUserID(c), middleware.GetRole(c), body.Name, body.Archived)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if h.hub != nil && body.Archived != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "whiteboard_archived",
			"payload": map[string]interface{}{
				"id":       id,
				"archived": *body.Archived,
			},
		})
		h.broadcastToSpace(wb.SpaceID, data)
	}
	errors.Success(c, wb)
}

// Delete removes a whiteboard and all its strokes.
func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("id")
	// 先取白板拿到归属空间，用于按空间成员定向广播（审计 N9）；
	// 不存在的白板直接 404，而不是像以前那样静默成功。
	wb, err := h.service.Get(id)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if err := h.service.Delete(id, middleware.GetRole(c)); err != nil {
		errors.JSONError(c, err)
		return
	}
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "whiteboard_deleted",
			"payload": map[string]interface{}{
				"id": id,
			},
		})
		h.broadcastToSpace(wb.SpaceID, data)
	}
	errors.Success(c, gin.H{"message": "deleted"})
}

// GetStrokes returns strokes for a whiteboard.
func (h *Handler) GetStrokes(c *gin.Context) {
	id := c.Param("id")
	strokes, err := h.service.GetStrokes(id, crypto.DecryptAES, h.cfg.EncryptionKey, h.cfg.JWTSecret)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, strokes)
}

// validateStrokeData 按工具对笔迹明文 data 做最小字段校验（在 AES 加密落库前执行）。
// 各工具必需字段与客户端 WhiteboardPanel.handleMouseUp 写入的结构一一对应：
// pen / eraser 写 {points: [[x,y], ...]}（至少 1 个坐标点）；
// rect / circle / ellipse / arrow / line 写 {startX, startY, endX, endY}（有限数值）；
// text 写 {startX, startY, text}（text 非空字符串）。
// 校验失败返回 400 级错误，杜绝「本地画了但不持久、不同步」的静默丢失（审计 N3）。
func validateStrokeData(tool, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.ErrBadRequest.WithDetails("stroke data is required")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return errors.ErrBadRequest.WithDetails("stroke data must be a JSON object")
	}
	switch tool {
	case toolPen, toolEraser:
		pointsRaw, ok := data["points"]
		if !ok {
			return errors.ErrBadRequest.WithDetails(tool + " stroke requires points")
		}
		var points [][2]float64
		if err := json.Unmarshal(pointsRaw, &points); err != nil || len(points) == 0 {
			return errors.ErrBadRequest.WithDetails("points must be a non-empty array of [x,y] pairs")
		}
	case toolRect, toolCircle, toolEllipse, toolArrow, toolLine:
		for _, field := range []string{"startX", "startY", "endX", "endY"} {
			v, ok := data[field]
			if !ok {
				return errors.ErrBadRequest.WithDetails(tool + " stroke requires " + field)
			}
			var num float64
			if err := json.Unmarshal(v, &num); err != nil {
				return errors.ErrBadRequest.WithDetails(field + " must be a finite number")
			}
		}
	case toolText:
		for _, field := range []string{"startX", "startY"} {
			v, ok := data[field]
			if !ok {
				return errors.ErrBadRequest.WithDetails(tool + " stroke requires " + field)
			}
			var num float64
			if err := json.Unmarshal(v, &num); err != nil {
				return errors.ErrBadRequest.WithDetails(field + " must be a finite number")
			}
		}
		var text string
		textRaw, ok := data["text"]
		if !ok {
			return errors.ErrBadRequest.WithDetails("text stroke requires text")
		}
		if err := json.Unmarshal(textRaw, &text); err != nil || strings.TrimSpace(text) == "" {
			return errors.ErrBadRequest.WithDetails("text must be a non-empty string")
		}
	default:
		return errors.ErrBadRequest.WithDetails("invalid tool, must be one of: pen, eraser, rect, circle, arrow, line, text")
	}
	return nil
}

// CreateStroke creates a new stroke on a whiteboard.
func (h *Handler) CreateStroke(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Type  string  `json:"type"` // legacy field
		Tool  string  `json:"tool"`
		Data  string  `json:"data"`
		Color string  `json:"color"`
		Width float64 `json:"width"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	tool := body.Tool
	if tool == "" {
		tool = body.Type
	}
	if tool == "" {
		tool = toolPen
	}
	// 按工具做最小字段校验（加密前明文），未知工具拒绝（审计 N3：
	// 旧白名单缺 circle/arrow/line/text，导致这 4 种工具笔迹被 400 丢弃）。
	if err := validateStrokeData(tool, body.Data); err != nil {
		errors.JSONError(c, err)
		return
	}

	// username 已退役：作者名不再写入（AuthorName 为遗留快照，读取时按 UserID 解析当前 displayName）
	stroke, err := h.service.CreateStroke(id, middleware.GetUserID(c), "", tool, body.Data, body.Color, body.Width, crypto.EncryptAES, h.cfg.EncryptionKey)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "whiteboard_stroke",
			"payload": map[string]interface{}{
				"whiteboardId": id,
				"strokeId":     stroke.ID,
				"userId":       stroke.UserID,
				"authorName":   stroke.AuthorName,
				"tool":         stroke.Tool,
				"color":        stroke.Color,
				"width":        stroke.Width,
				"data":         body.Data,
				"createdAt":    stroke.CreatedAt,
			},
		})
		h.hub.BroadcastToWhiteboard(id, data)
	}

	errors.Success(c, stroke)
}

// DeleteStroke deletes a single stroke from a whiteboard (S-3 选中删除).
func (h *Handler) DeleteStroke(c *gin.Context) {
	id := c.Param("id")
	strokeID := c.Param("strokeId")
	// 白板必须存在：不存在的白板直接 404，避免给空白板伪造删除事件。
	if _, err := h.service.Get(id); err != nil {
		errors.JSONError(c, err)
		return
	}
	// 权限与笔迹提交（CreateStroke）同层：通过 AuthRequired 的认证用户即可操作，
	// 当前白板没有只读成员概念（写笔迹同样不受角色限制），保持同一准入口径。
	if err := h.service.DeleteStroke(id, strokeID); err != nil {
		errors.JSONError(c, err)
		return
	}
	if h.hub != nil {
		// 复用 whiteboard_stroke 通道广播删除：渲染进程的 WS→window 事件桥接
		// 集中在客户端 App.tsx 且仅按 type 分发白板事件（S-3 期间该文件为并发
		// 禁区，无法新增独立事件分支），payload.deleted=true 让协作端区分
		// 「新增笔迹」与「删除笔迹」。旧客户端收到后会误当新增，但其渲染层对
		// 空 data 不绘制、不可见，重进白板后自然消失，无可见副作用。
		data, _ := json.Marshal(map[string]interface{}{
			"type": "whiteboard_stroke",
			"payload": map[string]interface{}{
				"whiteboardId": id,
				"strokeId":     strokeID,
				"deleted":      true,
				"deletedBy":    middleware.GetUserID(c),
			},
		})
		h.hub.BroadcastToWhiteboard(id, data)
	}
	errors.Success(c, gin.H{"message": "deleted", "strokeId": strokeID})
}

// UploadThumbnail receives a Base64 PNG thumbnail and stores it.
func (h *Handler) UploadThumbnail(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Image string `json:"image" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("image is required"))
		return
	}
	const prefix = "data:image/png;base64,"
	b64 := body.Image
	if strings.HasPrefix(b64, prefix) {
		b64 = b64[len(prefix):]
	}
	imgData, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid base64 image"))
		return
	}
	if len(imgData) == 0 {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("empty image"))
		return
	}

	storageDir := filepath.Join(h.cfg.LocalDataPath, "whiteboard")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to create storage directory"))
		return
	}
	fileName := id + ".png"
	filePath := filepath.Join(storageDir, fileName)
	if err := os.WriteFile(filePath, imgData, 0644); err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to save thumbnail"))
		return
	}

	thumbnailURL := "/storage/whiteboard/" + fileName
	if err := h.service.UpdateThumbnail(id, thumbnailURL); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	errors.Success(c, gin.H{"url": thumbnailURL})
}

// GetThumbnail serves the whiteboard thumbnail file.
func (h *Handler) GetThumbnail(c *gin.Context) {
	id := c.Param("id")
	filePath := filepath.Join(h.cfg.LocalDataPath, "whiteboard", id+".png")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.Status(http.StatusNotFound)
		return
	}
	c.File(filePath)
}

// ClearStrokes clears all strokes for a whiteboard.
func (h *Handler) ClearStrokes(c *gin.Context) {
	id := c.Param("id")
	wb, err := h.service.Get(id)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if !h.service.CanManage(wb, middleware.GetUserID(c), middleware.GetRole(c)) {
		errors.JSONError(c, errors.New(errors.WHITEBOARD_CLEAR_NOT_ALLOWED, "no permission to clear whiteboard"))
		return
	}
	if err := h.service.ClearStrokes(id); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "whiteboard_clear",
			"payload": map[string]interface{}{
				"whiteboardId": id,
				"clearedBy":    middleware.GetUserID(c),
			},
		})
		h.hub.BroadcastToWhiteboard(id, data)
	}
	errors.Success(c, gin.H{"message": "cleared"})
}
