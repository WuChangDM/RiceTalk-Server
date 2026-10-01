package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/core/validator"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
	"ridgericetalk/middleware"
)

// hashRefreshToken computes a SHA-256 hash of a refresh token for secure storage.
func hashRefreshToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// generateInternalUsername derives a unique internal username from an email address.
// The login identity is the email (L4); username is kept as a system-generated,
// non-editable internal identifier (Discord-style: unique ID + display name, with
// a stable handle). The generated value is ASCII-safe (meets usernameRegex:
// alphanumeric/underscore), ≤32 chars, and unique per email.
//
// Example: "alice@example.com" → "alice_1392067018" (email local-part + snowflake tail).
func generateInternalUsername(email string) string {
	at := strings.IndexByte(email, '@')
	base := ""
	if at > 0 {
		base = email[:at]
	}
	// Sanitize: keep only alphanumeric + underscore, lowercase, cap at 20 chars.
	var sb strings.Builder
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			sb.WriteRune(r)
		}
	}
	clean := strings.ToLower(sb.String())
	if len(clean) < 2 {
		clean = "user"
	}
	if len(clean) > 20 {
		clean = clean[:20]
	}
	// Append a snowflake tail (unique per call) so the username is globally unique.
	// idgen.NextString() is the global unique snowflake sequence already used for IDs.
	id := idgen.NextString()
	suffix := id
	if len(suffix) > 10 {
		suffix = suffix[len(suffix)-10:]
	}
	return clean + "_" + suffix
}

// Service handles authentication business logic.
//
// C6 migration: simple User CRUD is delegated to UserRepository (which may be
// wrapped by a cache layer). Transactional and complex operations (password
// history, sessions, bootstrap) still use *gorm.DB directly per the design
// doc's risk-control guidance (§5B / 批次9h 设计方案 §三).
type Service struct {
	db       *gorm.DB
	cfg      *config.Config
	userRepo repositories.UserRepository
	// OnNewLoginAlert 在登录命中「异地登录提醒」判定时被调用（A4-S2，
	// DES-20261001-01 §5.2）。参数：userID（新登录的用户）、exceptSessionID
	// （新会话 ID，广播时跳过——用户自己刚登录的这条连接不需要被提醒）、
	// payload（已脱敏的告警内容：kind/deviceType/deviceName/ip/at）。
	// 由 internal/server 装配到 realtime.Hub 的广播原语；nil 时静默跳过。
	// 回调在 hub 事件循环外以非阻塞投递方式实现，本服务不感知传输细节。
	OnNewLoginAlert func(userID, exceptSessionID string, payload map[string]interface{})
}

// userCacheInvalidator is an optional interface implemented by CachedUserRepository.
// It allows the service to invalidate cache entries when bypassing the Repository
// (e.g. direct s.db writes in Login/ChangePassword/ResetPassword).
type userCacheInvalidator interface {
	InvalidateUser(u *model.User)
	InvalidateUserByID(id string)
}

// invalidateUserCache is a no-op when the repository is not cache-backed.
func (s *Service) invalidateUserCache(user *model.User) {
	if inv, ok := s.userRepo.(userCacheInvalidator); ok {
		inv.InvalidateUser(user)
	}
}

func (s *Service) invalidateUserCacheByID(userID string) {
	if inv, ok := s.userRepo.(userCacheInvalidator); ok {
		inv.InvalidateUserByID(userID)
	}
	middleware.InvalidateAuthCache(userID)
}

// NewService creates a new auth service with a default GORM-backed
// UserRepository (no cache). Use NewServiceWithRepos to inject a cached or
// mock repository.
func NewService(db *gorm.DB, cfg *config.Config) *Service {
	return &Service{
		db:       db,
		cfg:      cfg,
		userRepo: gormrepo.NewGormUserRepository(db),
	}
}

// NewServiceWithRepos creates a new auth service with the given UserRepository.
// Pass a cache-wrapped repository (cacheinfra.NewCachedUserRepository) to
// enable TTL caching per design doc §5B.3.
func NewServiceWithRepos(db *gorm.DB, cfg *config.Config, userRepo repositories.UserRepository) *Service {
	if userRepo == nil {
		userRepo = gormrepo.NewGormUserRepository(db)
	}
	return &Service{db: db, cfg: cfg, userRepo: userRepo}
}

