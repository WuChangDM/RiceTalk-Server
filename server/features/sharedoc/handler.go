package sharedoc

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// maxDocContentLen limits the size of a single shared document update to prevent
// excessive memory usage and slow diff/version operations.
const maxDocContentLen = 1024 * 1024 // 1 MB

// Handler handles shared document HTTP requests
type Handler struct {
	db        *gorm.DB
	cfg       *config.Config
	hub       *realtime.Hub
	editors   map[string]map[string]time.Time // M23: docId → userId → joinedAt
	userEdits map[string]map[string]bool      // M23: userId → docId → true
	mu        sync.RWMutex
}

// NewHandler creates a new sharedoc handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	h := &Handler{
		db:        db,
		cfg:       cfg,
		hub:       hub,
		editors:   make(map[string]map[string]time.Time),
		userEdits: make(map[string]map[string]bool),
	}
	return h
}

// RegisterRoutes registers sharedoc routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	sd := r.Group("/sharedoc")
	sd.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		sd.GET("/list", h.List)
		sd.POST("/create", h.Create)
		sd.PATCH("/:id", h.Update)
		sd.DELETE("/:id", h.Delete)
		// M25: 版本历史 API
		sd.GET("/:id/versions", h.ListVersions)
		sd.GET("/:id/versions/:versionNo", h.GetVersion)
		sd.POST("/:id/versions/:versionNo/restore", h.RestoreVersion)
		// Phase 2: 版本对比
		sd.GET("/:id/versions/compare", h.CompareVersions)
		// M23: 协同编辑 - 在线编辑者列表
		sd.GET("/:id/editors", h.GetEditors)
		// Phase 2: 文档评论
		sd.GET("/:id/comments", h.ListComments)
		sd.POST("/:id/comments", h.CreateComment)
		sd.PATCH("/:id/comments/:commentId", h.UpdateComment)
		sd.DELETE("/:id/comments/:commentId", h.DeleteComment)
		sd.POST("/:id/comments/:commentId/resolve", h.ResolveComment)
		sd.POST("/:id/comments/:commentId/reopen", h.ReopenComment)
	}
}

// DocResponse is the frontend-friendly format for document lists
type DocResponse struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Creator   string `json:"creator"`
	UpdatedAt string `json:"updatedAt"`
}

// List returns shared document summaries for the current space.
func (h *Handler) List(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var docs []model.SharedDocument
	if err := h.db.Where("space_id = ?", spaceID).Find(&docs).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// Batch query usernames for all documents (avoid N+1)
	ownerIDs := make([]string, 0, len(docs))
	for _, d := range docs {
		ownerIDs = append(ownerIDs, d.OwnerID)
	}
	var users []model.User
	usernameMap := make(map[string]string)
	if len(ownerIDs) > 0 {
		if err := h.db.Select("id, display_name, username").Where("id IN ?", ownerIDs).Find(&users).Error; err != nil {
			errors.JSONError(c, errors.ErrInternal)
			return
		}
		for _, u := range users {
			usernameMap[u.ID] = u.DisplayName
if usernameMap[u.ID] == "" {
	usernameMap[u.ID] = u.Username
}
		}
	}

	result := make([]DocResponse, 0, len(docs))
	for _, d := range docs {
		result = append(result, DocResponse{
			ID:        d.ID,
			Title:     d.Title,
			Creator:   usernameMap[d.OwnerID],
			UpdatedAt: d.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}
	errors.Success(c, result)
}

// Create creates a new shared document
func (h *Handler) Create(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var body struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	doc := model.SharedDocument{
		ID:      idgen.GenerateID(idgen.PrefixDoc),
		SpaceID: spaceID,
		Title:   body.Title,
		Content: body.Content,
		OwnerID: middleware.GetUserID(c),
	}
	if err := h.db.Create(&doc).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// C37: Broadcast sharedoc_created event to all clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "sharedoc_created",
			"payload": map[string]interface{}{
				"id":        doc.ID,
				"title":     doc.Title,
				"ownerId":   doc.OwnerID,
				"spaceId":   doc.SpaceID,
				"version":   doc.Version,
				"createdAt": doc.CreatedAt,
			},
		})
		h.broadcastToDocSpace(doc.SpaceID, data)
	}

	errors.Success(c, doc)
}

