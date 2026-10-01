package errors

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ErrorCode follows the format MODULE_ERROR_TYPE (e.g., AUTH_INVALID_CREDENTIALS)
type ErrorCode string

const (
	// Auth
	AUTH_INVALID_CREDENTIALS    ErrorCode = "AUTH_INVALID_CREDENTIALS"
	AUTH_TOKEN_MISSING          ErrorCode = "AUTH_TOKEN_MISSING"
	AUTH_TOKEN_INVALID          ErrorCode = "AUTH_TOKEN_INVALID"
	AUTH_TOKEN_EXPIRED          ErrorCode = "AUTH_TOKEN_EXPIRED"
	AUTH_UNAUTHORIZED           ErrorCode = "AUTH_UNAUTHORIZED"
	AUTH_FORBIDDEN              ErrorCode = "AUTH_FORBIDDEN"
	AUTH_SERVER_NOT_INITIALIZED ErrorCode = "AUTH_SERVER_NOT_INITIALIZED"
	AUTH_CSRF_TOKEN_MISSING     ErrorCode = "AUTH_CSRF_TOKEN_MISSING"
	AUTH_CSRF_TOKEN_INVALID     ErrorCode = "AUTH_CSRF_TOKEN_INVALID"
	AUTH_USER_EXISTS            ErrorCode = "AUTH_USER_EXISTS"
	AUTH_EMAIL_EXISTS           ErrorCode = "AUTH_EMAIL_EXISTS" // alias for backward compat
	AUTH_EMAIL_ALREADY_EXISTS   ErrorCode = "AUTH_EMAIL_ALREADY_EXISTS"
	AUTH_ACCOUNT_LOCKED         ErrorCode = "AUTH_ACCOUNT_LOCKED"
	AUTH_ACCOUNT_LOCK_NOTIFY    ErrorCode = "AUTH_ACCOUNT_LOCK_NOTIFY"
	AUTH_PASSWORD_BREACHED      ErrorCode = "AUTH_PASSWORD_BREACHED"
	AUTH_PASSWORD_REUSED        ErrorCode = "AUTH_PASSWORD_REUSED" // H10: password matches a recent history entry
	AUTH_USER_DISABLED          ErrorCode = "AUTH_USER_DISABLED"
	AUTH_USER_NOT_FOUND         ErrorCode = "AUTH_USER_NOT_FOUND" // L6: user not found by login ID (design doc §2.2)
	AUTH_RESET_TOKEN_INVALID    ErrorCode = "AUTH_RESET_TOKEN_INVALID"
	AUTH_RESET_TOKEN_EXPIRED    ErrorCode = "AUTH_RESET_TOKEN_EXPIRED"
	AUTH_RESET_TOKEN_USED       ErrorCode = "AUTH_RESET_TOKEN_USED"
	AUTH_SECURITY_QUESTION_NOT_SET   ErrorCode = "AUTH_SECURITY_QUESTION_NOT_SET"
	AUTH_SECURITY_QUESTION_INCORRECT ErrorCode = "AUTH_SECURITY_QUESTION_INCORRECT"
	AUTH_SECURITY_QUESTION_LOCKED    ErrorCode = "AUTH_SECURITY_QUESTION_LOCKED"
	AUTH_SECURITY_QUESTION_INVALID   ErrorCode = "AUTH_SECURITY_QUESTION_INVALID"
	AUTH_SECURITY_QUESTION_DUPLICATE ErrorCode = "AUTH_SECURITY_QUESTION_DUPLICATE"
	AUTH_BOOTSTRAP_TOKEN_INVALID ErrorCode = "AUTH_BOOTSTRAP_TOKEN_INVALID"
	AUTH_BOOTSTRAP_TOKEN_USED   ErrorCode = "AUTH_BOOTSTRAP_TOKEN_USED"
	AUTH_ALREADY_INITIALIZED    ErrorCode = "AUTH_ALREADY_INITIALIZED" // H1: server already initialized when env-based owner creation attempted
	AUTH_DISPLAY_NAME_EXISTS    ErrorCode = "AUTH_DISPLAY_NAME_EXISTS" // FIX-2026-0808-01: display name must be unique within the user's space
	// A4-S1（DES-20261001-01 §5.2）：访问令牌缺少 session_id 声明（旧 token），
	// 无法定位「当前会话」——revoke-others 需要它来保住自己不下线。
	AUTH_NO_SESSION_CONTEXT ErrorCode = "AUTH_NO_SESSION_CONTEXT"

	// Channel
	CHANNEL_NOT_FOUND      ErrorCode = "CHANNEL_NOT_FOUND"
	CHANNEL_ACCESS_DENIED  ErrorCode = "CHANNEL_ACCESS_DENIED"
	CHANNEL_ALREADY_EXISTS ErrorCode = "CHANNEL_ALREADY_EXISTS"

	// Message
	MESSAGE_NOT_FOUND    ErrorCode = "MESSAGE_NOT_FOUND"
	MESSAGE_TOO_LONG     ErrorCode = "MESSAGE_TOO_LONG"
	MESSAGE_RATE_LIMITED ErrorCode = "MESSAGE_RATE_LIMITED"

	// Voice
	VOICE_ROOM_NOT_FOUND      ErrorCode = "VOICE_ROOM_NOT_FOUND"
	VOICE_JOIN_DENIED         ErrorCode = "VOICE_JOIN_DENIED"
	VOICE_LIVEKIT_ERROR       ErrorCode = "VOICE_LIVEKIT_ERROR"
	VOICE_SERVICE_UNAVAILABLE ErrorCode = "VOICE_SERVICE_UNAVAILABLE"

	// Bot
	BOT_NOT_FOUND              ErrorCode = "BOT_NOT_FOUND"
	BOT_QUEUE_FULL             ErrorCode = "BOT_QUEUE_FULL"
	BOT_TTS_CONSENT_REQUIRED   ErrorCode = "BOT_TTS_CONSENT_REQUIRED"
	BOT_NETEASE_API_ERROR      ErrorCode = "BOT_NETEASE_API_ERROR"
	BOT_NETEASE_VERIFICATION_REQUIRED ErrorCode = "BOT_NETEASE_VERIFICATION_REQUIRED"
	BOT_NETEASE_NOT_CONFIGURED ErrorCode = "BOT_NETEASE_NOT_CONFIGURED"
	BOT_NETEASE_UNREACHABLE    ErrorCode = "BOT_NETEASE_UNREACHABLE"
	BOT_TTS_NOT_CONFIGURED     ErrorCode = "BOT_TTS_NOT_CONFIGURED"

	// File
	FILE_TOO_LARGE     ErrorCode = "FILE_TOO_LARGE"
	FILE_INVALID_TYPE  ErrorCode = "FILE_INVALID_TYPE"
	FILE_UPLOAD_FAILED ErrorCode = "FILE_UPLOAD_FAILED"

	// Admin
	ADMIN_UNAUTHORIZED   ErrorCode = "ADMIN_UNAUTHORIZED"
	ADMIN_CONFIG_INVALID ErrorCode = "ADMIN_CONFIG_INVALID"

	// System
	SYSTEM_INTERNAL_ERROR      ErrorCode = "SYSTEM_INTERNAL_ERROR"
	SYSTEM_RATE_LIMITED        ErrorCode = "SYSTEM_RATE_LIMITED"
	SYSTEM_NOT_FOUND           ErrorCode = "SYSTEM_NOT_FOUND"
	SYSTEM_BAD_REQUEST         ErrorCode = "SYSTEM_BAD_REQUEST"
	SYSTEM_NOT_IMPLEMENTED     ErrorCode = "SYSTEM_NOT_IMPLEMENTED"
	SYSTEM_CONFLICT            ErrorCode = "SYSTEM_CONFLICT"
	SYSTEM_SERVICE_UNAVAILABLE ErrorCode = "SYSTEM_SERVICE_UNAVAILABLE"

	// Search
	SEARCH_QUERY_TOO_SHORT ErrorCode = "SEARCH_QUERY_TOO_SHORT"

	// Notifications
	NOTIFICATION_NOT_FOUND ErrorCode = "NOTIFICATION_NOT_FOUND"

	// Recording
	VOICE_RECORDING_NOT_FOUND ErrorCode = "VOICE_RECORDING_NOT_FOUND"

	// ═══════════════════════════════════════════════════════════
	// Extended Error Codes — aligned with the original pkg/errors design doc
	// ═══════════════════════════════════════════════════════════

	// Auth extensions
	AUTH_ACCOUNT_FROZEN       ErrorCode = "AUTH_ACCOUNT_FROZEN"
	AUTH_PASSWORD_TOO_WEAK    ErrorCode = "AUTH_PASSWORD_TOO_WEAK"
	AUTH_PASSWORD_SAME_AS_OLD ErrorCode = "AUTH_PASSWORD_SAME_AS_OLD"
	AUTH_EMAIL_NOT_VERIFIED   ErrorCode = "AUTH_EMAIL_NOT_VERIFIED"
	AUTH_MAX_SESSIONS_REACHED ErrorCode = "AUTH_MAX_SESSIONS_REACHED"

	// User extensions
	USER_NOT_FOUND          ErrorCode = "USER_NOT_FOUND"
	USER_ALREADY_FRIENDS    ErrorCode = "USER_ALREADY_FRIENDS"
	USER_CANNOT_FRIEND_SELF ErrorCode = "USER_CANNOT_FRIEND_SELF"

	// Channel extensions
	CHANNEL_NAME_INVALID          ErrorCode = "CHANNEL_NAME_INVALID"
	CHANNEL_LIMIT_REACHED         ErrorCode = "CHANNEL_LIMIT_REACHED"
	CHANNEL_CANNOT_DELETE_GENERAL ErrorCode = "CHANNEL_CANNOT_DELETE_GENERAL"
	CHANNEL_NOT_VOICE             ErrorCode = "CHANNEL_NOT_VOICE" // 频道不是语音频道

	// Message extensions
	MESSAGE_EDIT_EXPIRED         ErrorCode = "MESSAGE_EDIT_EXPIRED"
	MESSAGE_ALREADY_DELETED      ErrorCode = "MESSAGE_ALREADY_DELETED"
	MESSAGE_CANNOT_EDIT_OTHERS   ErrorCode = "MESSAGE_CANNOT_EDIT_OTHERS"
	MESSAGE_CANNOT_DELETE_OTHERS ErrorCode = "MESSAGE_CANNOT_DELETE_OTHERS"

	// Voice extensions
	VOICE_ROOM_FULL           ErrorCode = "VOICE_ROOM_FULL"
	VOICE_ALREADY_IN_ROOM     ErrorCode = "VOICE_ALREADY_IN_ROOM"
	VOICE_QUALITY_UNAVAILABLE ErrorCode = "VOICE_QUALITY_UNAVAILABLE"
	VOICE_E2EE_REQUIRED       ErrorCode = "VOICE_E2EE_REQUIRED"
	// VOICE_E2EE_RECORDING_UNSUPPORTED: E2EE 房间中的加密轨在 LiveKit Egress 侧
	// 无法解密（egress 参与者拿不到客户端密钥），录像只会得到噪声/黑屏，
	// 因此直接拒绝而不是产出坏录像。见 DES-2026-0912-06 §4.6。
	VOICE_E2EE_RECORDING_UNSUPPORTED ErrorCode = "VOICE_E2EE_RECORDING_UNSUPPORTED"

	// Bot extensions
	BOT_NOT_PLAYING       ErrorCode = "BOT_NOT_PLAYING"
	BOT_NO_NEXT_TRACK     ErrorCode = "BOT_NO_NEXT_TRACK"
	BOT_TRACK_NOT_FOUND   ErrorCode = "BOT_TRACK_NOT_FOUND"
	BOT_TTS_TEXT_TOO_LONG ErrorCode = "BOT_TTS_TEXT_TOO_LONG"
	BOT_TOKEN_INVALID     ErrorCode = "BOT_TOKEN_INVALID" // M22: 无效的机器人令牌

	// File extensions
	FILE_VIRUS_DETECTED  ErrorCode = "FILE_VIRUS_DETECTED"
	FILE_STORAGE_FULL    ErrorCode = "FILE_STORAGE_FULL"
	FILE_NOT_FOUND       ErrorCode = "FILE_NOT_FOUND"
	FILE_DOWNLOAD_FAILED ErrorCode = "FILE_DOWNLOAD_FAILED"

	// Whiteboard extensions
	WHITEBOARD_NOT_FOUND        ErrorCode = "WHITEBOARD_NOT_FOUND"
	WHITEBOARD_STROKE_TOO_LARGE ErrorCode = "WHITEBOARD_STROKE_TOO_LARGE"
	// M27: Whiteboard module dedicated codes (API 错误码规范 §6.10)
	WHITEBOARD_ARCHIVE_NOT_FOUND  ErrorCode = "WHITEBOARD_ARCHIVE_NOT_FOUND"
	WHITEBOARD_SESSION_NOT_JOINED ErrorCode = "WHITEBOARD_SESSION_NOT_JOINED"
	WHITEBOARD_CLEAR_NOT_ALLOWED  ErrorCode = "WHITEBOARD_CLEAR_NOT_ALLOWED"
	WHITEBOARD_ARCHIVE_NOT_ALLOWED ErrorCode = "WHITEBOARD_ARCHIVE_NOT_ALLOWED"
	WHITEBOARD_DELETE_NOT_ALLOWED  ErrorCode = "WHITEBOARD_DELETE_NOT_ALLOWED"

	// Screen share extensions
	SCREEN_SHARE_ALREADY_ACTIVE      ErrorCode = "SCREEN_SHARE_ALREADY_ACTIVE"
	SCREEN_SHARE_NOT_ACTIVE          ErrorCode = "SCREEN_SHARE_NOT_ACTIVE"
	SCREENSHARE_WEB_FORBIDDEN        ErrorCode = "SCREENSHARE_WEB_FORBIDDEN"                     // Web 端不允许发起屏幕共享
	SCREENSHARE_FORCE_STOP_FORBIDDEN ErrorCode = "SCREENSHARE_FORCE_STOP_FORBIDDEN"             // M21: 无权强制终止屏幕共享
	SCREEN_MAX_VIEWERS_REACHED       ErrorCode = "SCREEN_MAX_VIEWERS_REACHED"                    // 屏幕共享观看人数已达上限
	SCREEN_SOURCE_INVALID            ErrorCode = "SCREEN_SOURCE_INVALID"                         // 无效的屏幕共享源

	// CloudFS extensions
	CLOUDFS_FOLDER_NOT_FOUND ErrorCode = "CLOUDFS_FOLDER_NOT_FOUND"
	CLOUDFS_FILE_NOT_FOUND   ErrorCode = "CLOUDFS_FILE_NOT_FOUND"
	CLOUDFS_QUOTA_EXCEEDED   ErrorCode = "CLOUDFS_QUOTA_EXCEEDED"
	// M27: CloudFS module dedicated codes (API 错误码规范 §6.11)
	// 保留 CLOUDFS_* 前缀（向后兼容），对应设计文档的 CLOUD_FS_* 前缀
	CLOUDFS_ALREADY_EXISTS    ErrorCode = "CLOUDFS_ALREADY_EXISTS"
	CLOUDFS_NAME_TOO_LONG     ErrorCode = "CLOUDFS_NAME_TOO_LONG"
	CLOUDFS_NAME_INVALID      ErrorCode = "CLOUDFS_NAME_INVALID"
	CLOUDFS_STORAGE_FULL      ErrorCode = "CLOUDFS_STORAGE_FULL"
	CLOUDFS_PERMISSION_DENIED ErrorCode = "CLOUDFS_PERMISSION_DENIED"

	// DES-2026-0912-05: 云文件分享链接（唯一免鉴权业务接口）
	SHARE_NOT_FOUND            ErrorCode = "SHARE_NOT_FOUND"            // token 不存在
	SHARE_EXPIRED              ErrorCode = "SHARE_EXPIRED"              // 已过期
	SHARE_REVOKED              ErrorCode = "SHARE_REVOKED"              // 已撤销
	SHARE_DOWNLOAD_LIMITED     ErrorCode = "SHARE_DOWNLOAD_LIMITED"     // 已达下载次数上限
	SHARE_UNAVAILABLE          ErrorCode = "SHARE_UNAVAILABLE"          // 并发争抢落败等兜底拒绝
	SHARE_VERIFY_FAILED        ErrorCode = "SHARE_VERIFY_FAILED"        // 「分享不存在」与「密码错误」共用，不可区分
	SHARE_TICKET_INVALID       ErrorCode = "SHARE_TICKET_INVALID"       // 缺少/无效的一次性下载凭据
	SHARE_TTL_INVALID          ErrorCode = "SHARE_TTL_INVALID"          // 过期时间缺失或超出上限
	SHARE_MAX_DOWNLOADS_INVALID ErrorCode = "SHARE_MAX_DOWNLOADS_INVALID" // 次数上限为负
	SHARE_PASSWORD_INVALID     ErrorCode = "SHARE_PASSWORD_INVALID"     // 创建时密码不合法（过长等）

	// M27: SharedDoc module dedicated codes (API 错误码规范 §6.12)
	SHAREDOC_NOT_FOUND         ErrorCode = "SHAREDOC_NOT_FOUND"
	SHAREDOC_TITLE_TOO_LONG    ErrorCode = "SHAREDOC_TITLE_TOO_LONG"
	SHAREDOC_CONFLICT          ErrorCode = "SHAREDOC_CONFLICT"
	SHAREDOC_PERMISSION_DENIED ErrorCode = "SHAREDOC_PERMISSION_DENIED"

	// M27: Schedule module dedicated codes (API 错误码规范 §6.13)
	SCHEDULE_NOT_FOUND         ErrorCode = "SCHEDULE_NOT_FOUND"
	SCHEDULE_TIME_INVALID      ErrorCode = "SCHEDULE_TIME_INVALID"
	SCHEDULE_TITLE_TOO_LONG    ErrorCode = "SCHEDULE_TITLE_TOO_LONG"
	SCHEDULE_PERMISSION_DENIED ErrorCode = "SCHEDULE_PERMISSION_DENIED"
	SCHEDULE_INVITE_INVALID    ErrorCode = "SCHEDULE_INVITE_INVALID" // A8-S1: 邀请参数无效（受邀人非成员/超上限/含创建者/应答状态非法）

	// M27: Minigame module dedicated codes (API 错误码规范 §6.14)
	MINIGAME_NOT_FOUND           ErrorCode = "MINIGAME_NOT_FOUND"
	MINIGAME_ROOM_FULL           ErrorCode = "MINIGAME_ROOM_FULL"
	MINIGAME_ALREADY_IN_ROOM     ErrorCode = "MINIGAME_ALREADY_IN_ROOM"
	MINIGAME_NOT_IN_ROOM         ErrorCode = "MINIGAME_NOT_IN_ROOM"
	MINIGAME_INVALID_ACTION      ErrorCode = "MINIGAME_INVALID_ACTION"
	MINIGAME_NOT_YOUR_TURN       ErrorCode = "MINIGAME_NOT_YOUR_TURN"
	MINIGAME_GAME_ALREADY_STARTED ErrorCode = "MINIGAME_GAME_ALREADY_STARTED"
	MINIGAME_GAME_NOT_STARTED    ErrorCode = "MINIGAME_GAME_NOT_STARTED"
	MINIGAME_GAME_ALREADY_ENDED  ErrorCode = "MINIGAME_GAME_ALREADY_ENDED"
	MINIGAME_MODULE_DISABLED     ErrorCode = "MINIGAME_MODULE_DISABLED"

	// M27: VirtualNet module dedicated codes (API 错误码规范 §6.15)
	VIRTUALNET_NOT_FOUND           ErrorCode = "VIRTUALNET_NOT_FOUND"
	VIRTUALNET_ALREADY_CONNECTED   ErrorCode = "VIRTUALNET_ALREADY_CONNECTED"
	VIRTUALNET_NOT_CONNECTED       ErrorCode = "VIRTUALNET_NOT_CONNECTED"
	VIRTUALNET_SERVICE_UNAVAILABLE ErrorCode = "VIRTUALNET_SERVICE_UNAVAILABLE"
	VIRTUALNET_PLATFORM_UNSUPPORTED ErrorCode = "VIRTUALNET_PLATFORM_UNSUPPORTED"
	VIRTUALNET_PERMISSION_DENIED   ErrorCode = "VIRTUALNET_PERMISSION_DENIED"

	// Admin extensions
	ADMIN_USER_NOT_FOUND      ErrorCode = "ADMIN_USER_NOT_FOUND"
	ADMIN_CANNOT_DELETE_OWNER ErrorCode = "ADMIN_CANNOT_DELETE_OWNER"
	ADMIN_CANNOT_DEMOTE_SELF  ErrorCode = "ADMIN_CANNOT_DEMOTE_SELF"

	// WebSocket / Realtime extensions
	WS_CONNECTION_LIMIT     ErrorCode = "WS_CONNECTION_LIMIT"
	ROOM_ACCESS_DENIED      ErrorCode = "ROOM_ACCESS_DENIED"
	CORS_ORIGIN_NOT_ALLOWED ErrorCode = "CORS_ORIGIN_NOT_ALLOWED"

	// Update / Version
	CLIENT_VERSION_UNSUPPORTED ErrorCode = "CLIENT_VERSION_UNSUPPORTED"
	CLIENT_VERSION_TOO_OLD     ErrorCode = "CLIENT_VERSION_TOO_OLD"
	SERVER_MAINTENANCE         ErrorCode = "SERVER_MAINTENANCE"

	// Validation
	VALIDATION_FAILED      ErrorCode = "VALIDATION_FAILED"
	INVALID_JSON           ErrorCode = "INVALID_JSON"
	MISSING_REQUIRED_FIELD ErrorCode = "MISSING_REQUIRED_FIELD"
	INVALID_FIELD_FORMAT   ErrorCode = "INVALID_FIELD_FORMAT"
	VALUE_OUT_OF_RANGE     ErrorCode = "VALUE_OUT_OF_RANGE"

	// Network / External
	NETWORK_ERROR          ErrorCode = "NETWORK_ERROR"
	TIMEOUT                ErrorCode = "TIMEOUT"
	EXTERNAL_SERVICE_ERROR ErrorCode = "EXTERNAL_SERVICE_ERROR"
	DATABASE_ERROR         ErrorCode = "DATABASE_ERROR"

	// Voice module extended codes
	VOICE_TOKEN_REFRESH_FAILED    ErrorCode = "VOICE_TOKEN_REFRESH_FAILED"
	VOICE_CHANNEL_FULL            ErrorCode = "VOICE_CHANNEL_FULL"
	VOICE_USER_MUTED_BY_ADMIN     ErrorCode = "VOICE_USER_MUTED_BY_ADMIN"
	VOICE_USER_KICKED             ErrorCode = "VOICE_USER_KICKED"
	VOICE_RECORDING_IN_PROGRESS   ErrorCode = "VOICE_RECORDING_IN_PROGRESS"
	VOICE_RECORDING_NO_PERMISSION ErrorCode = "VOICE_RECORDING_NO_PERMISSION"
	VOICE_STT_UNAVAILABLE         ErrorCode = "VOICE_STT_UNAVAILABLE"
	VOICE_QUALITY_CHANGE_FAILED   ErrorCode = "VOICE_QUALITY_CHANGE_FAILED"
	VOICE_NOT_IN_CHANNEL          ErrorCode = "VOICE_NOT_IN_CHANNEL"

	// Screen Share extended codes
	SCREEN_SHARE_IN_PROGRESS   ErrorCode = "SCREEN_SHARE_IN_PROGRESS"
	SCREEN_SHARE_NO_PERMISSION ErrorCode = "SCREEN_SHARE_NO_PERMISSION"

	// Whiteboard extended codes
	WHITEBOARD_SYNC_FAILED   ErrorCode = "WHITEBOARD_SYNC_FAILED"
	WHITEBOARD_NO_PERMISSION ErrorCode = "WHITEBOARD_NO_PERMISSION"

	// Search extended codes
	SEARCH_NO_RESULTS ErrorCode = "SEARCH_NO_RESULTS"

	// Backup
	BACKUP_IN_PROGRESS ErrorCode = "BACKUP_IN_PROGRESS"
	BACKUP_FAILED      ErrorCode = "BACKUP_FAILED"
	RESTORE_FAILED     ErrorCode = "RESTORE_FAILED"

	// RemoteAssist module dedicated codes (T49 §6.16)
	REMOTE_ASSIST_NOT_FOUND         ErrorCode = "REMOTE_ASSIST_NOT_FOUND"
	REMOTE_ASSIST_FORBIDDEN         ErrorCode = "REMOTE_ASSIST_FORBIDDEN"
	REMOTE_ASSIST_EXPIRED           ErrorCode = "REMOTE_ASSIST_EXPIRED"
	REMOTE_ASSIST_ALREADY_ACTIVE    ErrorCode = "REMOTE_ASSIST_ALREADY_ACTIVE"
	REMOTE_ASSIST_INVALID_STATE     ErrorCode = "REMOTE_ASSIST_INVALID_STATE"
	REMOTE_ASSIST_PERMISSION_DENIED ErrorCode = "REMOTE_ASSIST_PERMISSION_DENIED"
)