// checkPasswordNotPwned checks the password against the Have I Been Pwned
// breach database when HIBP is enabled. C17: fail-open policy — if the HIBP
// API is unreachable, logs a warning and allows the password, to avoid
// blocking legitimate users due to external service outages.
func (s *Service) checkPasswordNotPwned(password string) error {
	if !s.cfg.HIBPEnabled {
		return nil
	}
	timeout := time.Duration(s.cfg.HIBPTimeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := validator.NewHIBPClient(s.cfg.HIBPAPIURL, timeout)
	pwned, err := client.IsPwned(password)
	if err != nil {
		// C17: fail-open — log warning and allow the password when HIBP is unreachable
		fmt.Printf("WARN: HIBP check failed (fail-open): %v\n", err)
		return nil
	}
	if pwned {
		return errors.New(errors.AUTH_PASSWORD_BREACHED, "this password has been exposed in a data breach; please choose a different one")
	}
	return nil
}

// checkPasswordHistory checks whether newPassword matches any of the user's
// recent password history entries. H10: prevent password reuse.
func (s *Service) checkPasswordHistory(userID, newPassword string) error {
	if s.cfg.PasswordHistoryCount <= 0 {
		return nil
	}
	var histories []model.PasswordHistory
	if err := s.db.Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(s.cfg.PasswordHistoryCount).
		Find(&histories).Error; err != nil {
		return errors.ErrInternal.WithDetails("failed to query password history")
	}
	for _, h := range histories {
		if crypto.CheckPassword(newPassword, h.PasswordHash) {
			return errors.New(errors.AUTH_PASSWORD_REUSED, "new password matches a recently used password")
		}
	}
	return nil
}

// savePasswordHistory saves the old password hash to history and trims excess
// entries. H10: record password history for future reuse checks.
func (s *Service) savePasswordHistory(userID, oldPasswordHash string) error {
	if s.cfg.PasswordHistoryCount <= 0 {
		return nil
	}
	ph := &model.PasswordHistory{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		UserID:       userID,
		PasswordHash: oldPasswordHash,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.db.Create(ph).Error; err != nil {
		return err
	}
	// Trim entries beyond the configured retention count (keep a small buffer)
	keepCount := s.cfg.PasswordHistoryCount
	if err := s.db.Where("user_id = ? AND id NOT IN (?)", userID,
		s.db.Model(&model.PasswordHistory{}).
			Where("user_id = ?", userID).
			Order("created_at DESC").
			Limit(keepCount).
			Select("id")).Delete(&model.PasswordHistory{}).Error; err != nil {
		return err
	}
	return nil
}

// LogSecurityEvent records a security-relevant event to the tamper-evident audit log.
// IP addresses are stored masked (last octet for IPv4, last 80 bits for IPv6) to
// preserve user privacy while still allowing rough source attribution.
func (s *Service) LogSecurityEvent(userID, action, resourceType, resourceID, ip, userAgent string, details map[string]interface{}) error {
	log := &model.SecurityAuditLog{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		UserID:       userID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPMasked:     maskIP(ip),
		CreatedAt:    time.Now().UTC(),
	}
	if userAgent != "" {
		details["userAgent"] = userAgent
	}
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err == nil {
			log.DetailsJSON = string(b)
		}
	}
	return s.db.Create(log).Error
}

// maskIP anonymizes an IP address by zeroing the last octet (IPv4) or the last
// 80 bits (IPv6).
func maskIP(ip string) string {
	if ip == "" {
		return ""
	}
	// Very basic masking: drop everything after the last dot for IPv4-like strings.
	if idx := len(ip) - 1; idx >= 0 {
		for i := idx; i >= 0; i-- {
			if ip[i] == '.' {
				return ip[:i] + ".0"
			}
			if ip[i] == ':' {
				// IPv6: keep first 4 hextets, mask the rest.
				parts := strings.Split(ip, ":")
				if len(parts) > 4 {
					return strings.Join(parts[:4], ":") + ":xxxx:xxxx:xxxx:xxxx"
				}
				return ip
			}
		}
	}
	return ip
}

// SecurityQuestionItem represents one security question + answer pair.
type SecurityQuestionItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// RegisterRequest represents a registration request
type RegisterRequest struct {
	Username          string                 `json:"username"`
	Email             string                 `json:"email"`
	Password          string                 `json:"password"`
	DisplayName       string                 `json:"displayName"`
	SecurityQuestions []SecurityQuestionItem `json:"securityQuestions"`
	// A4-S1：注册自动登录会话的来源网络信息（服务端填充，客户端不可自报）。
	IP        string `json:"-"`
	UserAgent string `json:"-"`
}

// ForgotPasswordRequest requests the security questions for an account.
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPasswordResponse returns the security questions for an account.
type ForgotPasswordResponse struct {
	Questions []string `json:"questions"`
}

// VerifySecurityQuestionsRequest verifies security question answers.
type VerifySecurityQuestionsRequest struct {
	Email   string                 `json:"email"`
	Answers []SecurityQuestionItem `json:"answers"`
}

// VerifySecurityQuestionsResponse returns a one-time reset token.
type VerifySecurityQuestionsResponse struct {
	ResetToken string `json:"resetToken"`
}

// ResetPasswordRequest resets the password using a reset token.
// Email is optional (added 2026-07-07): when provided, the token lookup is
// scoped to that user, avoiding an O(n) bcrypt scan over all unexpired tokens.
// Older clients that omit Email fall back to the original full-scan behavior.
type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
	Email       string `json:"email,omitempty"`
}

