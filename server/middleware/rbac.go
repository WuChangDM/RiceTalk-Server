package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
)

// Role constants
const (
	RoleOwner  = "OWNER"
	RoleAdmin  = "ADMIN"
	RoleMember = "MEMBER"
)

// RoleHierarchy defines role precedence (higher = more permissions)
var RoleHierarchy = map[string]int{
	RoleOwner:  3,
	RoleAdmin:  2,
	RoleMember: 1,
}

// RequireRole returns a middleware that requires at least the specified role
func RequireRole(minRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists {
			c.AbortWithStatusJSON(errors.ErrUnauthorized.Status, errors.FromError(c, errors.ErrUnauthorized))
			return
		}

		userRole, ok := role.(string)
		if !ok {
			c.AbortWithStatusJSON(errors.ErrUnauthorized.Status, errors.FromError(c, errors.ErrUnauthorized))
			return
		}

		// Owner has all permissions
		if userRole == RoleOwner {
			c.Next()
			return
		}

		userLevel := RoleHierarchy[userRole]
		minLevel := RoleHierarchy[minRole]

		if userLevel < minLevel {
			c.AbortWithStatusJSON(errors.ErrForbidden.Status, errors.FromError(c, errors.ErrForbidden))
			return
		}

		c.Next()
	}
}

// RequireAdmin requires ADMIN or OWNER role
func RequireAdmin() gin.HandlerFunc {
	return RequireRole(RoleAdmin)
}

// RequireOwner requires OWNER role only
func RequireOwner() gin.HandlerFunc {
	return RequireRole(RoleOwner)
}

// GetUserID extracts user ID from context
func GetUserID(c *gin.Context) string {
	uid, _ := c.Get("user_id")
	if id, ok := uid.(string); ok {
		return id
	}
	return ""
}

// GetUsername extracts username from context
func GetUsername(c *gin.Context) string {
	uname, _ := c.Get("username")
	if name, ok := uname.(string); ok {
		return name
	}
	return ""
}

// GetRole extracts role from context
func GetRole(c *gin.Context) string {
	role, _ := c.Get("role")
	if r, ok := role.(string); ok {
		return r
	}
	return RoleMember
}

// GetSpaceID extracts the current space ID from context (M1, design doc §3.1).
func GetSpaceID(c *gin.Context) string {
	sid, _ := c.Get("space_id")
	if id, ok := sid.(string); ok {
		return id
	}
	return ""
}

// RequireSpaceID extracts the current space ID from context, query parameter,
// or JSON body. It returns an error if no space ID is available.
// This prevents fallback to an arbitrary space in multi-space deployments.
func RequireSpaceID(c *gin.Context) (string, error) {
	// 1. Try JWT claim first
	if sid := GetSpaceID(c); sid != "" {
		return sid, nil
	}

	// 2. Try query parameter
	if sid := c.Query("spaceId"); sid != "" {
		return sid, nil
	}

	// 3. Try JSON body. Use GetRawData so the body is restored for downstream
	// handlers that call ShouldBindJSON.
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodDelete {
		bodyBytes, err := c.GetRawData()
		if err == nil && len(bodyBytes) > 0 {
			// Restore the body so subsequent ShouldBindJSON calls still work.
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			var body struct {
				SpaceID string `json:"spaceId"`
			}
			if json.Unmarshal(bodyBytes, &body) == nil && body.SpaceID != "" {
				return body.SpaceID, nil
			}
		}
	}

	return "", errors.ErrBadRequest.WithDetails("spaceId is required")
}

// GetSessionID extracts the session ID from context (M1, design doc §3.1).
func GetSessionID(c *gin.Context) string {
	sid, _ := c.Get("session_id")
	if id, ok := sid.(string); ok {
		return id
	}
	return ""
}

// IsOwner checks if the current user is the owner
func IsOwner(c *gin.Context) bool {
	return GetRole(c) == RoleOwner
}

// IsAdmin checks if the current user is admin or owner
func IsAdmin(c *gin.Context) bool {
	role := GetRole(c)
	return role == RoleOwner || role == RoleAdmin
}