// HTTP status code mapping for each error code
var statusCodeMap = map[ErrorCode]int{
	AUTH_INVALID_CREDENTIALS:    http.StatusUnauthorized,
	AUTH_TOKEN_MISSING:          http.StatusUnauthorized,
	AUTH_TOKEN_INVALID:          http.StatusUnauthorized,
	AUTH_TOKEN_EXPIRED:          http.StatusUnauthorized,
	AUTH_UNAUTHORIZED:           http.StatusUnauthorized,
	AUTH_FORBIDDEN:              http.StatusForbidden,
	AUTH_SERVER_NOT_INITIALIZED: http.StatusServiceUnavailable,
	AUTH_CSRF_TOKEN_MISSING:     http.StatusForbidden,
	AUTH_CSRF_TOKEN_INVALID:     http.StatusForbidden,
	AUTH_USER_EXISTS:            http.StatusConflict,
	AUTH_EMAIL_EXISTS:           http.StatusConflict,
	AUTH_EMAIL_ALREADY_EXISTS:   http.StatusConflict,
	AUTH_ACCOUNT_LOCKED:         http.StatusTooManyRequests,
	AUTH_ACCOUNT_LOCK_NOTIFY:    http.StatusTooManyRequests,
	AUTH_PASSWORD_BREACHED:      http.StatusUnprocessableEntity,
	AUTH_PASSWORD_REUSED:        http.StatusBadRequest,
	AUTH_USER_DISABLED:          http.StatusForbidden,
	AUTH_USER_NOT_FOUND:         http.StatusNotFound,
	AUTH_RESET_TOKEN_INVALID:         http.StatusForbidden,
	AUTH_RESET_TOKEN_EXPIRED:         http.StatusForbidden,
	AUTH_RESET_TOKEN_USED:            http.StatusForbidden,
	AUTH_SECURITY_QUESTION_NOT_SET:   http.StatusBadRequest,
	AUTH_SECURITY_QUESTION_INCORRECT: http.StatusForbidden,
	AUTH_SECURITY_QUESTION_LOCKED:    http.StatusTooManyRequests,
	AUTH_SECURITY_QUESTION_INVALID:   http.StatusBadRequest,
	AUTH_SECURITY_QUESTION_DUPLICATE: http.StatusBadRequest,
	AUTH_BOOTSTRAP_TOKEN_INVALID:     http.StatusForbidden,
	AUTH_BOOTSTRAP_TOKEN_USED:        http.StatusForbidden,
	AUTH_ALREADY_INITIALIZED:         http.StatusConflict,
	AUTH_DISPLAY_NAME_EXISTS:         http.StatusConflict,
	AUTH_NO_SESSION_CONTEXT:          http.StatusBadRequest,

	CHANNEL_NOT_FOUND:      http.StatusNotFound,
	CHANNEL_ACCESS_DENIED:  http.StatusForbidden,
	CHANNEL_ALREADY_EXISTS: http.StatusConflict,
	CHANNEL_NOT_VOICE:      http.StatusBadRequest,

	MESSAGE_NOT_FOUND:    http.StatusNotFound,
	MESSAGE_TOO_LONG:     http.StatusBadRequest,
	MESSAGE_RATE_LIMITED: http.StatusTooManyRequests,

	VOICE_ROOM_NOT_FOUND:      http.StatusNotFound,
	VOICE_JOIN_DENIED:         http.StatusForbidden,
	VOICE_LIVEKIT_ERROR:       http.StatusServiceUnavailable,
	VOICE_SERVICE_UNAVAILABLE: http.StatusServiceUnavailable,

	BOT_NOT_FOUND:            http.StatusNotFound,
	BOT_QUEUE_FULL:           http.StatusConflict,
	BOT_TTS_CONSENT_REQUIRED: http.StatusForbidden,
	BOT_NETEASE_API_ERROR:    http.StatusServiceUnavailable,
	BOT_NETEASE_VERIFICATION_REQUIRED: http.StatusConflict,
	BOT_TTS_NOT_CONFIGURED:   http.StatusServiceUnavailable,

	FILE_TOO_LARGE:     http.StatusRequestEntityTooLarge,
	FILE_INVALID_TYPE:  http.StatusUnsupportedMediaType,
	FILE_UPLOAD_FAILED: http.StatusInternalServerError,

	ADMIN_UNAUTHORIZED:   http.StatusForbidden,
	ADMIN_CONFIG_INVALID: http.StatusBadRequest,

	SYSTEM_INTERNAL_ERROR:      http.StatusInternalServerError,
	SYSTEM_RATE_LIMITED:        http.StatusTooManyRequests,
	SYSTEM_NOT_FOUND:           http.StatusNotFound,
	SYSTEM_BAD_REQUEST:         http.StatusBadRequest,
	SYSTEM_NOT_IMPLEMENTED:     http.StatusNotImplemented,
	SYSTEM_CONFLICT:            http.StatusConflict,
	SYSTEM_SERVICE_UNAVAILABLE: http.StatusServiceUnavailable,

	SEARCH_QUERY_TOO_SHORT: http.StatusBadRequest,
	NOTIFICATION_NOT_FOUND: http.StatusNotFound,

	// Extended codes
	AUTH_ACCOUNT_FROZEN:       http.StatusForbidden,
	AUTH_PASSWORD_TOO_WEAK:    http.StatusBadRequest,
	AUTH_PASSWORD_SAME_AS_OLD: http.StatusBadRequest,
	AUTH_EMAIL_NOT_VERIFIED:   http.StatusForbidden,
	AUTH_MAX_SESSIONS_REACHED: http.StatusConflict,

	USER_NOT_FOUND:          http.StatusNotFound,
	USER_ALREADY_FRIENDS:    http.StatusConflict,
	USER_CANNOT_FRIEND_SELF: http.StatusBadRequest,

	CHANNEL_NAME_INVALID:          http.StatusBadRequest,
	CHANNEL_LIMIT_REACHED:         http.StatusConflict,
	CHANNEL_CANNOT_DELETE_GENERAL: http.StatusBadRequest,

	MESSAGE_EDIT_EXPIRED:         http.StatusBadRequest,
	MESSAGE_ALREADY_DELETED:      http.StatusBadRequest,
	MESSAGE_CANNOT_EDIT_OTHERS:   http.StatusForbidden,
	MESSAGE_CANNOT_DELETE_OTHERS: http.StatusForbidden,

	VOICE_ROOM_FULL:           http.StatusConflict,
	VOICE_ALREADY_IN_ROOM:     http.StatusConflict,
	VOICE_QUALITY_UNAVAILABLE: http.StatusBadRequest,
	VOICE_E2EE_REQUIRED:       http.StatusForbidden,

	VOICE_E2EE_RECORDING_UNSUPPORTED: http.StatusForbidden,

	BOT_NOT_PLAYING:       http.StatusBadRequest,
	BOT_NO_NEXT_TRACK:     http.StatusBadRequest,
	BOT_TRACK_NOT_FOUND:   http.StatusNotFound,
	BOT_TTS_TEXT_TOO_LONG: http.StatusBadRequest,
	BOT_TOKEN_INVALID:     http.StatusUnauthorized,

	FILE_VIRUS_DETECTED:  http.StatusForbidden,
	FILE_STORAGE_FULL:    http.StatusInsufficientStorage,
	FILE_NOT_FOUND:       http.StatusNotFound,
	FILE_DOWNLOAD_FAILED: http.StatusInternalServerError,

	WHITEBOARD_NOT_FOUND:        http.StatusNotFound,
	WHITEBOARD_STROKE_TOO_LARGE: http.StatusBadRequest,
	// M27: Whiteboard module dedicated codes
	WHITEBOARD_ARCHIVE_NOT_FOUND:   http.StatusNotFound,
	WHITEBOARD_SESSION_NOT_JOINED:  http.StatusBadRequest,
	WHITEBOARD_CLEAR_NOT_ALLOWED:   http.StatusForbidden,
	WHITEBOARD_ARCHIVE_NOT_ALLOWED: http.StatusForbidden,
	WHITEBOARD_DELETE_NOT_ALLOWED:  http.StatusForbidden,

	SCREEN_SHARE_ALREADY_ACTIVE:      http.StatusConflict,
	SCREEN_SHARE_NOT_ACTIVE:          http.StatusBadRequest,
	SCREENSHARE_WEB_FORBIDDEN:        http.StatusForbidden,
	SCREENSHARE_FORCE_STOP_FORBIDDEN: http.StatusForbidden,
	SCREEN_MAX_VIEWERS_REACHED:       http.StatusBadRequest,
	SCREEN_SOURCE_INVALID:            http.StatusBadRequest,

	CLOUDFS_FOLDER_NOT_FOUND: http.StatusNotFound,
	CLOUDFS_FILE_NOT_FOUND:   http.StatusNotFound,
	CLOUDFS_QUOTA_EXCEEDED:   http.StatusInsufficientStorage,
	// M27: CloudFS module dedicated codes
	CLOUDFS_ALREADY_EXISTS:    http.StatusConflict,
	CLOUDFS_NAME_TOO_LONG:     http.StatusBadRequest,
	CLOUDFS_NAME_INVALID:      http.StatusBadRequest,
	CLOUDFS_STORAGE_FULL:      http.StatusRequestEntityTooLarge,
	CLOUDFS_PERMISSION_DENIED: http.StatusForbidden,

	// DES-2026-0912-05: 云文件分享链接
	SHARE_NOT_FOUND:             http.StatusNotFound,
	SHARE_EXPIRED:               http.StatusGone,
	SHARE_REVOKED:               http.StatusGone,
	SHARE_DOWNLOAD_LIMITED:      http.StatusForbidden,
	SHARE_UNAVAILABLE:           http.StatusForbidden,
	SHARE_VERIFY_FAILED:         http.StatusUnauthorized,
	SHARE_TICKET_INVALID:        http.StatusUnauthorized,
	SHARE_TTL_INVALID:           http.StatusBadRequest,
	SHARE_MAX_DOWNLOADS_INVALID: http.StatusBadRequest,
	SHARE_PASSWORD_INVALID:      http.StatusBadRequest,

	// M27: SharedDoc module dedicated codes
	SHAREDOC_NOT_FOUND:         http.StatusNotFound,
	SHAREDOC_TITLE_TOO_LONG:    http.StatusBadRequest,
	SHAREDOC_CONFLICT:          http.StatusConflict,
	SHAREDOC_PERMISSION_DENIED: http.StatusForbidden,

	// M27: Schedule module dedicated codes
	SCHEDULE_NOT_FOUND:         http.StatusNotFound,
	SCHEDULE_TIME_INVALID:      http.StatusBadRequest,
	SCHEDULE_TITLE_TOO_LONG:    http.StatusBadRequest,
	SCHEDULE_PERMISSION_DENIED: http.StatusForbidden,
	SCHEDULE_INVITE_INVALID:    http.StatusBadRequest,

	// M27: Minigame module dedicated codes
	MINIGAME_NOT_FOUND:            http.StatusNotFound,
	MINIGAME_ROOM_FULL:            http.StatusConflict,
	MINIGAME_ALREADY_IN_ROOM:      http.StatusConflict,
	MINIGAME_NOT_IN_ROOM:          http.StatusBadRequest,
	MINIGAME_INVALID_ACTION:       http.StatusBadRequest,
	MINIGAME_NOT_YOUR_TURN:        http.StatusBadRequest,
	MINIGAME_GAME_ALREADY_STARTED: http.StatusConflict,
	MINIGAME_GAME_NOT_STARTED:     http.StatusBadRequest,
	MINIGAME_GAME_ALREADY_ENDED:   http.StatusBadRequest,
	MINIGAME_MODULE_DISABLED:      http.StatusServiceUnavailable,

	// M27: VirtualNet module dedicated codes
	VIRTUALNET_NOT_FOUND:            http.StatusNotFound,
	VIRTUALNET_ALREADY_CONNECTED:    http.StatusConflict,
	VIRTUALNET_NOT_CONNECTED:        http.StatusBadRequest,
	VIRTUALNET_SERVICE_UNAVAILABLE:  http.StatusServiceUnavailable,
	VIRTUALNET_PLATFORM_UNSUPPORTED: http.StatusBadRequest,
	VIRTUALNET_PERMISSION_DENIED:    http.StatusForbidden,

	ADMIN_USER_NOT_FOUND:      http.StatusNotFound,
	ADMIN_CANNOT_DELETE_OWNER: http.StatusForbidden,
	ADMIN_CANNOT_DEMOTE_SELF:  http.StatusForbidden,

	CLIENT_VERSION_UNSUPPORTED: http.StatusUpgradeRequired,
	CLIENT_VERSION_TOO_OLD:     http.StatusUpgradeRequired,
	SERVER_MAINTENANCE:         http.StatusServiceUnavailable,

	VALIDATION_FAILED:      http.StatusBadRequest,
	INVALID_JSON:           http.StatusBadRequest,
	MISSING_REQUIRED_FIELD: http.StatusBadRequest,
	INVALID_FIELD_FORMAT:   http.StatusBadRequest,
	VALUE_OUT_OF_RANGE:     http.StatusBadRequest,

	NETWORK_ERROR:          http.StatusBadGateway,
	TIMEOUT:                http.StatusGatewayTimeout,
	EXTERNAL_SERVICE_ERROR: http.StatusBadGateway,
	DATABASE_ERROR:         http.StatusInternalServerError,

	VOICE_TOKEN_REFRESH_FAILED:    http.StatusInternalServerError,
	VOICE_CHANNEL_FULL:            http.StatusConflict,
	VOICE_USER_MUTED_BY_ADMIN:     http.StatusForbidden,
	VOICE_USER_KICKED:             http.StatusForbidden,
	VOICE_RECORDING_IN_PROGRESS:   http.StatusConflict,
	VOICE_RECORDING_NO_PERMISSION: http.StatusForbidden,
	VOICE_STT_UNAVAILABLE:         http.StatusServiceUnavailable,
	VOICE_QUALITY_CHANGE_FAILED:   http.StatusBadRequest,
	VOICE_NOT_IN_CHANNEL:          http.StatusBadRequest,

	SCREEN_SHARE_IN_PROGRESS:   http.StatusConflict,
	SCREEN_SHARE_NO_PERMISSION: http.StatusForbidden,

	WHITEBOARD_SYNC_FAILED:   http.StatusInternalServerError,
	WHITEBOARD_NO_PERMISSION: http.StatusForbidden,

	SEARCH_NO_RESULTS: http.StatusOK,

	BACKUP_IN_PROGRESS: http.StatusConflict,
	BACKUP_FAILED:      http.StatusInternalServerError,
	RESTORE_FAILED:     http.StatusInternalServerError,

	WS_CONNECTION_LIMIT:     http.StatusTooManyRequests,
	ROOM_ACCESS_DENIED:      http.StatusForbidden,
	CORS_ORIGIN_NOT_ALLOWED: http.StatusForbidden,

	// RemoteAssist module dedicated codes (T49 §6.16)
	REMOTE_ASSIST_NOT_FOUND:         http.StatusNotFound,
	REMOTE_ASSIST_FORBIDDEN:         http.StatusForbidden,
	REMOTE_ASSIST_EXPIRED:           http.StatusGone,
	REMOTE_ASSIST_ALREADY_ACTIVE:    http.StatusConflict,
	REMOTE_ASSIST_INVALID_STATE:     http.StatusConflict,
	REMOTE_ASSIST_PERMISSION_DENIED: http.StatusForbidden,
}