// LoginRequest represents a login request
type LoginRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"` // L4: email-based login (design doc §10.3); Username takes precedence for backward compatibility
	Password string `json:"password"`
	// S-2 多设备标识（可选）：客户端登录时上报的设备类型与设备名，写入会话行
	// 用于「登录设备」列表展示；缺省为空串，服务端截断防滥用。
	DeviceType string `json:"deviceType,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
	// A4-S1：会话来源网络信息。json:"-"——服务端 handler 从连接上下文填充
	//（ClientIP / User-Agent 请求头），绝不允许客户端 JSON 自报伪造。
	IP        string `json:"-"`
	UserAgent string `json:"-"`
}

// SessionDevice carries the sanitized device identity attached to a session
// (S-2 多设备标识). Zero value means "no device info reported".
// A4-S1：IP/UserAgent 是服务端采集的会话网络上下文，随设备标识一并传递
// 给 generateTokenPair 落库；空串表示未采集（如内部路径创建的会话）。
type SessionDevice struct {
	Type      string
	Name      string
	IP        string
	UserAgent string
}

// Device field length caps (mirrors model.UserSession GORM size tags).
const (
	maxDeviceTypeLen = 32
	maxDeviceNameLen = 64
	// A4-S1：user_agent 列宽 256；ip 列宽 45（IPv6 文本上限，见迁移 000038）。
	maxUserAgentLen = 256
	maxClientIPlen  = 45
)

// sanitizeUserAgent 规整客户端 User-Agent 请求头：去掉首尾空白与控制字符后
// 按 rune 截断到 256（与设备字段同款 rune 截断口径，CJK 不产生半字符）。
func sanitizeUserAgent(userAgent string) string {
	trimmed := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1 // drop control characters
		}
		return r
	}, strings.TrimSpace(userAgent))
	runes := []rune(trimmed)
	if len(runes) > maxUserAgentLen {
		return string(runes[:maxUserAgentLen])
	}
	return trimmed
}

// sanitizeClientIP 规整服务端解析出的客户端 IP（防御性截断到列宽；
// 数值合法性由 gin 的 ClientIP 保证，这里不做格式校验）。
func sanitizeClientIP(ip string) string {
	if len(ip) > maxClientIPlen {
		return ip[:maxClientIPlen]
	}
	return ip
}

// sessionMetaFromRequest 把 handler 侧采集的网络上下文并入会话设备标识。
func sessionMetaFromRequest(device SessionDevice, ip, userAgent string) SessionDevice {
	device.IP = sanitizeClientIP(ip)
	device.UserAgent = sanitizeUserAgent(userAgent)
	return device
}

// sanitizeSessionDevice trims and truncates client-reported device fields.
// Truncation counts runes (not bytes) so CJK device names stay inside the
// column width after UTF-8 encoding.
func sanitizeSessionDevice(deviceType, deviceName string) SessionDevice {
	trimRunes := func(s string, max int) string {
		s = strings.TrimSpace(s)
		runes := []rune(s)
		if len(runes) > max {
			return string(runes[:max])
		}
		return s
	}
	return SessionDevice{
		Type: trimRunes(deviceType, maxDeviceTypeLen),
		Name: trimRunes(deviceName, maxDeviceNameLen),
	}
}

// SpaceInfo contains the user's current space info for login/register responses.
// Per design doc §10.2, §10.3: response data includes user + space (id, name, role).
type SpaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// TokenResponse represents a token response
type TokenResponse struct {
	AccessToken  string      `json:"accessToken"`
	RefreshToken string      `json:"refreshToken"`
	ExpiresIn    int         `json:"expiresIn"` // seconds
	User         *model.User `json:"user"`
	Space        *SpaceInfo  `json:"space,omitempty"`
}

// predefinedSecurityQuestions is the allowed pool of security questions.
// Users must pick 1-2 questions from this list when registering.
var predefinedSecurityQuestions = []string{
	"您父亲的生日是哪一天？",
	"您母亲的娘家姓是什么？",
	"您的第一只宠物叫什么名字？",
	"您小学班主任的姓氏是什么？",
	"您出生城市是哪里？",
	"您最喜欢的书是什么？",
	"您学会驾驶的年份是哪一年？",
	"您童年最要好的朋友叫什么名字？",
}

// isPredefinedQuestion checks whether a question text is in the allowed pool.
func isPredefinedQuestion(q string) bool {
	for _, pq := range predefinedSecurityQuestions {
		if pq == q {
			return true
		}
	}
	return false
}

// normalizeSecurityAnswer normalizes an answer before hashing/comparison:
// trims surrounding whitespace and lowercases ASCII characters.
// Full Unicode NFKC normalization is intentionally NOT performed to avoid
// pulling in golang.org/x/text as a dependency; if answers contain full-width
// or composed Unicode forms, consider adding the import later.
func normalizeSecurityAnswer(a string) string {
	return strings.ToLower(strings.TrimSpace(a))
}

// validateSecurityQuestions validates the security questions provided during registration.
func validateSecurityQuestions(items []SecurityQuestionItem) error {
	// 安全问题改为可选：未提供时跳过验证，用户可在登录后自行设置
	if len(items) == 0 {
		return nil
	}
	if len(items) > 2 {
		return errors.New(errors.AUTH_SECURITY_QUESTION_INVALID, "at most 2 security questions are allowed")
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Question == "" || item.Answer == "" {
			return errors.New(errors.AUTH_SECURITY_QUESTION_INVALID, "question and answer are required")
		}
		if !isPredefinedQuestion(item.Question) {
			return errors.New(errors.AUTH_SECURITY_QUESTION_INVALID, "invalid security question")
		}
		if _, ok := seen[item.Question]; ok {
			return errors.New(errors.AUTH_SECURITY_QUESTION_DUPLICATE, "duplicate security question")
		}
		seen[item.Question] = struct{}{}
		if len(item.Answer) < 2 || len(item.Answer) > 64 {
			return errors.New(errors.AUTH_SECURITY_QUESTION_INVALID, "answer must be 2-64 characters")
		}
	}
	return nil
}

// saveSecurityQuestions persists a user's security question answer hashes.
func (s *Service) saveSecurityQuestions(tx *gorm.DB, userID string, items []SecurityQuestionItem) error {
	for _, item := range items {
		hash, err := crypto.HashPassword(normalizeSecurityAnswer(item.Answer))
		if err != nil {
			return err
		}
		sq := &model.SecurityQuestion{
			ID:         idgen.GenerateID(idgen.PrefixUser),
			UserID:     userID,
			Question:   item.Question,
			AnswerHash: hash,
		}
		if err := tx.Create(sq).Error; err != nil {
			return err
		}
	}
	return nil
}

// Register creates a new user account
func (s *Service) Register(req *RegisterRequest) (*TokenResponse, error) {
	// Registration can be disabled via server config (e.g., after initial setup).
	if !s.cfg.AllowRegister {
		return nil, errors.New(errors.AUTH_FORBIDDEN, "registration is disabled")
	}

	// Validate input
	if !validator.ValidateEmail(req.Email) {
		return nil, errors.ErrBadRequest.WithDetails("invalid email format")
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		return nil, errors.ErrBadRequest.WithDetails("display name is required")
	}
	if valid, reason := validator.ValidatePassword(req.Password); !valid {
		return nil, errors.ErrBadRequest.WithDetails(reason)
	}
	if err := s.checkPasswordNotPwned(req.Password); err != nil {
		return nil, err
	}
	if err := validateSecurityQuestions(req.SecurityQuestions); err != nil {
		return nil, err
	}

	// Check if email exists
	var existingUser model.User
	if err := s.db.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
		return nil, errors.New(errors.AUTH_EMAIL_EXISTS, "email already registered")
	}

	// FIX-2026-0808-01: display name must be unique within the default space.
	// New users auto-join the first (default) space; resolve that space and check
	// its existing members for a name collision (case-insensitive, trimmed).
	var spaceForNameCheck model.Space
	if err := s.db.Order("created_at ASC, id ASC").First(&spaceForNameCheck).Error; err == nil {
		var dup int64
		if err := s.db.Table("users").
			Joins("JOIN memberships ON memberships.user_id = users.id").
			Where("memberships.space_id = ? AND LOWER(TRIM(users.display_name)) = ?", spaceForNameCheck.ID, strings.ToLower(displayName)).
			Count(&dup).Error; err == nil && dup > 0 {
			return nil, errors.New(errors.AUTH_DISPLAY_NAME_EXISTS, "display name already used in this space")
		}
	}

	// Hash password
	passwordHash, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to hash password")
	}

	// Create user. Username is a system-generated internal identifier (login uses email).
	user := &model.User{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		Username:     generateInternalUsername(req.Email),
		Email:        req.Email,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		Role:         middleware.RoleMember,
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		if err := s.saveSecurityQuestions(tx, user.ID, req.SecurityQuestions); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to create user")
	}

	// Auto-join the default space. Multiple spaces can exist (e.g. leftover
	// bootstrap attempts); "first" must be deterministic — ORDER BY created_at
	// picks the originally bootstrapped space instead of whichever row happens
	// to come first physically.
	var defaultSpace model.Space
	if err := s.db.Order("created_at ASC, id ASC").First(&defaultSpace).Error; err == nil {
		membership := &model.Membership{
			ID:      idgen.GenerateID(idgen.PrefixUser),
			UserID:  user.ID,
			SpaceID: defaultSpace.ID,
			Role:    middleware.RoleMember,
		}
		s.db.Create(membership)
	}

	// Generate tokens (registration auto-login; A4-S1: record the network
	// context captured by the handler so the device list shows a source IP).
	return s.generateTokenPair(user, sessionMetaFromRequest(SessionDevice{}, req.IP, req.UserAgent))
}

// Login authenticates a user by email and returns tokens.
func (s *Service) Login(req *LoginRequest) (*TokenResponse, error) {
	// Login identity is the email (L4). Username is an internal identifier, not a login key.
	loginID := strings.TrimSpace(req.Email)
	if loginID == "" {
		loginID = req.Username // backward compatibility for old clients that sent email in username field
	}
	if loginID == "" {
		return nil, errors.New(errors.AUTH_INVALID_CREDENTIALS, "email is required")
	}
	// Find user by email
	var user model.User
	if err := s.db.Where("email = ?", loginID).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.AUTH_INVALID_CREDENTIALS, "invalid email or password")
		}
		return nil, errors.ErrInternal
	}

	// Verify password
	if !crypto.CheckPassword(req.Password, user.PasswordHash) {
		return nil, errors.New(errors.AUTH_INVALID_CREDENTIALS, "invalid email or password")
	}

	// Check if user is active (disabled users cannot login)
	if !user.IsActive {
		return nil, errors.New(errors.AUTH_USER_DISABLED, "account has been disabled")
	}

	// Update last login
	now := time.Now().UTC()
	user.LastLoginAt = &now
	if err := s.db.Save(&user).Error; err != nil {
		return nil, errors.ErrInternal
	}
	s.invalidateUserCache(&user)

	// A4-S2（DES-20261001-01 §5.2）：异地登录提醒。在写入新会话行之前查询
	// 该用户既有活跃会话（此时查询结果天然不含本次登录），若全部既有会话
	// 的 IP 与本次登录 IP 均不同网段，则向该用户除新会话外的在线连接广播
	// security_alert。查询失败不阻断登录（宁漏发不误伤登录主流程）。
	device := sessionMetaFromRequest(
		sanitizeSessionDevice(req.DeviceType, req.DeviceName), req.IP, req.UserAgent)
	var existing []sessionRow
	if err := s.db.Model(&model.UserSession{}).
		Where("user_id = ? AND expires_at > ?", user.ID, now).
		Select("ip").
		Find(&existing).Error; err != nil {
		existing = nil
	}
	alertLogin := shouldAlertNewLogin(existing, device.IP)

	// Generate tokens
	resp, err := s.generateTokenPair(&user, device)
	if err != nil {
		return nil, err
	}

	if alertLogin && s.OnNewLoginAlert != nil {
		// TokenResponse 不携带 sessionID；从 access token 声明中取回本次
		// 登录的会话 ID，用于广播时排除刚登录的这条连接。
		newSessionID := ""
		if claims, parseErr := middleware.ParseToken(resp.AccessToken, s.cfg); parseErr == nil {
			newSessionID = claims.SessionID
		}
		payload := map[string]interface{}{
			"kind":       "new_login",
			"deviceType": device.Type,
			"deviceName": device.Name,
			"ip":         desensitizeIPForAlert(device.IP),
			"at":         now.UTC().Format(time.RFC3339),
		}
		s.OnNewLoginAlert(user.ID, newSessionID, payload)
	}

	return resp, nil
}

// AdminLogin authenticates an admin/owner user.
func (s *Service) AdminLogin(req *LoginRequest) (*TokenResponse, error) {
	return s.Login(req)
}

// Logout invalidates a user's session and increments TokenVersion for global logout
func (s *Service) Logout(userID, refreshToken string) error {
	tokenHash := hashRefreshToken(refreshToken)

	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete session by token hash
		res := tx.Where("user_id = ? AND token_hash = ?", userID, tokenHash).Delete(&model.UserSession{})
		if res.Error != nil {
			return res.Error
		}

		// Only increment TokenVersion if the session actually existed, preventing
		// spurious invalidation of the user's other sessions on bogus requests.
		if res.RowsAffected == 0 {
			return nil
		}

		// Increment TokenVersion to invalidate all existing tokens globally
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Update("token_version", gorm.Expr("token_version + 1")).Error; err != nil {
			return err
		}
		middleware.InvalidateAuthCache(userID)
		return nil
	})
}

// RefreshToken generates a new access token from a refresh token.
// device is optional (may be nil): when provided it overrides the device
// identity carried over from the old session row (S-2); when nil the old
// session's device fields are preserved across rotation. The new row's
// LastActiveAt is bumped to now — refresh is the deterministic liveness
// signal for the「登录设备」list.
func (s *Service) RefreshToken(refreshToken string, device *SessionDevice) (*TokenResponse, error) {
	// Parse refresh token
	claims, err := middleware.ParseToken(refreshToken, s.cfg)
	if err != nil {
		return nil, err
	}

	if claims.Type != "refresh" {
		return nil, errors.ErrInvalidToken
	}

	// Verify session exists by hash
	tokenHash := hashRefreshToken(refreshToken)
	var session model.UserSession
	if err := s.db.Where("token_hash = ?", tokenHash).First(&session).Error; err != nil {
		return nil, errors.ErrInvalidToken
	}

	// Check if session is expired
	if time.Now().UTC().After(session.ExpiresAt) {
		if err := s.db.Delete(&session).Error; err != nil {
			return nil, errors.ErrInternal
		}
		return nil, errors.ErrTokenExpired
	}

	// Get user
	var user model.User
	if err := s.db.First(&user, "id = ?", claims.UserID).Error; err != nil {
		return nil, errors.ErrInvalidToken
	}

	// Validate TokenVersion to prevent revoked tokens from being refreshed
	if claims.TokenVersion != user.TokenVersion {
		return nil, errors.New(errors.AUTH_TOKEN_INVALID, "token has been revoked")
	}
	middleware.InvalidateAuthCache(claims.UserID)

	// Resolve the device identity for the rotated session row: explicit
	// client report wins, otherwise inherit the old row (keeps「登录设备」
	// stable across silent token refreshes from old clients).
	// A4-S1：网络上下文（IP/UA）不做继承——每次 refresh 都用当前请求的
	// 采集值覆盖（「最近活跃 IP」语义；移动网络抖动可接受）。device 为 nil
	// 时（内部路径）保持空串。
	rotatedDevice := SessionDevice{Type: session.DeviceType, Name: session.DeviceName}
	if device != nil {
		if device.Type != "" || device.Name != "" {
			rotatedDevice.Type = device.Type
			rotatedDevice.Name = device.Name
		}
		rotatedDevice.IP = device.IP
		rotatedDevice.UserAgent = device.UserAgent
	}

	// Wrap refresh token rotation in a transaction: delete old session and create new session atomically.
	// RowsAffected check prevents concurrent reuse of the same refresh token (TOCTOU).
	var resp *TokenResponse
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Delete old session to prevent token reuse
		res := tx.Where("token_hash = ?", tokenHash).Delete(&model.UserSession{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Another request already consumed this refresh token.
			return errors.ErrInvalidToken
		}
		// Generate new tokens inside transaction
		var err error
		resp, err = s.generateTokenPair(&user, rotatedDevice, tx)
		return err
	})
	if err != nil {
		if appErr, ok := err.(*errors.AppError); ok {
			return nil, appErr
		}
		return nil, errors.ErrInternal
	}
	return resp, nil
}

// GetUserByLoginID looks up a user by email (login identity). Username is an
// internal identifier and not a login key; kept as fallback for backward
// compatibility with any caller passing a legacy username.
func (s *Service) GetUserByLoginID(login string) (*model.User, error) {
	// C6: delegate to UserRepository (cache-wrapped in production).
	user, err := s.userRepo.GetByEmail(context.Background(), login)
	if err == nil {
		return user, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, errors.ErrInternal
	}
	// Fall back to username lookup (legacy callers / old data paths).
	user, err = s.userRepo.GetByUsername(context.Background(), login)
	if err == nil {
		return user, nil
	}
	if err == gorm.ErrRecordNotFound {
		// L6: return a dedicated not-found error code so callers can distinguish
		// "user does not exist" from "invalid credentials" (design doc §2.2).
		return nil, errors.New(errors.AUTH_USER_NOT_FOUND, "user not found")
	}
	return nil, errors.ErrInternal
}

// GetUserByID retrieves a user by ID
func (s *Service) GetUserByID(userID string) (*model.User, error) {
	// C6: delegate to UserRepository (cache-wrapped in production).
	user, err := s.userRepo.GetByID(context.Background(), userID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return user, nil
}

// CheckServerInitialized checks if the server has been initialized
func (s *Service) CheckServerInitialized() (bool, error) {
	var count int64
	if err := s.db.Model(&model.User{}).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateBootstrapToken creates a one-time bootstrap token for initial setup.
// Stores only bcrypt hash in DB; returns the plaintext token once to the caller.
func (s *Service) CreateBootstrapToken() (string, error) {
	token, err := crypto.RandomToken(32)
	if err != nil {
		return "", err
	}

	tokenHash, err := crypto.HashPassword(token)
	if err != nil {
		return "", err
	}

	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	bt := &model.AdminBootstrapToken{
		ID:        idgen.GenerateID(idgen.PrefixUser),
		TokenHash: tokenHash,
		ExpiresAt: &expiresAt,
	}

	if err := s.db.Create(bt).Error; err != nil {
		return "", err
	}

	return token, nil
}

// findValidBootstrapToken finds an unused, non-expired token that matches the provided plaintext token.
func (s *Service) findValidBootstrapToken(token string) (*model.AdminBootstrapToken, error) {
	var tokens []model.AdminBootstrapToken
	if err := s.db.Where("used = ?", false).Find(&tokens).Error; err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range tokens {
		if tokens[i].ExpiresAt != nil && now.After(*tokens[i].ExpiresAt) {
			continue // expired
		}
		if crypto.CheckPassword(token, tokens[i].TokenHash) {
			return &tokens[i], nil
		}
	}
	// Check if the token matches a used token (to give a more specific error)
	var usedTokens []model.AdminBootstrapToken
	if err := s.db.Where("used = ?", true).Find(&usedTokens).Error; err == nil {
		for i := range usedTokens {
			if crypto.CheckPassword(token, usedTokens[i].TokenHash) {
				return nil, errors.New(errors.AUTH_BOOTSTRAP_TOKEN_USED, "bootstrap token has already been used")
			}
		}
	}
	return nil, errors.New(errors.AUTH_BOOTSTRAP_TOKEN_INVALID, "invalid bootstrap token")
}

// VerifyBootstrapToken checks if a bootstrap token is valid without consuming it.
func (s *Service) VerifyBootstrapToken(token string) error {
	_, err := s.findValidBootstrapToken(token)
	return err
}

// ValidateBootstrapToken checks and consumes a bootstrap token.
func (s *Service) ValidateBootstrapToken(token string) error {
	bt, err := s.findValidBootstrapToken(token)
	if err != nil {
		return err
	}

	bt.Used = true
	now := time.Now().UTC()
	bt.UsedAt = &now
	if err := s.db.Save(bt).Error; err != nil {
		return err
	}
	// Remove the bootstrap token file if it exists
	_ = os.Remove("./storage/.bootstrap_token")
	return nil
}

// generateTokenPair creates access and refresh tokens and stores the session.
// device is the sanitized client device identity (S-2, may be zero value).
func (s *Service) generateTokenPair(user *model.User, device SessionDevice, tx ...*gorm.DB) (*TokenResponse, error) {
	db := s.db
	if len(tx) > 0 && tx[0] != nil {
		db = tx[0]
	}

	// Query user's current space info first so we can embed it in the token (M1).
	var spaceInfo *SpaceInfo
	var membership model.Membership
	if err := db.Where("user_id = ?", user.ID).First(&membership).Error; err == nil {
		var space model.Space
		if err := db.Where("id = ?", membership.SpaceID).First(&space).Error; err == nil {
			spaceInfo = &SpaceInfo{
				ID:   space.ID,
				Name: space.Name,
				Role: membership.Role,
			}
		}
	}

	// M1: pre-generate session ID so it can be embedded in the JWT claims.
	spaceID := ""
	if spaceInfo != nil {
		spaceID = spaceInfo.ID
	}
	sessionID := idgen.GenerateID(idgen.PrefixSession)

	accessToken, refreshToken, err := middleware.GenerateTokenPair(
		user.ID, user.Username, user.Email, user.Role, user.TokenVersion, spaceID, sessionID, s.cfg,
	)
	if err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to generate tokens")
	}

	// Store refresh session (hash the token before storage)
	// Per design doc §8.1: Admin sessions expire in AdminSessionTTL minutes (default 15).
	isAdmin := user.Role == middleware.RoleOwner || user.Role == middleware.RoleAdmin
	sessionTTL := time.Duration(s.cfg.JWTRefreshTTL) * 24 * time.Hour
	if isAdmin {
		sessionTTL = time.Duration(s.cfg.AdminSessionTTL) * time.Minute
	}
	now := time.Now().UTC()
	session := &model.UserSession{
		ID:             sessionID,
		UserID:         user.ID,
		TokenHash:      hashRefreshToken(refreshToken),
		ExpiresAt:      now.Add(sessionTTL),
		IsAdminSession: isAdmin,
		DeviceType:     device.Type,
		DeviceName:     device.Name,
		IP:             device.IP,
		UserAgent:      device.UserAgent,
		LastActiveAt:   now,
	}

	if err := db.Create(session).Error; err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to create session")
	}

	return &TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    s.cfg.JWTAccessTTL * 60,
		User:         user,
		Space:        spaceInfo,
	}, nil
}

// GetBootstrapStatus returns whether there is a valid (unused, non-expired) bootstrap token.
func (s *Service) GetBootstrapStatus() (bool, error) {
	var count int64
	now := time.Now().UTC()
	if err := s.db.Model(&model.AdminBootstrapToken{}).
		Where("used = ? AND (expires_at IS NULL OR expires_at > ?)", false, now).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateOwner creates the first owner account during bootstrap.
// spaceName is optional; defaults to "RidgeRiceTalk" if empty.
func (s *Service) CreateOwner(req *RegisterRequest, bootstrapToken, spaceName string) (*TokenResponse, error) {
	// N24：bootstrap 表单（admin 初始化页/脚本调用）只有 用户名/邮箱/密码/空间名称
	// 四个字段，displayName 缺省取 username —— 不再强制要求，避免全新部署
	// 初始化必 400（普通注册路径 Register 仍要求 displayName，不受影响）。
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = req.Username
	}
	// Validate input first (before consuming the token)
	if !validator.ValidateEmail(req.Email) {
		return nil, errors.ErrBadRequest.WithDetails("invalid email format")
	}
	if valid, reason := validator.ValidatePassword(req.Password); !valid {
		return nil, errors.ErrBadRequest.WithDetails(reason)
	}
	// Security questions must also be valid before consuming the one-time token.
	if err := validateSecurityQuestions(req.SecurityQuestions); err != nil {
		return nil, err
	}

	// Validate and consume bootstrap token
	if err := s.ValidateBootstrapToken(bootstrapToken); err != nil {
		return nil, err
	}

	return s.createOwnerInternal(req, spaceName)
}

// CreateOwnerFromEnv creates the owner via environment variable pre-configuration,
// skipping the bootstrap token flow (H1). Only callable when the server is not initialized.
func (s *Service) CreateOwnerFromEnv(req *RegisterRequest, spaceName string) (*TokenResponse, error) {
	// Validate input
	if !validator.ValidateEmail(req.Email) {
		return nil, errors.ErrBadRequest.WithDetails("invalid email format")
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		return nil, errors.ErrBadRequest.WithDetails("display name is required")
	}
	if valid, reason := validator.ValidatePassword(req.Password); !valid {
		return nil, errors.ErrBadRequest.WithDetails(reason)
	}

	// Pre-check: server must not be already initialized
	initialized, err := s.CheckServerInitialized()
	if err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to check server initialization")
	}
	if initialized {
		return nil, errors.New(errors.AUTH_ALREADY_INITIALIZED, "server already initialized")
	}

	return s.createOwnerInternal(req, spaceName)
}

// createOwnerInternal is the shared core logic for owner creation used by both
// CreateOwner (bootstrap token flow) and CreateOwnerFromEnv (env pre-configuration flow).
func (s *Service) createOwnerInternal(req *RegisterRequest, spaceName string) (*TokenResponse, error) {
	// Validate security questions (owner must set them too)
	if err := validateSecurityQuestions(req.SecurityQuestions); err != nil {
		return nil, err
	}

	// Hash password
	passwordHash, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, errors.ErrInternal
	}

	// Create owner user. Username is a system-generated internal identifier.
	user := &model.User{
		ID:            idgen.GenerateID(idgen.PrefixUser),
		Username:      generateInternalUsername(req.Email),
		Email:         req.Email,
		PasswordHash:  passwordHash,
		DisplayName:   strings.TrimSpace(req.DisplayName),
		Role:          middleware.RoleOwner,
		EmailVerified: true,
	}

	if spaceName == "" {
		spaceName = "RidgeRiceTalk"
	}

	// Wrap initialization in a transaction with double-check to prevent race conditions
	var resp *TokenResponse
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Double-check user count inside transaction to prevent concurrent owner creation
		var count int64
		if err := tx.Model(&model.User{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New(errors.AUTH_ALREADY_INITIALIZED, "server already initialized")
		}

		if err := tx.Create(user).Error; err != nil {
			return errors.ErrInternal.WithDetails(fmt.Sprintf("failed to create owner: %v", err))
		}

		// Save owner security questions
		if err := s.saveSecurityQuestions(tx, user.ID, req.SecurityQuestions); err != nil {
			return errors.ErrInternal.WithDetails(fmt.Sprintf("failed to save owner security questions: %v", err))
		}

		// Create default space
		space := &model.Space{
			ID:      idgen.GenerateID(idgen.PrefixSpace),
			Name:    "dm",
			OwnerID: user.ID,
		}
		if err := tx.Create(space).Error; err != nil {
			return errors.ErrInternal.WithDetails(fmt.Sprintf("failed to create default space: %v", err))
		}

		// Add owner as a member of the default space (OWNER role)
		ownerMembership := &model.Membership{
			ID:      idgen.GenerateID(idgen.PrefixUser),
			UserID:  user.ID,
			SpaceID: space.ID,
			Role:    middleware.RoleOwner,
		}
		if err := tx.Create(ownerMembership).Error; err != nil {
			return errors.ErrInternal.WithDetails(fmt.Sprintf("failed to create owner membership: %v", err))
		}

		// Create default channels
		channels := []*model.Channel{
			{ID: idgen.GenerateID(idgen.PrefixChannel), SpaceID: space.ID, Name: "文字频道", Type: "TEXT", Position: 0},
			{ID: idgen.GenerateID(idgen.PrefixChannel), SpaceID: space.ID, Name: "语音频道", Type: "VOICE", Position: 1},
		}
		for _, ch := range channels {
			if err := tx.Create(ch).Error; err != nil {
				return errors.ErrInternal.WithDetails(fmt.Sprintf("failed to create default channel: %v", err))
			}
		}

		// Generate tokens inside the transaction (bootstrap owner login: no device info)
		var err error
		resp, err = s.generateTokenPair(user, SessionDevice{}, tx)
		return err
	})
	if err != nil {
		return nil, err
	}

	// Mark server as initialized in memory cache
	middleware.MarkServerInitialized()

	return resp, nil
}

// ForgotPassword returns the security questions for the given email.
// To prevent email enumeration, this always returns 200 with an empty
// questions list when the email is not registered.
func (s *Service) ForgotPassword(req *ForgotPasswordRequest) (*ForgotPasswordResponse, error) {
	if req.Email == "" || !validator.ValidateEmail(req.Email) {
		return nil, errors.ErrBadRequest.WithDetails("invalid email format")
	}

	var user model.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &ForgotPasswordResponse{Questions: []string{}}, nil
		}
		return nil, errors.ErrInternal
	}

	var questions []model.SecurityQuestion
	if err := s.db.Where("user_id = ?", user.ID).Find(&questions).Error; err != nil {
		return nil, errors.ErrInternal
	}

	out := make([]string, 0, len(questions))
	for _, q := range questions {
		out = append(out, q.Question)
	}
	return &ForgotPasswordResponse{Questions: out}, nil
}

// VerifySecurityQuestions verifies the answers and issues a one-time reset token.
func (s *Service) VerifySecurityQuestions(req *VerifySecurityQuestionsRequest) (*VerifySecurityQuestionsResponse, error) {
	if req.Email == "" || !validator.ValidateEmail(req.Email) {
		return nil, errors.ErrBadRequest.WithDetails("invalid email format")
	}
	if len(req.Answers) == 0 {
		return nil, errors.ErrBadRequest.WithDetails("answers are required")
	}

	var user model.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.AUTH_SECURITY_QUESTION_INCORRECT, "security question answer incorrect")
		}
		return nil, errors.ErrInternal
	}

	var stored []model.SecurityQuestion
	if err := s.db.Where("user_id = ?", user.ID).Find(&stored).Error; err != nil {
		return nil, errors.ErrInternal
	}
	if len(stored) == 0 {
		return nil, errors.New(errors.AUTH_SECURITY_QUESTION_NOT_SET, "security questions are not set for this account")
	}

	// Build map of stored questions to answers.
	storedMap := make(map[string]string, len(stored))
	for _, sq := range stored {
		storedMap[sq.Question] = sq.AnswerHash
	}

	// Verify all submitted answers match their questions and are correct.
	for _, ans := range req.Answers {
		hash, ok := storedMap[ans.Question]
		if !ok {
			return nil, errors.New(errors.AUTH_SECURITY_QUESTION_INCORRECT, "security question answer incorrect")
		}
		if !crypto.CheckPassword(normalizeSecurityAnswer(ans.Answer), hash) {
			return nil, errors.New(errors.AUTH_SECURITY_QUESTION_INCORRECT, "security question answer incorrect")
		}
	}

	// Issue a one-time reset token.
	token, err := crypto.RandomToken(32)
	if err != nil {
		return nil, errors.ErrInternal
	}
	tokenHash, err := crypto.HashPassword(token)
	if err != nil {
		return nil, errors.ErrInternal
	}

	prt := &model.PasswordResetToken{
		ID:        idgen.GenerateID(idgen.PrefixUser),
		UserID:    user.ID,
		TokenHash: tokenHash,
		Method:    "security_question",
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}
	if err := s.db.Create(prt).Error; err != nil {
		return nil, errors.ErrInternal
	}

	return &VerifySecurityQuestionsResponse{ResetToken: token}, nil
}

// ResetPassword resets the password using a one-time reset token.
func (s *Service) ResetPassword(req *ResetPasswordRequest) error {
	if req.Token == "" {
		return errors.New(errors.AUTH_RESET_TOKEN_INVALID, "reset token is required")
	}
	if valid, reason := validator.ValidatePassword(req.NewPassword); !valid {
		return errors.ErrBadRequest.WithDetails(reason)
	}

	// Build the candidate token query. We can't query by bcrypt hash directly,
	// so we load candidate rows and CompareHashAndPassword each one.
	// When req.Email is provided (newer clients), scope the query to that
	// user's tokens — typically 1 row — to avoid an O(n) bcrypt scan over all
	// unexpired tokens in the system (DoS hardening for public deployments).
	now := time.Now().UTC()
	query := s.db.Where("used_at IS NULL AND expires_at > ?", now)
	if req.Email != "" {
		var user model.User
		if err := s.db.Select("id").Where("email = ?", req.Email).First(&user).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				// Email not registered — no token can match. Return the same
				// error as "token invalid" to avoid email enumeration.
				return errors.New(errors.AUTH_RESET_TOKEN_INVALID, "reset token is invalid or expired")
			}
			return errors.ErrInternal
		}
		query = query.Where("user_id = ?", user.ID)
	}

	var tokens []model.PasswordResetToken
	if err := query.Find(&tokens).Error; err != nil {
		return errors.ErrInternal
	}

	var matched *model.PasswordResetToken
	for i := range tokens {
		if crypto.CheckPassword(req.Token, tokens[i].TokenHash) {
			matched = &tokens[i]
			break
		}
	}
	if matched == nil {
		return errors.New(errors.AUTH_RESET_TOKEN_INVALID, "reset token is invalid or expired")
	}

	// Check password history.
	if err := s.checkPasswordHistory(matched.UserID, req.NewPassword); err != nil {
		return err
	}

	passwordHash, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		return errors.ErrInternal
	}

	// Update password, increment token version, mark token used.
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Where("id = ?", matched.UserID).First(&user).Error; err != nil {
			return err
		}
		oldHash := user.PasswordHash

		if err := tx.Model(&model.User{}).Where("id = ?", matched.UserID).Updates(map[string]interface{}{
			"password_hash": passwordHash,
			"token_version": gorm.Expr("token_version + 1"),
		}).Error; err != nil {
			return err
		}

		now := time.Now().UTC()
		if err := tx.Model(&model.PasswordResetToken{}).Where("id = ?", matched.ID).Update("used_at", now).Error; err != nil {
			return err
		}

		// Best-effort password history.
		_ = s.savePasswordHistoryTx(tx, matched.UserID, oldHash)
		return nil
	}); err != nil {
		if appErr, ok := err.(*errors.AppError); ok {
			return appErr
		}
		return errors.ErrInternal
	}

	s.invalidateUserCacheByID(matched.UserID)
	return nil
}

// savePasswordHistoryTx saves a password history entry inside a transaction.
func (s *Service) savePasswordHistoryTx(tx *gorm.DB, userID, hash string) error {
	if s.cfg.PasswordHistoryCount <= 0 {
		return nil
	}
	ph := &model.PasswordHistory{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		UserID:       userID,
		PasswordHash: hash,
	}
	return tx.Create(ph).Error
}

// SessionInfo is the API view of one active login session (S-2 多设备标识).
// TokenHash is never exposed; Current marks the session the caller is using.
// A4-S1：IP 是会话的最近活跃来源地址——ListSessions 只会话归属者本人可见，
// 原样返回、由客户端脱敏展示（desensitizeIp）；空串表示未知（迁移前的老行）。
type SessionInfo struct {
	ID           string    `json:"id"`
	DeviceType   string    `json:"deviceType"`
	DeviceName   string    `json:"deviceName"`
	IP           string    `json:"ip"`
	LastActiveAt time.Time `json:"lastActiveAt"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	IsCurrent    bool      `json:"isCurrent"`
}

