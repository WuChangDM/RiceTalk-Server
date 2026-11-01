package notifications

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/middleware"
)

// Handler 提供通知中心的 HTTP 接口（1.9.4 task-118）。
//
// 路由（挂在 /api/v1 与 /api 两个组下，由 registerAPIRoutes 负责）：
//   - GET  /notifications?unreadOnly=&limit=&cursor=  拉取通知列表（含未读数与下一页游标）
//   - POST /notifications/:id/read                    单条标记已读（幂等）
//   - POST /notifications/read-all                    全部标记已读（幂等）
//
// 鉴权：整个路由组挂 middleware.AuthRequired，所有接口都要求登录；读取与写入
// 一律以登录用户为准，不接受客户端传 userId（防越权读他人通知）。
type Handler struct {
	db  *gorm.DB
	cfg *config.Config
	svc *Service
}

// NewHandler 构造通知处理器。
func NewHandler(db *gorm.DB, cfg *config.Config) *Handler {
	return &Handler{db: db, cfg: cfg, svc: NewService(db)}
}

// RegisterRoutes 注册通知相关路由（需由调用方给出 /api/v1 级别的分组）。
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/notifications")
	g.Use(middleware.AuthRequired(h.cfg, h.db))

	g.GET("", h.List)
	g.POST("/read-all", h.MarkAllRead)
	g.POST("/:id/read", h.MarkRead)
}

// List 返回当前用户的通知列表。
func (h *Handler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		errors.JSONError(c, errors.ErrUnauthorized)
		return
	}

	res, err := h.svc.List(userID, ListOptions{
		UnreadOnly: ParseBoolQuery(c.Query("unreadOnly")),
		Limit:      ParseLimit(c.Query("limit")),
		Cursor:     c.Query("cursor"),
	})
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, res)
}

// MarkRead 把单条通知标为已读（重复调用幂等，已读时间不覆盖）。
func (h *Handler) MarkRead(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		errors.JSONError(c, errors.ErrUnauthorized)
		return
	}

	n, err := h.svc.MarkRead(userID, c.Param("id"))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "ok", "id": n.ID, "readAt": n.ReadAt})
}

// MarkAllRead 把当前用户的全部未读通知标为已读。
func (h *Handler) MarkAllRead(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		errors.JSONError(c, errors.ErrUnauthorized)
		return
	}

	updated, err := h.svc.MarkAllRead(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "ok", "updated": updated})
}