// AppError is the unified application error
type AppError struct {
	Code           ErrorCode `json:"code"`
	Message        string    `json:"message"`
	ChineseMessage string    `json:"chinese_message,omitempty"`
	Status         int       `json:"-"`
	Details        string    `json:"details,omitempty"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// New creates a new AppError
func New(code ErrorCode, message string) *AppError {
	status, ok := statusCodeMap[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &AppError{
		Code:           code,
		Message:        message,
		ChineseMessage: GetChineseMessage(code),
		Status:         status,
	}
}

// WithDetails adds details to the error
func (e *AppError) WithDetails(details string) *AppError {
	e.Details = details
	return e
}

// Common errors
var (
	ErrInternal      = New(SYSTEM_INTERNAL_ERROR, "internal server error")
	ErrBadRequest    = New(SYSTEM_BAD_REQUEST, "bad request")
	ErrNotFound      = New(SYSTEM_NOT_FOUND, "resource not found")
	ErrUnauthorized  = New(AUTH_UNAUTHORIZED, "unauthorized")
	ErrForbidden     = New(AUTH_FORBIDDEN, "forbidden")
	ErrRateLimited   = New(SYSTEM_RATE_LIMITED, "too many requests")
	ErrServerNotInit = New(AUTH_SERVER_NOT_INITIALIZED, "server not initialized")
	ErrInvalidToken  = New(AUTH_TOKEN_INVALID, "invalid token")
	ErrTokenExpired  = New(AUTH_TOKEN_EXPIRED, "token expired")
	ErrCSRFMissing   = New(AUTH_CSRF_TOKEN_MISSING, "CSRF token missing")
	ErrCSRFInvalid   = New(AUTH_CSRF_TOKEN_INVALID, "CSRF token invalid")
)

// Meta contains request metadata
type Meta struct {
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
}

// Response is the standard API response
type Response struct {
	Code           string      `json:"code"`
	Message        string      `json:"message,omitempty"`
	Details        string      `json:"details,omitempty"` // L15: detailed error message from WithDetails
	ChineseMessage string      `json:"chinese_message,omitempty"`
	Data           interface{} `json:"data,omitempty"`
	Meta           *Meta       `json:"meta,omitempty"`
}

// buildMeta creates Meta from gin context, generating/storing requestId if needed.
func buildMeta(c *gin.Context) *Meta {
	reqID, exists := c.Get("requestId")
	if !exists {
		reqID = uuid.New().String()
		c.Set("requestId", reqID)
	}
	return &Meta{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		RequestID: reqID.(string),
	}
}

// Success returns a success response and writes it to the gin context.
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    "OK",
		Message: "success",
		Data:    data,
		Meta:    buildMeta(c),
	})
}

// FromError returns an error response
func FromError(c *gin.Context, err error) Response {
	if appErr, ok := err.(*AppError); ok {
		chineseMsg := appErr.ChineseMessage
		if chineseMsg == "" {
			chineseMsg = GetChineseMessage(appErr.Code)
		}
		return Response{
			Code:           string(appErr.Code),
			Message:        appErr.Message,
			Details:        appErr.Details, // L15: include WithDetails message in response
			ChineseMessage: chineseMsg,
			Meta:           buildMeta(c),
		}
	}
	return Response{
		Code:           string(SYSTEM_INTERNAL_ERROR),
		Message:        "internal server error",
		ChineseMessage: GetChineseMessage(SYSTEM_INTERNAL_ERROR),
		Meta:           buildMeta(c),
	}
}

// GinErrorHandler returns a gin middleware that handles AppError
func GinErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			err := c.Errors.Last().Err
			if appErr, ok := err.(*AppError); ok {
				c.JSON(appErr.Status, FromError(c, appErr))
			} else {
				c.JSON(http.StatusInternalServerError, FromError(c, ErrInternal))
			}
			c.Abort()
		}
	}
}

// JSON responds with a success JSON
func JSON(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    "OK",
		Message: "success",
		Data:    data,
		Meta:    buildMeta(c),
	})
}

// JSONError responds with an error JSON and aborts.
// Uses the error's mapped HTTP status code instead of always returning 500.
func JSONError(c *gin.Context, err error) {
	if appErr, ok := err.(*AppError); ok {
		c.AbortWithStatusJSON(appErr.Status, FromError(c, appErr))
	} else {
		c.AbortWithStatusJSON(http.StatusInternalServerError, FromError(c, ErrInternal))
	}
}

// ═══════════════════════════════════════════════════════════
// Chinese Error Messages
// ═══════════════════════════════════════════════════════════

// ChineseMessages maps error codes to user-facing Chinese messages.
// Keep ErrorCode in English for programmatic handling; only the message is localized.
var ChineseMessages = map[ErrorCode]string{
	// Auth
	AUTH_INVALID_CREDENTIALS:    "邮箱或密码错误，请检查后重试",
	AUTH_TOKEN_MISSING:          "登录信息缺失，请重新登录",
	AUTH_TOKEN_INVALID:          "登录信息无效，请重新登录",
	AUTH_TOKEN_EXPIRED:          "登录已过期，请重新登录",
	AUTH_UNAUTHORIZED:           "请先登录后再进行操作",
	AUTH_FORBIDDEN:              "你没有权限执行此操作",
	AUTH_SERVER_NOT_INITIALIZED: "服务器尚未初始化，请联系管理员",
	AUTH_CSRF_TOKEN_MISSING:     "安全验证失败，请刷新页面后重试",
	AUTH_CSRF_TOKEN_INVALID:     "安全验证失败，请刷新页面后重试",
	AUTH_USER_EXISTS:            "该用户名已被使用，请更换后重试",
	AUTH_EMAIL_EXISTS:           "该邮箱已被注册，请直接登录或找回密码",
	AUTH_EMAIL_ALREADY_EXISTS:   "该邮箱已被注册，请直接登录或找回密码",
	AUTH_ACCOUNT_LOCKED:         "账号已被锁定，请30分钟后再试或联系管理员",
	AUTH_ACCOUNT_LOCK_NOTIFY:    "账号因多次失败已锁定，已发送邮件通知",
	AUTH_PASSWORD_BREACHED:      "该密码已在泄露事件中出现，请更换更安全的密码",
	AUTH_PASSWORD_REUSED:        "新密码不能与最近使用的历史密码重复",
	AUTH_USER_DISABLED:          "用户已被禁用，请联系管理员",
	AUTH_USER_NOT_FOUND:         "用户不存在",
	AUTH_RESET_TOKEN_INVALID:         "重置链接无效",
	AUTH_RESET_TOKEN_EXPIRED:         "重置链接已过期，请重新申请",
	AUTH_RESET_TOKEN_USED:            "重置链接已使用，请重新申请",
	AUTH_SECURITY_QUESTION_NOT_SET:   "该账号未设置密保问题，请联系管理员重置密码",
	AUTH_SECURITY_QUESTION_INCORRECT: "密保问题答案错误，请重新输入",
	AUTH_SECURITY_QUESTION_LOCKED:    "密保问题验证已锁定，请30分钟后再试",
	AUTH_SECURITY_QUESTION_INVALID:   "密保问题无效",
	AUTH_SECURITY_QUESTION_DUPLICATE: "不能选择重复的密保问题",
	AUTH_BOOTSTRAP_TOKEN_INVALID:     "初始化令牌无效",
	AUTH_BOOTSTRAP_TOKEN_USED:        "初始化令牌已使用",
	AUTH_ALREADY_INITIALIZED:         "服务器已初始化",
	AUTH_DISPLAY_NAME_EXISTS:         "该显示名已在本空间中使用，请更换",
	AUTH_NO_SESSION_CONTEXT:          "当前登录凭证缺少会话标识，请重新登录后再试",

	// Channel
	CHANNEL_NOT_FOUND:      "频道不存在或已被删除",
	CHANNEL_ACCESS_DENIED:  "你没有权限访问该频道",
	CHANNEL_ALREADY_EXISTS: "同名频道已存在，请使用其他名称",
	CHANNEL_NOT_VOICE:      "该频道不是语音频道",

	// Message
	MESSAGE_NOT_FOUND:    "消息不存在或已被删除",
	MESSAGE_TOO_LONG:     "消息内容过长，请缩短后重试",
	MESSAGE_RATE_LIMITED: "发送消息过于频繁，请稍后再试",

	// Voice
	VOICE_ROOM_NOT_FOUND:      "语音房间不存在，请刷新后重试",
	VOICE_JOIN_DENIED:         "无法加入语音频道，可能人数已满或无权限",
	VOICE_LIVEKIT_ERROR:       "语音服务暂时不可用，请稍后重试",
	VOICE_SERVICE_UNAVAILABLE: "语音服务暂时不可用，请稍后重试",

	// Bot
	BOT_NOT_FOUND:            "音乐机器人未在该频道启用",
	BOT_QUEUE_FULL:           "播放队列已满，请稍后再添加",
	BOT_TTS_CONSENT_REQUIRED: "使用语音合成前需要先同意使用条款",
	BOT_NETEASE_API_ERROR:    "音乐服务暂时不可用，请稍后重试",
	BOT_NETEASE_VERIFICATION_REQUIRED: "网易云要求完成安全验证，请使用二维码登录验证或暂时改用本地上传",
	BOT_TTS_NOT_CONFIGURED:   "TTS 模型未配置，请联系管理员部署模型文件",

	// File
	FILE_TOO_LARGE:     "文件过大，请上传不超过限制大小的文件",
	FILE_INVALID_TYPE:  "不支持的文件类型，请上传常见的图片、音频或文档格式",
	FILE_UPLOAD_FAILED: "文件上传失败，请检查网络后重试",

	// Admin
	ADMIN_UNAUTHORIZED:   "需要管理员权限才能执行此操作",
	ADMIN_CONFIG_INVALID: "配置参数无效，请检查输入内容",

	// System
	SYSTEM_INTERNAL_ERROR:      "服务器内部错误，请稍后重试或联系管理员",
	SYSTEM_RATE_LIMITED:        "操作过于频繁，请稍后再试",
	SYSTEM_NOT_FOUND:           "请求的资源不存在",
	SYSTEM_BAD_REQUEST:         "请求参数错误，请检查输入内容",
	SYSTEM_NOT_IMPLEMENTED:     "该功能尚未实现，敬请期待",
	SYSTEM_CONFLICT:            "请求冲突，请检查数据状态后重试",
	SYSTEM_SERVICE_UNAVAILABLE: "服务暂时不可用，请稍后重试",

	// Search
	SEARCH_QUERY_TOO_SHORT: "搜索关键词至少2个字符",

	// Notifications
	NOTIFICATION_NOT_FOUND: "通知不存在",

	// Recording
	VOICE_RECORDING_NOT_FOUND: "录制记录不存在",

	// Extended codes
	AUTH_ACCOUNT_FROZEN:       "账号已被冻结，请联系管理员解冻",
	AUTH_PASSWORD_TOO_WEAK:    "密码强度不足，请使用至少8位包含字母、数字和特殊字符的密码",
	AUTH_PASSWORD_SAME_AS_OLD: "新密码不能与旧密码相同",
	AUTH_EMAIL_NOT_VERIFIED:   "邮箱尚未验证，请先验证邮箱后再进行此操作",
	AUTH_MAX_SESSIONS_REACHED: "登录设备数量已达上限（最多5台），请先在其他设备登出",

	USER_NOT_FOUND:          "用户不存在",
	USER_ALREADY_FRIENDS:    "你们已经是好友了",
	USER_CANNOT_FRIEND_SELF: "不能添加自己为好友",

	CHANNEL_NAME_INVALID:          "频道名称无效，请使用2-50个字符",
	CHANNEL_LIMIT_REACHED:         "频道数量已达上限",
	CHANNEL_CANNOT_DELETE_GENERAL: "不能删除默认频道",

	MESSAGE_EDIT_EXPIRED:         "消息编辑时间已过期（超过24小时）",
	MESSAGE_ALREADY_DELETED:      "该消息已被删除",
	MESSAGE_CANNOT_EDIT_OTHERS:   "不能编辑他人的消息",
	MESSAGE_CANNOT_DELETE_OTHERS: "不能删除他人的消息",

	VOICE_ROOM_FULL:           "语音频道人数已满",
	VOICE_ALREADY_IN_ROOM:     "你已经在另一个语音频道中，请先离开当前频道",
	VOICE_QUALITY_UNAVAILABLE: "该音质设置当前不可用",
	VOICE_E2EE_REQUIRED:       "该房间需要端到端加密，请更新客户端版本",

	VOICE_E2EE_RECORDING_UNSUPPORTED: "该语音频道已启用端到端加密，加密房间不支持服务端录音",

	BOT_NOT_PLAYING:       "当前没有正在播放的歌曲",
	BOT_NO_NEXT_TRACK:     "已经是最后一首了",
	BOT_TRACK_NOT_FOUND:   "找不到该歌曲，可能已被移除",
	BOT_TTS_TEXT_TOO_LONG: "文本过长，TTS最多支持500字",
	BOT_TOKEN_INVALID:     "无效的机器人令牌",

	FILE_VIRUS_DETECTED:  "文件包含风险内容，已被拦截",
	FILE_STORAGE_FULL:    "服务器存储空间不足，请联系管理员",
	FILE_NOT_FOUND:       "文件不存在或已被删除",
	FILE_DOWNLOAD_FAILED: "文件下载失败，请检查网络后重试",

	WHITEBOARD_NOT_FOUND:        "白板数据不存在",
	WHITEBOARD_STROKE_TOO_LARGE: "笔迹数据过大，请简化后重试",
	// M27: Whiteboard module dedicated codes
	WHITEBOARD_ARCHIVE_NOT_FOUND:   "未找到画布存档",
	WHITEBOARD_SESSION_NOT_JOINED:  "请先加入协作会话",
	WHITEBOARD_CLEAR_NOT_ALLOWED:   "无权清空白板",
	WHITEBOARD_ARCHIVE_NOT_ALLOWED: "无权归档该白板",
	WHITEBOARD_DELETE_NOT_ALLOWED:  "无权删除该白板",

	SCREEN_SHARE_ALREADY_ACTIVE:      "你正在进行屏幕共享，请先停止当前共享",
	SCREEN_SHARE_NOT_ACTIVE:          "当前没有进行中的屏幕共享",
	SCREENSHARE_WEB_FORBIDDEN:        "Web 端不允许发起屏幕共享，请使用桌面客户端",
	SCREENSHARE_FORCE_STOP_FORBIDDEN: "仅管理员可强制终止屏幕共享",
	SCREEN_MAX_VIEWERS_REACHED:       "当前屏幕共享观看人数已达上限",
	SCREEN_SOURCE_INVALID:            "无效的屏幕共享源，请选择有效的屏幕、窗口或应用",

	CLOUDFS_FOLDER_NOT_FOUND: "文件夹不存在",
	CLOUDFS_FILE_NOT_FOUND:   "文件不存在",
	CLOUDFS_QUOTA_EXCEEDED:   "存储空间已用完，请删除不必要的文件",
	// M27: CloudFS module dedicated codes
	CLOUDFS_ALREADY_EXISTS:    "文件或目录已存在",
	CLOUDFS_NAME_TOO_LONG:     "文件名过长",
	CLOUDFS_NAME_INVALID:      "文件名包含非法字符",
	CLOUDFS_STORAGE_FULL:      "存储空间不足",
	CLOUDFS_PERMISSION_DENIED: "没有操作权限",

	// DES-2026-0912-05: 云文件分享链接
	// SHARE_VERIFY_FAILED 的文案不得区分「分享不存在」与「密码错误」。
	SHARE_NOT_FOUND:             "分享链接不存在或已失效",
	SHARE_EXPIRED:               "分享链接已过期",
	SHARE_REVOKED:               "分享链接已被撤销",
	SHARE_DOWNLOAD_LIMITED:      "分享链接的下载次数已用完",
	SHARE_UNAVAILABLE:           "分享链接当前不可用",
	SHARE_VERIFY_FAILED:         "分享校验失败：链接无效或密码错误",
	SHARE_TICKET_INVALID:        "请先通过密码校验再下载",
	SHARE_TTL_INVALID:           "有效期必须在 1 到 30 天之间",
	SHARE_MAX_DOWNLOADS_INVALID: "下载次数上限不能为负数",
	SHARE_PASSWORD_INVALID:      "分享密码不合法",

	// M27: SharedDoc module dedicated codes
	SHAREDOC_NOT_FOUND:         "文档不存在",
	SHAREDOC_TITLE_TOO_LONG:    "文档标题过长",
	SHAREDOC_CONFLICT:          "文档已被他人修改，请刷新后重试",
	SHAREDOC_PERMISSION_DENIED: "没有操作该文档的权限",

	// M27: Schedule module dedicated codes
	SCHEDULE_NOT_FOUND:         "日程事件不存在",
	SCHEDULE_TIME_INVALID:      "时间设置无效",
	SCHEDULE_TITLE_TOO_LONG:    "标题过长",
	SCHEDULE_PERMISSION_DENIED: "没有操作该日程的权限",
	SCHEDULE_INVITE_INVALID:    "日程邀请参数无效",

	// M27: Minigame module dedicated codes
	MINIGAME_NOT_FOUND:            "游戏房间不存在",
	MINIGAME_ROOM_FULL:            "游戏房间已满",
	MINIGAME_ALREADY_IN_ROOM:      "你已在该游戏房间中",
	MINIGAME_NOT_IN_ROOM:          "你尚未加入该游戏房间",
	MINIGAME_INVALID_ACTION:       "无效的游戏操作",
	MINIGAME_NOT_YOUR_TURN:        "现在不是你的回合",
	MINIGAME_GAME_ALREADY_STARTED: "游戏已经开始",
	MINIGAME_GAME_NOT_STARTED:     "游戏尚未开始",
	MINIGAME_GAME_ALREADY_ENDED:   "游戏已结束",
	MINIGAME_MODULE_DISABLED:      "小游戏模块已被禁用",

	// M27: VirtualNet module dedicated codes
	VIRTUALNET_NOT_FOUND:            "虚拟网络会话不存在",
	VIRTUALNET_ALREADY_CONNECTED:    "已连接到虚拟网络",
	VIRTUALNET_NOT_CONNECTED:        "尚未连接到虚拟网络",
	VIRTUALNET_SERVICE_UNAVAILABLE:  "虚拟网络服务暂不可用",
	VIRTUALNET_PLATFORM_UNSUPPORTED: "当前平台不支持虚拟局域网",
	VIRTUALNET_PERMISSION_DENIED:    "没有操作虚拟网络的权限",

	ADMIN_USER_NOT_FOUND:      "用户不存在",
	ADMIN_CANNOT_DELETE_OWNER: "不能删除服务器所有者",
	ADMIN_CANNOT_DEMOTE_SELF:  "不能降低自己的权限",

	// WebSocket / Realtime / CORS
	WS_CONNECTION_LIMIT:     "WebSocket 连接数已达上限，请关闭其他设备后重试",
	ROOM_ACCESS_DENIED:      "你没有权限访问该房间",
	CORS_ORIGIN_NOT_ALLOWED: "请求来源不被允许",

	CLIENT_VERSION_UNSUPPORTED: "客户端版本过低，请更新到最新版本",
	CLIENT_VERSION_TOO_OLD:     "客户端版本与服务端不兼容，请更新",
	SERVER_MAINTENANCE:         "服务器正在维护中，请稍后再试",

	VALIDATION_FAILED:      "输入验证失败，请检查表单内容",
	INVALID_JSON:           "请求格式错误，请检查数据格式",
	MISSING_REQUIRED_FIELD: "缺少必填字段，请补充完整",
	INVALID_FIELD_FORMAT:   "字段格式错误，请按正确格式输入",
	VALUE_OUT_OF_RANGE:     "数值超出允许范围",

	NETWORK_ERROR:          "网络连接异常，请检查网络后重试",
	TIMEOUT:                "请求超时，请稍后重试",
	EXTERNAL_SERVICE_ERROR: "外部服务暂时不可用，请稍后重试",
	DATABASE_ERROR:         "数据库操作失败，请稍后重试或联系管理员",

	VOICE_TOKEN_REFRESH_FAILED:    "语音令牌刷新失败，请重新加入语音频道",
	VOICE_CHANNEL_FULL:            "语音频道人数已满，请稍后再试",
	VOICE_USER_MUTED_BY_ADMIN:     "你已被管理员静音",
	VOICE_USER_KICKED:             "你已被管理员移出语音频道",
	VOICE_RECORDING_IN_PROGRESS:   "该频道正在录制中",
	VOICE_RECORDING_NO_PERMISSION: "你没有权限操作录制",
	VOICE_STT_UNAVAILABLE:         "语音转文字服务暂不可用",
	VOICE_QUALITY_CHANGE_FAILED:   "语音音质设置失败",
	VOICE_NOT_IN_CHANNEL:          "你不在该语音频道中",

	SCREEN_SHARE_IN_PROGRESS:   "该频道正在有人共享屏幕",
	SCREEN_SHARE_NO_PERMISSION: "你没有权限共享屏幕",

	WHITEBOARD_SYNC_FAILED:   "白板同步失败，请刷新后重试",
	WHITEBOARD_NO_PERMISSION: "你没有权限操作白板",

	SEARCH_NO_RESULTS: "未找到相关消息",

	BACKUP_IN_PROGRESS: "备份正在进行中，请稍后再试",
	BACKUP_FAILED:      "备份失败，请检查磁盘空间后重试",
	RESTORE_FAILED:     "恢复失败，请检查备份文件完整性",

	// RemoteAssist module dedicated codes (T49 §6.16)
	REMOTE_ASSIST_NOT_FOUND:         "远程协助会话不存在",
	REMOTE_ASSIST_FORBIDDEN:         "你没有权限发起远程协助请求",
	REMOTE_ASSIST_EXPIRED:           "远程协助请求已过期",
	REMOTE_ASSIST_ALREADY_ACTIVE:    "已有进行中的远程协助会话",
	REMOTE_ASSIST_INVALID_STATE:     "远程协助会话状态不允许此操作",
	REMOTE_ASSIST_PERMISSION_DENIED: "你没有权限执行此远程控制操作",
}

// GetChineseMessage returns the Chinese user-facing message for an error code.
// Falls back to empty string if no Chinese translation exists.
func GetChineseMessage(code ErrorCode) string {
	if msg, ok := ChineseMessages[code]; ok {
		return msg
	}
	return ""
}