// ListSessions returns the caller's own active (non-expired) sessions,
// newest activity first. currentSessionID marks which row is "this device";
// it comes from the access token's session_id claim (M1) and may be empty
// for legacy tokens (no row is flagged current in that case).
func (s *Service) ListSessions(userID, currentSessionID string) ([]SessionInfo, error) {
	var sessions []model.UserSession
	if err := s.db.Where("user_id = ? AND expires_at > ?", userID, time.Now().UTC()).
		Order("last_active_at DESC, created_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, errors.ErrInternal
	}

	out := make([]SessionInfo, 0, len(sessions))
	for i := range sessions {
		sess := &sessions[i]
		lastActive := sess.LastActiveAt
		if lastActive.IsZero() {
			lastActive = sess.CreatedAt // pre-migration rows: fall back to creation time
		}
		out = append(out, SessionInfo{
			ID:           sess.ID,
			DeviceType:   sess.DeviceType,
			DeviceName:   sess.DeviceName,
			IP:           sess.IP,
			LastActiveAt: lastActive,
			CreatedAt:    sess.CreatedAt,
			ExpiresAt:    sess.ExpiresAt,
			IsCurrent:    currentSessionID != "" && sess.ID == currentSessionID,
		})
	}
	return out, nil
}

// RevokeSession deletes one of the caller's own sessions ("下线此设备").
//
// Semantics:
//   - session not found or owned by another user → ErrNotFound (404); we do
//     not distinguish the two to avoid leaking other users' session IDs.
//   - session IS the caller's current session → ErrBadRequest (400): logging
//     out of the current device has a dedicated flow (/auth/logout) that also
//     bumps TokenVersion; silently succeeding here would look like "kicked
//     another device" while actually killing the caller's own session.
func (s *Service) RevokeSession(userID, currentSessionID, sessionID string) error {
	if sessionID == "" {
		return errors.ErrBadRequest.WithDetails("session id required")
	}
	if currentSessionID != "" && sessionID == currentSessionID {
		return errors.ErrBadRequest.WithDetails("cannot revoke the current session; use logout instead")
	}

	res := s.db.Where("id = ? AND user_id = ?", sessionID, userID).Delete(&model.UserSession{})
	if res.Error != nil {
		return errors.ErrInternal
	}
	if res.RowsAffected == 0 {
		return errors.ErrNotFound
	}
	return nil
}