// CheckChannelAccess 是频道访问判定的纯内核（不查库）：把已取出的频道元数据 +
// role-specific 权限行的 CanView（nil = 权限行不存在）交给同一套口径判定。
// ChannelPermission 中间件与「频道 ID 不在路由参数 :id 上」的调用方（如线程查询：
// 频道由父消息反查）必须共用本函数，禁止各自复制一份权限逻辑。
// 判定口径（§6.5A）：
//   - public: 所有已认证成员可访问
//   - admin-only: 仅 OWNER/ADMIN
//   - role-specific: 以 ChannelRolePermission.CanView 为准
//   - 未知 visibility: 默认拒绝
//
// OWNER 始终放行。返回 nil 表示放行，否则返回应直接回写客户端的错误。
func CheckChannelAccess(c *gin.Context, ch *model.Channel, roleCanView *bool) *errors.AppError {
	if ch == nil {
		return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}
	// dm 频道（legacy-only）与其他私有通道不经本权限通道访问。
	if strings.EqualFold(ch.Type, "dm") {
		return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}

	userRole := GetRole(c)
	// OWNER has full access to supported channel types.
	if userRole == RoleOwner {
		return nil
	}

	switch ch.Visibility {
	case "public", "":
		// allow all authenticated members
		return nil
	case "admin-only":
		if userRole == RoleAdmin {
			return nil
		}
		return errors.New(errors.CHANNEL_ACCESS_DENIED, "channel requires admin role")
	case "role-specific":
		if roleCanView == nil {
			return errors.New(errors.CHANNEL_ACCESS_DENIED, "no permission for this channel")
		}
		if !*roleCanView {
			return errors.New(errors.CHANNEL_ACCESS_DENIED, "view permission denied")
		}
		return nil
	default:
		// Unknown visibility — deny by default
		return errors.New(errors.CHANNEL_ACCESS_DENIED, "unknown channel visibility")
	}
}

// loadRoleCanView 取某角色在指定频道的 role-specific 权限行 CanView。
// 权限行不存在返回 (nil, nil)，由 CheckChannelAccess 统一转成 CHANNEL_ACCESS_DENIED；
// 数据库故障返回 ErrInternal，保持与旧 ChannelPermission 相同的 500 语义。
func loadRoleCanView(db *gorm.DB, channelID, role string) (*bool, *errors.AppError) {
	var perm model.ChannelRolePermission
	if err := db.Where("channel_id = ? AND role = ?", channelID, role).First(&perm).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, errors.ErrInternal
	}
	return &perm.CanView, nil
}

// AuthorizeChannel 按频道 ID 取频道（必要时补 role-specific 权限行）后调用
// CheckChannelAccess 判定，供 ChannelPermission 中间件使用。
func AuthorizeChannel(c *gin.Context, db *gorm.DB, channelID string) *errors.AppError {
	if channelID == "" {
		return errors.ErrBadRequest.WithDetails("channel id required")
	}

	// OWNER 短路：先于「频道可见性 + role-specific 权限行」加载（与重构前顺序一致：
	// OWNER 不因权限行查询故障从放行变 500）。dm 例外仍保留——私信不参与频道权限模型，
	// 任何角色一律 404，故 OWNER 只需读一次频道类型即可判定。
	// 但频道元数据查询本身失败（非 ErrRecordNotFound）时 fail-closed 返回 500：
	// 故障时后续业务查询同样会失败，fail-open 收益为零，且无法判定目标是否 dm。
	if GetRole(c) == RoleOwner {
		var ownerCh model.Channel
		ownerErr := db.Select("id", "type").First(&ownerCh, "id = ?", channelID).Error
		switch {
		case ownerErr == nil:
			if strings.EqualFold(ownerCh.Type, "dm") {
				return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
			}
			return nil
		case ownerErr == gorm.ErrRecordNotFound:
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		default:
			// DB 故障无法判定是否 dm：鉴权 fail-closed，与「非 ErrRecordNotFound → 500」一致。
			return errors.ErrInternal
		}
	}

	var ch model.Channel
	if err := db.Select("id, type, visibility").First(&ch, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return errors.ErrInternal
	}

	var roleCanView *bool
	if ch.Visibility == "role-specific" {
		var appErr *errors.AppError
		roleCanView, appErr = loadRoleCanView(db, channelID, GetRole(c))
		if appErr != nil {
			return appErr
		}
	}
	return CheckChannelAccess(c, &ch, roleCanView)
}

// ChannelPermission returns a middleware that checks channel-level access
// based on the channel's visibility setting (§6.5A):
//   - public: allow all authenticated members
//   - admin-only: only OWNER/ADMIN
//   - role-specific: consult ChannelRolePermission table for the user's role
//
// OWNER always has full access regardless of visibility.
// The channel ID is read from the ":id" URL parameter. 若频道 ID 不在 :id 上
// （如线程查询由父消息反查频道），请直接调用 CheckChannelAccess/AuthorizeChannel。
func ChannelPermission(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if appErr := AuthorizeChannel(c, db, c.Param("id")); appErr != nil {
			errors.JSONError(c, appErr)
			return
		}
		c.Next()
	}
}
