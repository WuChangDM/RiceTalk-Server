package middleware

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/serverstate"
)

// tokenVersionCache is a small in-memory TTL cache for auth TokenVersion lookups.
// It eliminates one DB query per authenticated request while remaining safe across
// revocation because write paths can invalidate entries and the TTL is short.
type tokenVersionCache struct {
	mu      sync.RWMutex
	entries map[string]*tokenVersionEntry
	ttl     time.Duration
}

type tokenVersionEntry struct {
	version   int64
	expiresAt time.Time
}

var (
	authCache     *tokenVersionCache
	authCacheOnce sync.Once
)

func getAuthCache() *tokenVersionCache {
	authCacheOnce.Do(func() {
		ttl := 30 * time.Second
		if v, err := strconv.Atoi(os.Getenv("RRT_AUTH_CACHE_TTL")); err == nil && v >= 0 {
			ttl = time.Duration(v) * time.Second
		}
		authCache = &tokenVersionCache{
			entries: make(map[string]*tokenVersionEntry),
			ttl:     ttl,
		}
	})
	return authCache
}

func (c *tokenVersionCache) get(userID string) (int64, bool) {
	if c.ttl == 0 {
		return 0, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[userID]
	if !ok || time.Now().After(entry.expiresAt) {
		return 0, false
	}
	return entry.version, true
}

func (c *tokenVersionCache) set(userID string, version int64) {
	if c.ttl == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[userID] = &tokenVersionEntry{version: version, expiresAt: time.Now().Add(c.ttl)}
}

// InvalidateAuthCache removes a user from the TokenVersion cache. Called by
// logout, refresh, password change, and admin revocation paths.
func InvalidateAuthCache(userID string) {
	cache := getAuthCache()
	if cache.ttl == 0 {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.entries, userID)
}

// validateTokenVersion checks the JWT TokenVersion against the cached or DB value.
func validateTokenVersion(db *gorm.DB, userID string, claimVersion int64) *errors.AppError {
	if db == nil {
		return nil
	}
	cache := getAuthCache()
	if cachedVersion, ok := cache.get(userID); ok {
		if claimVersion != cachedVersion {
			return errors.New(errors.AUTH_TOKEN_INVALID, "token revoked")
		}
		return nil
	}
	var user model.User
	if err := db.First(&user, "id = ?", userID).Error; err != nil {
		return errors.ErrUnauthorized
	}
	cache.set(userID, user.TokenVersion)
	if claimVersion != user.TokenVersion {
		return errors.New(errors.AUTH_TOKEN_INVALID, "token revoked")
	}
	return nil
}

// JWTClaims represents the claims in a JWT token
type JWTClaims struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	Type         string `json:"type"` // access or refresh
	TokenVersion int64  `json:"tv"`
	SpaceID      string `json:"space_id,omitempty"`   // M1: current space context (design doc §3.1)
	SessionID    string `json:"session_id,omitempty"` // M1: session identifier for revocation (design doc §3.1)
	jwt.RegisteredClaims
}

// GenerateTokenPair creates access and refresh tokens. Each token carries a
// unique JWT ID so that refresh token rotation cannot accidentally regenerate
// the same token hash within the same clock second.
func GenerateTokenPair(userID, username, email, role string, tokenVersion int64, spaceID, sessionID string, cfg *config.Config) (accessToken, refreshToken string, err error) {
	now := time.Now().UTC()

	accessID, err := crypto.RandomToken(16)
	if err != nil {
		return "", "", err
	}
	refreshID, err := crypto.RandomToken(16)
	if err != nil {
		return "", "", err
	}

	// Access token
	accessClaims := JWTClaims{
		UserID:       userID,
		Username:     username,
		Email:        email,
		Role:         role,
		Type:         "access",
		TokenVersion: tokenVersion,
		SpaceID:      spaceID,
		SessionID:    sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        accessID,
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(cfg.JWTAccessTTL) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "ridgericetalk",
		},
	}
	access := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessToken, err = access.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return "", "", err
	}

	// Refresh token
	refreshClaims := JWTClaims{
		UserID:       userID,
		Type:         "refresh",
		TokenVersion: tokenVersion,
		SessionID:    sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        refreshID,
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(cfg.JWTRefreshTTL) * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "ridgericetalk",
		},
	}
	refresh := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshToken, err = refresh.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}

// ParseToken validates and parses a JWT token.
// M11: 30-second clock skew tolerance per design doc §2.3 — allows clients
// whose clocks are slightly out of sync to still validate tokens around the
// ExpiresAt / NotBefore boundaries.
func ParseToken(tokenString string, cfg *config.Config) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Explicitly require HS256 to prevent algorithm-confusion attacks where
		// an attacker supplies a token signed with a different HMAC variant.
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New(errors.AUTH_TOKEN_INVALID, "unexpected signing method")
		}
		return []byte(cfg.JWTSecret), nil
	}, jwt.WithLeeway(30*time.Second))
	if err != nil {
		if strings.Contains(err.Error(), "token is expired") {
			return nil, errors.ErrTokenExpired
		}
		return nil, errors.ErrInvalidToken
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.ErrInvalidToken
}

// AuthRequired is a gin middleware that validates JWT tokens.
// Supports both Authorization: Bearer header and rrt_token Cookie.
// According to the design doc, initialization check must come before auth check.
func AuthRequired(cfg *config.Config, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !checkServerInitialized(db) {
			c.AbortWithStatusJSON(errors.ErrServerNotInit.Status, errors.FromError(c, errors.ErrServerNotInit))
			return
		}
		if err := authenticateRequest(c, cfg, db); err != nil {
			c.AbortWithStatusJSON(err.Status, errors.FromError(c, err))
			return
		}
		c.Next()
	}
}