// RevokeOthersSessions 下线当前用户除当前会话外的全部会话（A4-S1，
// DES-20261001-01 §5.2「POST /auth/sessions/revoke-others」）。
// currentSessionID 来自 access token 的 session_id 声明；返回被删除的行数。
// 当前会话行（连同正在使用的 token）保持不动。
func (s *Service) RevokeOthersSessions(userID, currentSessionID string) (int64, error) {
	res := s.db.Where("user_id = ? AND id <> ?", userID, currentSessionID).
		Delete(&model.UserSession{})
	if res.Error != nil {
		return 0, errors.ErrInternal
	}
	return res.RowsAffected, nil
}

// sessionRow 是异地登录提醒判定的最小会话投影（只取 ip 列，避免整行加载）。
type sessionRow struct {
	IP string
}

// shouldAlertNewLogin 判定一次新登录是否触发「异地登录提醒」（A4-S2）。
// 规则（DES-20261001-01 §5.2）：
//   - 用户没有任何既有活跃会话 → 首次登录，不告警；
//   - 所有既有会话的 IP 与 newIP 均不同网段 → 告警；
//   - 任一既有会话与 newIP 同网段（IPv4 /24、IPv6 /64）→ 不告警；
//   - 既有会话 IP 为空串（迁移前老行 / 未采集）视为「未知网段」，即与任何
//     newIP 都不同段——首次从新网络登录最多告警一次，可接受。
func shouldAlertNewLogin(existing []sessionRow, newIP string) bool {
	if len(existing) == 0 {
		return false
	}
	for _, row := range existing {
		if sameIPNetwork(row.IP, newIP) {
			return false
		}
	}
	return true
}

