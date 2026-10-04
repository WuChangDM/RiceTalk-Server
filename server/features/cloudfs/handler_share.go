package cloudfs

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/middleware"
)

// DES-2026-0912-05 云文件「分享链接」HTTP 层。
//
// 三条公开路由是本产品唯一免鉴权的业务接口，因此：
//   - 独立 group（RegisterShareRoutes），**不挂到任何既有 group**，不带 AuthRequired；
//   - 独立限流器（h.shareLimiter，与全局 limiter 分离）双向限流：按 IP + 按分享；
//   - 复用 middleware.BruteForceProtector（h.shareProtector）做密码爆破防护；
//   - 响应统一带 nosniff / no-store / noindex / no-referrer 安全头（设计 §7）。

const (
	// shareRateLimitRequests / shareRateLimitBurst：公开分享面的独立配额。
	// 单个分享链接被高频轮询时应先被限流而不是打到库里。
	shareRateLimitRequests = 30
	shareRateLimitBurst    = 10
)

// RegisterShareRoutes 在给定 group 上注册三条公开路由（设计 §3）。
// 调用方负责把它挂在 /api/v1/share 上，并且**不得**在该 group 上加鉴权中间件。
func (h *Handler) RegisterShareRoutes(r *gin.RouterGroup) {
	r.Use(shareResponseHeaders())
	r.Use(middleware.RateLimit(h.shareLimiter)) // 第一维：按 IP
	r.Use(h.shareSubjectRateLimit())            // 第二维：按分享
	r.GET("/:token", h.ShareMeta)
	r.POST("/:token/verify", h.ShareVerify)
	r.GET("/:token/download", h.ShareDownload)
}

// shareResponseHeaders 给所有分享响应加上与「一条 URL 即凭据」相称的头部（设计 §7）：
// 禁止 MIME 嗅探、禁止缓存、禁止被索引、禁止 token 经 Referer 泄漏到第三方。
func shareResponseHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Header("X-Robots-Tag", "noindex, nofollow")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	}
}

// shareSubjectRateLimit 是按「分享」维度的限流，叠加在 RateLimit 的按 IP 维度之上。
// 两维共用同一个独立 limiter 实例；key 用 token 的哈希，避免把明文令牌留在内存键里。
func (h *Handler) shareSubjectRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")
		if token == "" {
			c.Next()
			return
		}
		key := "share:" + HashShareToken(token)
		if !h.shareLimiter.Allow(key) {
			h.shareThrottled(c, h.shareLimiter.RetryAfter(key))
			return
		}
		c.Next()
	}
}

// shareThrottled 以统一的 429 + Retry-After 形式拒绝。
func (h *Handler) shareThrottled(c *gin.Context, retryAfter int) {
	if retryAfter <= 0 {
		retryAfter = 30
	}
	c.Header("Retry-After", strconv.Itoa(retryAfter))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, errors.FromError(c, errors.ErrRateLimited))
}

// ShareMeta 返回分享的非敏感元信息（设计 §3/§7）。
//
// 绝不返回：storage key、真实磁盘路径、ownerID、其他分享的存在性。
// 文件名/大小只在分享仍可用时返回 —— 失效链接不再继续披露内容元信息。
func (h *Handler) ShareMeta(c *gin.Context) {
	share, err := h.service.GetShareByToken(c.Param("token"))
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	state := h.service.EvaluateShare(share)
	resp := gin.H{
		"id":               share.ID,
		"requiresPassword": share.PasswordHash != "",
		"expiresAt":        share.ExpiresAt.UTC().Format(time.RFC3339),
		"maxDownloads":     share.MaxDownloads,
		"downloadCount":    share.DownloadCount,
		"revoked":          state.Revoked,
		"expired":          state.Expired,
		"exhausted":        state.Exhausted,
		"available":        state.Available,
	}

	if state.Available {
		if entry, entryErr := h.service.shareFileEntry(share); entryErr == nil {
			resp["fileName"] = entry.FileName
			resp["fileSize"] = entry.FileSize
			resp["mimeType"] = entry.MimeType
		} else {
			// 文件已删除/已进回收站：链接仍然「有效」但取不到内容。
			resp["available"] = false
		}
	}
	errors.Success(c, resp)
}

