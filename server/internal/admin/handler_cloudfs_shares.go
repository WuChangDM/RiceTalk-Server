package admin

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/middleware"
)

// E4：admin 全局分享管理（DES-20261001-01 §11.2）。
//
// 两个端点都挂在 /admin 路由组上，鉴权由 routes.go 的
// AuthRequiredWithState + RequireAdmin()（RoleAdmin+）统一保证，handler 内不再重复检查。
// 业务逻辑全部落在 cloudfs.Service（AdminListShares / RevokeShareAsAdmin），
// 与空间内分享路径共用同一套软删过滤与撤销语义。

const (
	// adminSharesDefaultLimit / adminSharesMaxLimit：全局分享列表分页上限。
	adminSharesDefaultLimit = 50
	adminSharesMaxLimit     = 200

	// adminShareRevokeAuditAction 是 admin 撤销分享写入 SecurityAuditLog 的动作名。
	// 与 cloudfs 既有 cloudfs.share.* 审计同一张表、同一 helper（LogShareSecurityEvent）。
	adminShareRevokeAuditAction = "admin.cloudfs.share_revoked"
)

// parseAdminSharesPage 解析 limit/offset 查询参数：
// limit 默认 50，最大 200（超限截断），非法或 ≤0 回落默认；offset 非法或 <0 归零。
func parseAdminSharesPage(c *gin.Context) (limit, offset int) {
	limit = adminSharesDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > adminSharesMaxLimit {
		limit = adminSharesMaxLimit
	}
	offset = 0
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			offset = v
		}
	}
	return limit, offset
}

// AdminListCloudShares 跨全部空间列出分享（联出文件名/空间名，分页 + created_at DESC）。
func (h *Handler) AdminListCloudShares(c *gin.Context) {
	limit, offset := parseAdminSharesPage(c)

	items, err := h.cloudfsSvc.AdminListShares(limit, offset)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	resp := make([]gin.H, 0, len(items))
	for i := range items {
		it := items[i]
		item := gin.H{
			"id":               it.ID,
			"fileId":           it.FileID,
			"fileName":         it.FileName,
			"userId":           it.UserID,
			"spaceId":          it.SpaceID,
			"spaceName":        it.SpaceName,
			"downloadCount":    it.DownloadCount,
			"revoked":          it.Revoked,
			"expired":          it.Expired,
			"requiresPassword": it.RequiresPassword,
			"expiresAt":        it.ExpiresAt.UTC().Format(time.RFC3339),
			"createdAt":        it.CreatedAt.UTC().Format(time.RFC3339),
		}
		if it.LastAccessAt != nil {
			item["lastAccessAt"] = it.LastAccessAt.UTC().Format(time.RFC3339)
		} else {
			item["lastAccessAt"] = nil
		}
		resp = append(resp, item)
	}
	errors.Success(c, gin.H{"items": resp})
}

// AdminRevokeCloudShare 以全局管理员身份撤销任意空间的分享。
//
// 复用 RevokeShareAsAdmin 的既有撤销语义（条件更新、幂等：重复撤销返回 200，
// 不存在返回 404）。审计只在**本次调用实际执行撤销**时写一条 SecurityAuditLog
// （action=admin.cloudfs.share_revoked），幂等的重复撤销不刷审计。
func (h *Handler) AdminRevokeCloudShare(c *gin.Context) {
	share, revoked, err := h.cloudfsSvc.RevokeShareAsAdmin(c.Param("id"))
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	if revoked {
		userID := middleware.GetUserID(c)
		ip := c.ClientIP()
		userAgent := c.Request.UserAgent()
		// 与 cloudfs 分享域同一审计 helper（SecurityAuditLog，IP 掩码，token 永不入审计）。
		h.cloudfsSvc.LogShareSecurityEvent(userID, adminShareRevokeAuditAction, share.ID, ip, userAgent,
			map[string]interface{}{
				"fileId":   share.FileID,
				"spaceId":  share.SpaceID,
				"viaAdmin": true,
			})
		// admin 包既有 AuditLog 审计同步落一条，便于管理后台审计页统一检索。
		_ = h.service.LogAudit(userID, adminShareRevokeAuditAction, "cloud_file_share",
			share.ID, ip, userAgent, true)
	}

	resp := gin.H{"id": share.ID, "revoked": true}
	if share.RevokedAt != nil {
		resp["revokedAt"] = share.RevokedAt.UTC().Format(time.RFC3339)
	}
	errors.Success(c, resp)
}