// Update updates a shared document
func (h *Handler) Update(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Title           string `json:"title"`
		Content         string `json:"content"`
		ExpectedVersion *int   `json:"expectedVersion"` // M23: 乐观并发控制（可选，nil 时不校验）
		VersionNote     string `json:"versionNote"`     // Phase 2: 版本备注
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canEditDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to operate this document"))
		return
	}

	// M23: 版本号乐观并发控制
	if body.ExpectedVersion != nil && *body.ExpectedVersion != doc.Version {
		errors.JSONError(c, errors.New(errors.SHAREDOC_CONFLICT, "document version conflict"))
		return
	}

	updates := map[string]interface{}{}
	if body.Title != "" {
		updates["title"] = body.Title
	}
	if body.Content != "" {
		if len(body.Content) > maxDocContentLen {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("document content exceeds maximum length"))
			return
		}
		updates["content"] = body.Content
	}
	if len(updates) == 0 {
		errors.Success(c, gin.H{"message": "no changes"})
		return
	}

	// M25: 使用事务记录版本历史 + 更新主表
	err := h.db.Transaction(func(tx *gorm.DB) error {
		// 1. 将当前版本（更新前的内容）写入版本历史表
		versionSnapshot := model.SharedDocumentVersion{
			ID:         idgen.GenerateID(idgen.PrefixDoc),
			DocumentID: doc.ID,
			VersionNo:  doc.Version,
			Title:      doc.Title,
			Content:    doc.Content,
			EditedBy:   userID,
			Note:       body.VersionNote,
		}
		if err := tx.Create(&versionSnapshot).Error; err != nil {
			return err
		}
		// 2. 递增主表 Version 字段
		updates["version"] = doc.Version + 1
		updates["updated_at"] = time.Now()
		// 3. 更新主表
		if err := tx.Model(&model.SharedDocument{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// C37: Broadcast sharedoc_updated event to space clients.
	// H16: 顶层补 userId，客户端据此过滤自己触发的更新（协议见
	// DES-2026-0713 §5.6）；此前缺 userId，本人保存也会被当成远端更新。
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type":   "sharedoc_updated",
			"userId": userID,
			"payload": map[string]interface{}{
				"id":      id,
				"title":   body.Title,
				"content": body.Content,
				"ownerId": doc.OwnerID,
				"spaceId": doc.SpaceID,
				"version": doc.Version + 1, // M25: 返回新版本号
			},
		})
		h.broadcastToDocSpace(doc.SpaceID, data)
	}

	errors.Success(c, gin.H{"message": "updated", "version": doc.Version + 1})
}

// Delete deletes a shared document
func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("id")

	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canEditDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to operate this document"))
		return
	}

	if err := h.db.Delete(&model.SharedDocument{}, "id = ?", id).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// C37: Broadcast sharedoc_deleted event to space clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "sharedoc_deleted",
			"payload": map[string]interface{}{
				"id":      id,
				"spaceId": doc.SpaceID,
				"ownerId": doc.OwnerID,
			},
		})
		h.broadcastToDocSpace(doc.SpaceID, data)
	}

	errors.Success(c, gin.H{"message": "deleted"})
}