// AuthRequiredWithState is like AuthRequired but uses serverstate.Manager as
// the source of truth for server initialization. It still honors the legacy
// package-level flag for test compatibility.
func AuthRequiredWithState(cfg *config.Config, db *gorm.DB, stateMgr *serverstate.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if stateMgr == nil || !stateMgr.IsInitialized() {
			if !checkServerInitialized(db) {
				c.AbortWithStatusJSON(errors.ErrServerNotInit.Status, errors.FromError(c, errors.ErrServerNotInit))
				return
			}
		}
		if err := authenticateRequest(c, cfg, db); err != nil {
			c.AbortWithStatusJSON(err.Status, errors.FromError(c, err))
			return
		}
		c.Next()
	}
}

// checkServerInitialized returns true if the server is initialized either from
// the legacy in-memory flag or by checking for any user in the database.
func checkServerInitialized(db *gorm.DB) bool {
	if IsServerInitialized() {
		return true
	}
	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err == nil && count > 0 {
		MarkServerInitialized()
		return true
	}
	return false
}

// authenticateRequest extracts and validates the access token from the request.
// On success it populates the gin context with user claims and returns nil.
// Token 读取优先级：Authorization: Bearer header > access_token query 参数 > rrt_token cookie。
// query 参数优先于 cookie，避免浏览器携带过期/无效的 rrt_token cookie 时，显式附加在 URL 上的
// access_token（用于 <img>/<video> 等浏览器原生标签）被忽略，导致附件图片/视频返回 401。
func authenticateRequest(c *gin.Context, cfg *config.Config, db *gorm.DB) *errors.AppError {
	tokenString := ""
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			tokenString = parts[1]
		}
	}
	// 从 query 参数 access_token 读取（用于 <img>/<video> 等浏览器原生标签）
	if tokenString == "" {
		if q := c.Query("access_token"); q != "" {
			tokenString = q
		}
	}
	// 兜底：从 rrt_token cookie 读取
	if tokenString == "" {
		cookieToken, err := c.Cookie("rrt_token")
		if err == nil && cookieToken != "" {
			tokenString = cookieToken
		}
	}
	if tokenString == "" {
		return errors.ErrUnauthorized
	}
	claims, err := ParseToken(tokenString, cfg)
	if err != nil {
		if appErr, ok := err.(*errors.AppError); ok {
			return appErr
		}
		return errors.ErrInvalidToken
	}
	if claims.Type != "access" {
		return errors.New(errors.AUTH_TOKEN_INVALID, "invalid token type")
	}
	// Validate TokenVersion against cache or database
	if err := validateTokenVersion(db, claims.UserID, claims.TokenVersion); err != nil {
		return err
	}
	// N22（A4 双端实测发现）：会话撤销必须即时生效。JWT 自包含、TokenVersion 只到
	// 用户粒度——若不校验会话表，revoke-others/单会话撤销后 access token 仍可用至
	// 自然过期，与设置页「这些设备上的当前登录立即失效」承诺（A4 验收标准「互踢」）
	// 矛盾。主键级存在性查询，自托管规模开销可忽略；旧 token 无 session_id 声明
	// 时期豁免（随自然过期退役）。
	if claims.SessionID != "" {
		var sessionCount int64
		if err := db.Model(&model.UserSession{}).
			Where("id = ? AND user_id = ?", claims.SessionID, claims.UserID).
			Count(&sessionCount).Error; err != nil || sessionCount == 0 {
			return errors.New(errors.AUTH_TOKEN_INVALID, "session revoked")
		}
	}
	c.Set("user_id", claims.UserID)
	c.Set("username", claims.Username)
	c.Set("email", claims.Email)
	c.Set("role", claims.Role)
	c.Set("space_id", claims.SpaceID)
	c.Set("session_id", claims.SessionID)
	return nil
}

// OptionalAuth is a gin middleware that validates JWT tokens if present
func OptionalAuth(cfg *config.Config, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Next()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.Next()
			return
		}

		claims, err := ParseToken(parts[1], cfg)
		if err != nil {
			c.Next()
			return
		}

		// Validate TokenVersion against cache or database
		if err := validateTokenVersion(db, claims.UserID, claims.TokenVersion); err != nil {
			c.Next()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("email", claims.Email)
		c.Set("role", claims.Role)
		c.Set("space_id", claims.SpaceID)
		c.Set("session_id", claims.SessionID)
		c.Next()
	}
}

// BotTokenRequired verifies the X-Bot-Token header against the Bot table.
// M22: If the token is valid, sets "bot_id" and "bot_space_id" in context.
// Used for bot control operations that require bot-level authorization.
//
// Design: optional verification mode for backward compatibility —
//   - If X-Bot-Token is absent, fall back to standard user auth (c.Next())
//   - If X-Bot-Token is present but invalid, reject with BOT_TOKEN_INVALID
//   - If X-Bot-Token is present and valid, set bot_id/bot_space_id in context
//
// This allows external bot integrations to use token-based auth while
// preserving the existing user-JWT flow for in-app users.
func BotTokenRequired(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("X-Bot-Token")
		if token == "" {
			// No bot token provided — fall back to user auth (backward compatible)
			c.Next()
			return
		}

		var bot model.Bot
		if err := db.Where("token = ?", token).First(&bot).Error; err != nil {
			errors.JSONError(c, errors.New(errors.BOT_TOKEN_INVALID, "invalid bot token"))
			return
		}

		c.Set("bot_id", bot.ID)
		c.Set("bot_space_id", bot.SpaceID)
		if bot.OutputRoomID != nil {
			c.Set("bot_output_room_id", *bot.OutputRoomID)
		}
		c.Next()
	}
}