// sameIPNetwork 报告两个 IP 是否属于同一网段：IPv4 按 /24 比较，IPv6 按
// /64 比较。任一地址为空串或无法解析时返回 false（视为不同网段，宁可
// 多告警一次也不放过真实的新设备登录）。
func sameIPNetwork(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	addrA, err := netip.ParseAddr(a)
	if err != nil {
		return false
	}
	addrB, err := netip.ParseAddr(b)
	if err != nil {
		return false
	}
	addrA = addrA.Unmap() // IPv4-mapped IPv6 归一到 IPv4 口径
	addrB = addrB.Unmap()
	if addrA.Is4() != addrB.Is4() {
		return false
	}
	bits := 64
	if addrA.Is4() {
		bits = 24
	}
	prefixA := netip.PrefixFrom(addrA, bits).Masked()
	prefixB := netip.PrefixFrom(addrB, bits).Masked()
	return prefixA == prefixB
}

// desensitizeIPForAlert 把 IP 脱敏后放进 security_alert 广播载荷（A4-S2）。
// security_alert 可能落进客户端日志或通知中心，广播面比 /auth/sessions 响应
// 更宽，因此服务端直接脱敏，不再依赖客户端二次处理：
//   - IPv4 保留前两段与尾段、中段打码：115.231.*.133；
//   - IPv6 保留前 2 组、其余打码；
//   - 空串/无法解析 → 空串（客户端显示「—」）。
func desensitizeIPForAlert(ip string) string {
	if ip == "" {
		return ""
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	if addr.Is4() {
		parts := strings.Split(addr.String(), ".")
		return parts[0] + "." + parts[1] + ".*." + parts[3]
	}
	groups := strings.Split(addr.String(), ":")
	if len(groups) <= 2 {
		return addr.String()
	}
	return groups[0] + ":" + groups[1] + ":****"
}

// ChangePassword changes a user's password after verifying the current password.
func (s *Service) ChangePassword(userID, currentPassword, newPassword string) error {
	// C6: delegate user lookup to UserRepository (cache-wrapped in production).
	user, err := s.userRepo.GetByID(context.Background(), userID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.ErrNotFound
		}
		return errors.ErrInternal
	}

	if !crypto.CheckPassword(currentPassword, user.PasswordHash) {
		return errors.New(errors.AUTH_INVALID_CREDENTIALS, "current password is incorrect")
	}

	if valid, reason := validator.ValidatePassword(newPassword); !valid {
		return errors.ErrBadRequest.WithDetails(reason)
	}
	if err := s.checkPasswordNotPwned(newPassword); err != nil {
		return err
	}

	// H10: check password history before allowing change
	if err := s.checkPasswordHistory(userID, newPassword); err != nil {
		return err
	}

	passwordHash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return errors.ErrInternal.WithDetails("failed to hash password")
	}

	// H10: save old password hash to history before updating
	oldPasswordHash := user.PasswordHash

	// Update password and increment TokenVersion to invalidate all existing sessions
	if err := s.db.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"password_hash": passwordHash,
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		return errors.ErrInternal
	}
	s.invalidateUserCacheByID(userID)

	// H10: record old password in history (best-effort, don't block on failure)
	if err := s.savePasswordHistory(userID, oldPasswordHash); err != nil {
		// Log but don't fail the password change
		fmt.Printf("WARN: failed to save password history for user %s: %v\n", userID, err)
	}

	return nil
}