// VersionListResponse is the frontend-friendly format for version list (M25)
type VersionListResponse struct {
	ID        string `json:"id"`
	VersionNo int    `json:"versionNo"`
	Title     string `json:"title"`
	EditedBy  string `json:"editedBy"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
}

// ListVersions returns version history of a document (M25)
// GET /sharedoc/:id/versions
func (h *Handler) ListVersions(c *gin.Context) {
	id := c.Param("id")

	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canAccessDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to view versions"))
		return
	}

	var versions []model.SharedDocumentVersion
	if err := h.db.Where("document_id = ?", id).Order("version_no DESC").Find(&versions).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// 批量查询编辑者用户名
	editorIDs := make([]string, 0, len(versions))
	for _, v := range versions {
		editorIDs = append(editorIDs, v.EditedBy)
	}
	var users []model.User
	usernameMap := make(map[string]string)
	if len(editorIDs) > 0 {
		if err := h.db.Select("id, display_name, username").Where("id IN ?", editorIDs).Find(&users).Error; err == nil {
			for _, u := range users {
				usernameMap[u.ID] = u.DisplayName
if usernameMap[u.ID] == "" {
	usernameMap[u.ID] = u.Username
}
			}
		}
	}

	result := make([]VersionListResponse, 0, len(versions))
	for _, v := range versions {
		result = append(result, VersionListResponse{
			ID:        v.ID,
			VersionNo: v.VersionNo,
			Title:     v.Title,
			EditedBy:  usernameMap[v.EditedBy],
			Note:      v.Note,
			CreatedAt: v.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	errors.Success(c, result)
}

// GetVersion returns a specific version's full content (M25)
// GET /sharedoc/:id/versions/:versionNo
func (h *Handler) GetVersion(c *gin.Context) {
	id := c.Param("id")
	versionNoStr := c.Param("versionNo")
	versionNo, err := strconv.Atoi(versionNoStr)
	if err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid versionNo"))
		return
	}

	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canAccessDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to view version"))
		return
	}

	var version model.SharedDocumentVersion
	if err := h.db.Where("document_id = ? AND version_no = ?", id, versionNo).First(&version).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "version not found"))
		return
	}

	errors.Success(c, version)
}

// RestoreVersion restores a document to a specific version (M25)
// POST /sharedoc/:id/versions/:versionNo/restore
func (h *Handler) RestoreVersion(c *gin.Context) {
	id := c.Param("id")
	versionNoStr := c.Param("versionNo")
	versionNo, err := strconv.Atoi(versionNoStr)
	if err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid versionNo"))
		return
	}

	userID := middleware.GetUserID(c)
	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canEditDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to restore version"))
		return
	}

	// 查询目标版本
	var targetVersion model.SharedDocumentVersion
	if err := h.db.Where("document_id = ? AND version_no = ?", id, versionNo).First(&targetVersion).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "version not found"))
		return
	}

	// 使用事务：1. 记录当前版本到历史 2. 恢复目标版本内容到主表
	err = h.db.Transaction(func(tx *gorm.DB) error {
		// 1. 将当前版本（恢复前的内容）写入版本历史表
		currentSnapshot := model.SharedDocumentVersion{
			ID:         idgen.GenerateID(idgen.PrefixDoc),
			DocumentID: doc.ID,
			VersionNo:  doc.Version,
			Title:      doc.Title,
			Content:    doc.Content,
			EditedBy:   userID,
		}
		if err := tx.Create(&currentSnapshot).Error; err != nil {
			return err
		}
		// 2. 恢复目标版本内容到主表，递增 Version
		updates := map[string]interface{}{
			"title":   targetVersion.Title,
			"content": targetVersion.Content,
			"version": doc.Version + 1,
		}
		if err := tx.Model(&model.SharedDocument{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// C37: Broadcast sharedoc_restored event to space clients（H16: 顶层补
	// userId，与 sharedoc_updated 一致，供客户端过滤自己触发的还原）
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type":   "sharedoc_restored",
			"userId": userID,
			"payload": map[string]interface{}{
				"id":         id,
				"spaceId":    doc.SpaceID,
				"versionNo":  versionNo,
				"newVersion": doc.Version + 1,
				"restoredBy": userID,
				"title":      targetVersion.Title,
				"content":    targetVersion.Content,
			},
		})
		h.broadcastToDocSpace(doc.SpaceID, data)
	}

	errors.Success(c, gin.H{
		"message":   "restored",
		"version":   doc.Version + 1,
		"versionNo": versionNo,
		"title":     targetVersion.Title,
		"content":   targetVersion.Content,
	})
}

// GetEditors returns the list of users currently editing a document (M23)
// GET /sharedoc/:id/editors
func (h *Handler) GetEditors(c *gin.Context) {
	id := c.Param("id")

	// 验证文档存在
	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canAccessDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to view editors"))
		return
	}

	h.mu.RLock()
	userSet, ok := h.editors[id]
	h.mu.RUnlock()

	if !ok || len(userSet) == 0 {
		errors.Success(c, gin.H{"docId": id, "editors": []interface{}{}, "count": 0})
		return
	}

	// 批量查询用户名
	h.mu.RLock()
	userIDs := make([]string, 0, len(userSet))
	for uid := range userSet {
		userIDs = append(userIDs, uid)
	}
	snapshot := make(map[string]time.Time, len(userSet))
	for k, v := range userSet {
		snapshot[k] = v
	}
	h.mu.RUnlock()

	var users []model.User
	usernameMap := make(map[string]string)
	if len(userIDs) > 0 {
		if err := h.db.Select("id, display_name, username").Where("id IN ?", userIDs).Find(&users).Error; err == nil {
			for _, u := range users {
				usernameMap[u.ID] = u.DisplayName
if usernameMap[u.ID] == "" {
	usernameMap[u.ID] = u.Username
}
			}
		}
	}

	editors := make([]gin.H, 0, len(snapshot))
	for uid, joinedAt := range snapshot {
		editors = append(editors, gin.H{
			"userId":   uid,
			"username": usernameMap[uid],
			"joinedAt": joinedAt.Format(time.RFC3339),
		})
	}
	errors.Success(c, gin.H{"docId": id, "editors": editors, "count": len(editors)})
}

// AddEditor marks a user as currently editing a document (M23)
// Called by WebSocket handler when user joins doc editing session.
func (h *Handler) AddEditor(docID, userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.editors[docID] == nil {
		h.editors[docID] = make(map[string]time.Time)
	}
	if _, exists := h.editors[docID][userID]; !exists {
		h.editors[docID][userID] = time.Now()
	}
	if h.userEdits[userID] == nil {
		h.userEdits[userID] = make(map[string]bool)
	}
	h.userEdits[userID][docID] = true
}

// RemoveEditor removes a user from a document's editing session (M23)
// Called by WebSocket handler when user leaves doc editing session.
func (h *Handler) RemoveEditor(docID, userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.editors[docID] != nil {
		delete(h.editors[docID], userID)
		if len(h.editors[docID]) == 0 {
			delete(h.editors, docID)
		}
	}
	if h.userEdits[userID] != nil {
		delete(h.userEdits[userID], docID)
		if len(h.userEdits[userID]) == 0 {
			delete(h.userEdits, userID)
		}
	}
}

// OnUserFullyOffline cleans up any document editing
// sessions when a user's last connection disconnects.
func (h *Handler) OnUserFullyOffline(userID string) {
	h.mu.Lock()
	docs := make([]string, 0, len(h.userEdits[userID]))
	for docID := range h.userEdits[userID] {
		docs = append(docs, docID)
	}
	delete(h.userEdits, userID)
	h.mu.Unlock()
	for _, docID := range docs {
		h.RemoveEditor(docID, userID)
	}
}

// CompareVersions returns a word-level diff between two versions.
// GET /sharedoc/:id/versions/compare?a=1&b=3
func (h *Handler) CompareVersions(c *gin.Context) {
	id := c.Param("id")
	aStr := c.Query("a")
	bStr := c.Query("b")
	aNo, err1 := strconv.Atoi(aStr)
	bNo, err2 := strconv.Atoi(bStr)
	if err1 != nil || err2 != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("a and b version numbers required"))
		return
	}

	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canAccessDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to compare versions"))
		return
	}

	resolveContent := func(no int) (string, error) {
		if no == doc.Version {
			return doc.Content, nil
		}
		var v model.SharedDocumentVersion
		if err := h.db.Where("document_id = ? AND version_no = ?", id, no).First(&v).Error; err != nil {
			return "", err
		}
		return v.Content, nil
	}

	contentA, err := resolveContent(aNo)
	if err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "version a not found"))
		return
	}
	contentB, err := resolveContent(bNo)
	if err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "version b not found"))
		return
	}

	diff := WordDiff(contentA, contentB)
	errors.Success(c, gin.H{
		"a":    aNo,
		"b":    bNo,
		"diff": diff,
	})
}

// canAccessDoc checks whether the current user can view the document.
// Space members can view; owners/admins can always view.
func (h *Handler) canAccessDoc(c *gin.Context, doc *model.SharedDocument) bool {
	userID := middleware.GetUserID(c)
	if doc.OwnerID == userID || middleware.IsAdmin(c) {
		return true
	}
	var membership model.Membership
	err := h.db.Where("user_id = ? AND space_id = ?", userID, doc.SpaceID).First(&membership).Error
	return err == nil
}

// canEditDoc checks whether the current user can modify/delete/restore the document.
// Only the document owner or space admins can edit.
func (h *Handler) canEditDoc(c *gin.Context, doc *model.SharedDocument) bool {
	userID := middleware.GetUserID(c)
	return doc.OwnerID == userID || middleware.IsAdmin(c)
}

// spaceMemberIDs 查询空间全部成员的用户 ID（空间创建者在建空间时已写入
// Membership，因此包含 Owner）。查询失败返回 nil：调用方跳过广播，宁可
// 少一次实时通知也不把事件泄露给非成员（fail-closed，与白板 T1 同款）。
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

// broadcastToDocSpace 将共享文档列表级事件（created/updated/deleted/restored/
// comment_*）定向送达空间全部在线成员（H16）。此前走
// BroadcastToChannel(spaceID)：客户端从不订阅空间键，事件无人能收到；
// 而全局 Broadcast 又会跨空间泄露。改用 BroadcastToUsers 后客户端行为
// 不变（在线成员仍即时刷新列表/评论，离线成员进入面板时 HTTP 拉取兜底）。
func (h *Handler) broadcastToDocSpace(spaceID string, data []byte) {
	if h.hub == nil {
		return
	}
	members := h.spaceMemberIDs(spaceID)
	if len(members) == 0 {
		return
	}
	h.hub.BroadcastToUsers(members, data)
}

var mentionRe = regexp.MustCompile(`@([a-zA-Z0-9_]+)`)

func (h *Handler) broadcastCommentEvent(docID, eventType string, comment model.SharedDocumentComment) {
	if h.hub == nil {
		return
	}
	var doc model.SharedDocument
	if err := h.db.Select("space_id").Where("id = ?", docID).First(&doc).Error; err != nil {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type": "sharedoc_comment_" + eventType,
		"payload": map[string]interface{}{
			"documentId": docID,
			"spaceId":    doc.SpaceID,
			"comment":    comment,
		},
	})
	h.broadcastToDocSpace(doc.SpaceID, data)
}

func (h *Handler) sendMentionNotifications(fromUserID, docID, docTitle string, content string) {
	if h.hub == nil {
		return
	}
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		userID := m[1]
		if userID == "" || seen[userID] {
			continue
		}
		seen[userID] = true
		data, _ := json.Marshal(map[string]interface{}{
			"type": "mention_notification",
			"payload": map[string]interface{}{
				"fromUserId":    fromUserID,
				"documentId":    docID,
				"documentTitle": docTitle,
				"userId":        userID,
			},
		})
		h.hub.BroadcastToUser(userID, data)
	}
}

// CommentResponse is the frontend-friendly comment format.
type CommentResponse struct {
	ID           string            `json:"id"`
	DocumentID   string            `json:"documentId"`
	ParentID     *string           `json:"parentId,omitempty"`
	AnchorText   string            `json:"anchorText"`
	AnchorOffset *int              `json:"anchorOffset,omitempty"`
	AnchorLength *int              `json:"anchorLength,omitempty"`
	Content      string            `json:"content"`
	CreatedBy    string            `json:"createdBy"`
	Creator      string            `json:"creator"`
	Resolved     bool              `json:"resolved"`
	ResolvedBy   *string           `json:"resolvedBy,omitempty"`
	ResolvedAt   *string           `json:"resolvedAt,omitempty"`
	CreatedAt    string            `json:"createdAt"`
	UpdatedAt    string            `json:"updatedAt"`
	Replies      []CommentResponse `json:"replies,omitempty"`
}

func (h *Handler) formatComment(c model.SharedDocumentComment, usernameMap map[string]string) CommentResponse {
	var resolvedAt *string
	if c.ResolvedAt != nil {
		s := c.ResolvedAt.Format("2006-01-02 15:04")
		resolvedAt = &s
	}
	return CommentResponse{
		ID:           c.ID,
		DocumentID:   c.DocumentID,
		ParentID:     c.ParentID,
		AnchorText:   c.AnchorText,
		AnchorOffset: c.AnchorOffset,
		AnchorLength: c.AnchorLength,
		Content:      c.Content,
		CreatedBy:    c.CreatedBy,
		Creator:      usernameMap[c.CreatedBy],
		Resolved:     c.Resolved,
		ResolvedBy:   c.ResolvedBy,
		ResolvedAt:   resolvedAt,
		CreatedAt:    c.CreatedAt.Format("2006-01-02 15:04"),
		UpdatedAt:    c.UpdatedAt.Format("2006-01-02 15:04"),
	}
}

// ListComments returns the comment tree for a document.
func (h *Handler) ListComments(c *gin.Context) {
	id := c.Param("id")
	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canAccessDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to view comments"))
		return
	}

	var comments []model.SharedDocumentComment
	if err := h.db.Where("document_id = ?", id).Order("created_at ASC").Find(&comments).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	userIDs := make([]string, 0, len(comments)*2)
	for _, c := range comments {
		userIDs = append(userIDs, c.CreatedBy)
		if c.ResolvedBy != nil {
			userIDs = append(userIDs, *c.ResolvedBy)
		}
	}
	usernameMap := make(map[string]string)
	if len(userIDs) > 0 {
		var users []model.User
		if err := h.db.Select("id, display_name, username").Where("id IN ?", userIDs).Find(&users).Error; err == nil {
			for _, u := range users {
				usernameMap[u.ID] = u.DisplayName
if usernameMap[u.ID] == "" {
	usernameMap[u.ID] = u.Username
}
			}
		}
	}

	// Build tree: top-level + replies
	byID := make(map[string]*CommentResponse)
	var rootPtrs []*CommentResponse
	for _, c := range comments {
		formatted := h.formatComment(c, usernameMap)
		byID[c.ID] = &formatted
	}
	for _, c := range comments {
		resp := byID[c.ID]
		if c.ParentID != nil && *c.ParentID != "" {
			if parent, ok := byID[*c.ParentID]; ok {
				parent.Replies = append(parent.Replies, *resp)
			}
		} else {
			rootPtrs = append(rootPtrs, resp)
		}
	}

	result := make([]CommentResponse, len(rootPtrs))
	for i, rp := range rootPtrs {
		result[i] = *rp
	}
	errors.Success(c, result)
}

// CreateComment creates a top-level comment or a reply.
func (h *Handler) CreateComment(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		ParentID     string `json:"parentId"`
		AnchorText   string `json:"anchorText"`
		AnchorOffset *int   `json:"anchorOffset"`
		AnchorLength *int   `json:"anchorLength"`
		Content      string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("content is required"))
		return
	}

	userID := middleware.GetUserID(c)
	var doc model.SharedDocument
	if err := h.db.Where("id = ?", id).First(&doc).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "document not found"))
		return
	}
	if !h.canAccessDoc(c, &doc) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to comment"))
		return
	}

	var parentID *string
	if body.ParentID != "" {
		var parent model.SharedDocumentComment
		if err := h.db.Where("id = ? AND document_id = ?", body.ParentID, id).First(&parent).Error; err != nil {
			errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "parent comment not found"))
			return
		}
		parentID = &body.ParentID
	}

	comment := model.SharedDocumentComment{
		ID:           idgen.GenerateID(idgen.PrefixComment),
		DocumentID:   id,
		ParentID:     parentID,
		AnchorText:   body.AnchorText,
		AnchorOffset: body.AnchorOffset,
		AnchorLength: body.AnchorLength,
		Content:      strings.TrimSpace(body.Content),
		CreatedBy:    userID,
	}
	if err := h.db.Create(&comment).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	h.broadcastCommentEvent(id, "created", comment)
	h.sendMentionNotifications(userID, id, doc.Title, comment.Content)

	errors.Success(c, comment)
}

// UpdateComment edits a comment's content.
func (h *Handler) UpdateComment(c *gin.Context) {
	id := c.Param("id")
	commentID := c.Param("commentId")
	var body struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("content is required"))
		return
	}

	userID := middleware.GetUserID(c)
	var comment model.SharedDocumentComment
	if err := h.db.Where("id = ? AND document_id = ?", commentID, id).First(&comment).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "comment not found"))
		return
	}
	if comment.CreatedBy != userID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to edit comment"))
		return
	}

	comment.Content = strings.TrimSpace(body.Content)
	if err := h.db.Save(&comment).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	h.broadcastCommentEvent(id, "updated", comment)
	errors.Success(c, comment)
}

// DeleteComment removes a comment and its replies.
func (h *Handler) DeleteComment(c *gin.Context) {
	id := c.Param("id")
	commentID := c.Param("commentId")
	userID := middleware.GetUserID(c)

	var comment model.SharedDocumentComment
	if err := h.db.Where("id = ? AND document_id = ?", commentID, id).First(&comment).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "comment not found"))
		return
	}
	if comment.CreatedBy != userID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to delete comment"))
		return
	}

	if err := h.db.Where("id = ? OR parent_id = ?", commentID, commentID).Delete(&model.SharedDocumentComment{}).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	comment.Content = ""
	h.broadcastCommentEvent(id, "deleted", comment)
	errors.Success(c, gin.H{"message": "deleted"})
}

// ResolveComment marks a comment thread as resolved.
func (h *Handler) ResolveComment(c *gin.Context) {
	id := c.Param("id")
	commentID := c.Param("commentId")
	userID := middleware.GetUserID(c)

	var comment model.SharedDocumentComment
	if err := h.db.Where("id = ? AND document_id = ?", commentID, id).First(&comment).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "comment not found"))
		return
	}
	if comment.CreatedBy != userID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to resolve comment"))
		return
	}
	if comment.Resolved {
		errors.Success(c, comment)
		return
	}

	now := time.Now()
	comment.Resolved = true
	comment.ResolvedBy = &userID
	comment.ResolvedAt = &now
	if err := h.db.Save(&comment).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	h.broadcastCommentEvent(id, "resolved", comment)
	errors.Success(c, comment)
}

// ReopenComment reopens a resolved comment thread.
func (h *Handler) ReopenComment(c *gin.Context) {
	id := c.Param("id")
	commentID := c.Param("commentId")
	userID := middleware.GetUserID(c)

	var comment model.SharedDocumentComment
	if err := h.db.Where("id = ? AND document_id = ?", commentID, id).First(&comment).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SHAREDOC_NOT_FOUND, "comment not found"))
		return
	}
	if comment.CreatedBy != userID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.New(errors.SHAREDOC_PERMISSION_DENIED, "no permission to reopen comment"))
		return
	}
	if !comment.Resolved {
		errors.Success(c, comment)
		return
	}

	comment.Resolved = false
	comment.ResolvedBy = nil
	comment.ResolvedAt = nil
	if err := h.db.Save(&comment).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	h.broadcastCommentEvent(id, "reopened", comment)
	errors.Success(c, comment)
}
