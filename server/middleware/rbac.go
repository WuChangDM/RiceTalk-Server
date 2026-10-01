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

// ChannelPermission returns a middleware that checks channel-level access
// based on the channel's visibility setting (§6.5A):
//   - public: allow all authenticated members
//   - admin-only: only OWNER/ADMIN
//   - role-specific: consult ChannelRolePermission table for the user's role
//
// OWNER always has full access regardless of visibility.
// The channel ID is read from the ":id" URL parameter.
func ChannelPermission(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID := c.Param("id")
		if channelID == "" {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("channel id required"))
			return
		}

		var ch model.Channel
		if err := db.Select("id, type, visibility").First(&ch, "id = ?", channelID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
			} else {
				errors.JSONError(c, errors.ErrInternal)
			}
			return
		}
		if strings.EqualFold(ch.Type, "dm") {
			errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
			return
		}

		userRole := GetRole(c)
		// OWNER has full access to supported channel types.
		if userRole == RoleOwner {
			c.Next()
			return
		}

		switch ch.Visibility {
		case "public", "":
			// allow all authenticated members
			c.Next()
			return
		case "admin-only":
			if userRole == RoleAdmin {
				c.Next()
				return
			}
			errors.JSONError(c, errors.New(errors.CHANNEL_ACCESS_DENIED, "channel requires admin role"))
			return
		case "role-specific":
			// Query ChannelRolePermission for the user's role
			var perm model.ChannelRolePermission
			if err := db.Where("channel_id = ? AND role = ?", channelID, userRole).First(&perm).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					errors.JSONError(c, errors.New(errors.CHANNEL_ACCESS_DENIED, "no permission for this channel"))
					return
				}
				errors.JSONError(c, errors.ErrInternal)
				return
			}
			if !perm.CanView {
				errors.JSONError(c, errors.New(errors.CHANNEL_ACCESS_DENIED, "view permission denied"))
				return
			}
			c.Next()
			return
		default:
			// Unknown visibility — deny by default
			errors.JSONError(c, errors.New(errors.CHANNEL_ACCESS_DENIED, "unknown channel visibility"))
			return
		}
	}
}