// ShareVerify 校验分享密码并签发一次性短期下载凭据（设计 §5）。
//
// 关键约束：**「密码错误」与「分享不存在」返回完全相同的错误**（同一个错误码、
// 同一条文案、同一个 HTTP 状态），否则该接口就成了分享存在性的探测器。
// 爆破防护在 bcrypt 之前生效，避免失败校验被用来放大 CPU 开销。
func (h *Handler) ShareVerify(c *gin.Context) {
	token := c.Param("token")
	ip := c.ClientIP()
	userAgent := c.Request.UserAgent()
	// 账号维度在「分享不存在」时只能退化为按 token 哈希计数 —— 否则猜 token 的
	// 尝试就完全不受爆破防护约束了。
	tokenAccountKey := "share:" + HashShareToken(token)

	if ok, retry := h.shareProtector.AllowIP(ip); !ok {
		h.shareThrottled(c, int(retry.Seconds())+1)
		return
	}

	share, err := h.service.GetShareByToken(token)
	if err != nil {
		if ok, retry := h.shareProtector.AllowAccount(tokenAccountKey); !ok {
			h.shareThrottled(c, int(retry.Seconds())+1)
			return
		}
		h.service.LogShareSecurityEvent("", ShareAuditPasswordFailed, "", ip, userAgent,
			map[string]interface{}{"reason": "share_not_found", "tokenPrefix": shareTokenPrefix(token)})
		h.shareProtector.RecordFailure(tokenAccountKey, ip)
		errors.JSONError(c, errors.New(errors.SHARE_VERIFY_FAILED, "share verification failed"))
		return
	}

	// 账号维度的锁定必须要能在「密码错误」累计后命中，因此用 share.ID 计数；
	// 检查在 bcrypt 之前，失败校验无法被用来放大 CPU 开销。
	shareAccountKey := "share:" + share.ID
	if ok, retry := h.shareProtector.AllowAccount(shareAccountKey); !ok {
		h.shareThrottled(c, int(retry.Seconds())+1)
		return
	}

	// 允许空 body（无密码分享的校验请求不带 body）。校验失败与「body 解析失败」
	// 不做区分 —— 后者同样走统一的失败响应。
	var body struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&body)

	if state := h.service.EvaluateShare(share); !state.Available {
		h.service.LogShareSecurityEvent("", ShareAuditPasswordFailed, share.ID, ip, userAgent,
			map[string]interface{}{"reason": state.Reason()})
		h.shareProtector.RecordFailure(shareAccountKey, ip)
		errors.JSONError(c, errors.New(errors.SHARE_VERIFY_FAILED, "share verification failed"))
		return
	}

	if share.PasswordHash != "" {
		if !crypto.CheckPassword(body.Password, share.PasswordHash) {
			h.service.LogShareSecurityEvent("", ShareAuditPasswordFailed, share.ID, ip, userAgent,
				map[string]interface{}{"reason": "password_mismatch"})
			h.shareProtector.RecordFailure(shareAccountKey, ip)
			errors.JSONError(c, errors.New(errors.SHARE_VERIFY_FAILED, "share verification failed"))
			return
		}
	}

	ticket, err := h.service.shareTickets.Issue(share.ID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	errors.Success(c, gin.H{
		"ticket":    ticket,
		"expiresIn": int(ShareTicketTTL.Seconds()),
	})
}

// ShareDownload 下载分享文件。
//
// 顺序上是设计 §6 的硬要求：**先原子占用、再返回文件内容**。任何「先判断能不能
// 下载、再递增计数」的写法在并发下都会被绕过，从而突破 MaxDownloads。
func (h *Handler) ShareDownload(c *gin.Context) {
	token := c.Param("token")
	ip := c.ClientIP()
	userAgent := c.Request.UserAgent()

	share, err := h.service.GetShareByToken(token)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// 有密码的分享必须先通过 POST /verify 换取一次性凭据（设计 §5）。
	// 凭据在占用配额之前消费：失败重试需要重新校验密码，这是刻意的选择。
	if share.PasswordHash != "" {
		if !h.service.shareTickets.Consume(share.ID, c.Query("ticket")) {
			h.service.LogShareSecurityEvent("", ShareAuditDownloadDenied, share.ID, ip, userAgent,
				map[string]interface{}{"reason": "missing_or_invalid_ticket"})
			errors.JSONError(c, errors.New(errors.SHARE_TICKET_INVALID, "password verification required"))
			return
		}
	}

	count, err := h.service.ClaimShareDownload(share.ID)
	if err != nil {
		reason := "unavailable"
		if appErr, ok := err.(*errors.AppError); ok {
			reason = string(appErr.Code)
		}
		h.service.LogShareSecurityEvent("", ShareAuditDownloadDenied, share.ID, ip, userAgent,
			map[string]interface{}{
				"reason":        reason,
				"downloadCount": share.DownloadCount,
				"maxDownloads":  share.MaxDownloads,
				"expiresAt":     share.ExpiresAt.UTC().Format(time.RFC3339),
			})
		errors.JSONError(c, err)
		return
	}

	entry, err := h.service.shareFileEntry(share)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	info, err := h.service.shareDownloadInfo(entry)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// 成功下载审计做采样，避免高频分享把审计表写爆（设计 §8）。
	if ShouldAuditDownload(count) {
		h.service.LogShareSecurityEvent("", ShareAuditDownloaded, share.ID, ip, userAgent,
			map[string]interface{}{"downloadCount": count, "sampled": count > 1})
	}

	// 与既有 GET /cloudfs/preview 完全一致的 MIME 白名单口径：白名单内 inline，
	// 其余（含 SVG / HTML 这类存储型 XSS 原语）一律 attachment（设计 §7）。
	disposition := "attachment"
	contentType := "application/octet-stream"
	if isInlinePreviewAllowed(entry.MimeType) {
		disposition = "inline"
		contentType = entry.MimeType
	}
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%s", disposition, strconv.Quote(entry.FileName)))
	c.Header("Content-Type", contentType)
	c.Header("Accept-Ranges", "bytes")
	setChecksumHeader(c, info.Checksum)
	c.File(info.PhysicalPath)
}

// CreateShare 创建分享链接（鉴权路由，挂在既有 /cloudfs 组下）。
//
// 返回体里的 token 是**唯一一次**能看到明文令牌的机会，客户端必须立刻保存/复制。
func (h *Handler) CreateShare(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var body struct {
		FileID        string `json:"fileId"`
		ExpiresInDays int    `json:"expiresInDays"`
		Password      string `json:"password"`
		MaxDownloads  int    `json:"maxDownloads"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if strings.TrimSpace(body.FileID) == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	share, token, err := h.service.CreateShare(spaceID, userID, ShareOptions{
		FileID:        body.FileID,
		ExpiresInDays: body.ExpiresInDays,
		Password:      body.Password,
		MaxDownloads:  body.MaxDownloads,
	})
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	h.service.LogShareSecurityEvent(userID, ShareAuditCreated, share.ID, c.ClientIP(), c.Request.UserAgent(),
		map[string]interface{}{
			"fileId":       share.FileID,
			"expiresAt":    share.ExpiresAt.Format(time.RFC3339),
			"hasPassword":  share.PasswordHash != "",
			"maxDownloads": share.MaxDownloads,
		})

	sharePath := "/api/v1/share/" + token
	resp := gin.H{
		"id":               share.ID,
		"fileId":           share.FileID,
		"token":            token, // 仅此一次
		"tokenPrefix":      share.TokenPrefix,
		"sharePath":        sharePath,
		"expiresAt":        share.ExpiresAt.UTC().Format(time.RFC3339),
		"maxDownloads":     share.MaxDownloads,
		"requiresPassword": share.PasswordHash != "",
	}
	if base := strings.TrimRight(h.cfg.PublicAddress, "/"); base != "" {
		resp["url"] = base + sharePath
	}
	errors.Success(c, resp)
}

// ListShares 列出当前空间内的分享（可带 fileId 过滤）。
func (h *Handler) ListShares(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	shares, err := h.service.ListShares(spaceID, userID, strings.TrimSpace(c.Query("fileId")))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	names := h.service.ShareFileNames(shares)

	now := time.Now().UTC()
	items := make([]gin.H, 0, len(shares))
	for i := range shares {
		share := shares[i]
		item := gin.H{
			"id":               share.ID,
			"fileId":           share.FileID,
			"tokenPrefix":      share.TokenPrefix,
			"requiresPassword": share.PasswordHash != "",
			"expiresAt":        share.ExpiresAt.UTC().Format(time.RFC3339),
			"maxDownloads":     share.MaxDownloads,
			"downloadCount":    share.DownloadCount,
			"createdAt":        share.CreatedAt.UTC().Format(time.RFC3339),
			"revoked":          share.RevokedAt != nil,
			"expired":          !share.ExpiresAt.After(now),
		}
		if share.RevokedAt != nil {
			item["revokedAt"] = share.RevokedAt.UTC().Format(time.RFC3339)
		}
		if share.LastAccessAt != nil {
			item["lastAccessAt"] = share.LastAccessAt.UTC().Format(time.RFC3339)
		}
		if name, ok := names[share.FileID]; ok {
			item["fileName"] = name
		} else {
			item["fileDeleted"] = true
		}
		items = append(items, item)
	}
	errors.Success(c, gin.H{"items": items})
}

// RevokeShare 撤销分享（创建者本人或空间管理员）。
func (h *Handler) RevokeShare(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	share, err := h.service.RevokeShare(spaceID, userID, c.Param("shareId"))
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	h.service.LogShareSecurityEvent(userID, ShareAuditRevoked, share.ID, c.ClientIP(), c.Request.UserAgent(),
		map[string]interface{}{"fileId": share.FileID})

	resp := gin.H{"id": share.ID, "revoked": true}
	if share.RevokedAt != nil {
		resp["revokedAt"] = share.RevokedAt.UTC().Format(time.RFC3339)
	}
	errors.Success(c, resp)
}
