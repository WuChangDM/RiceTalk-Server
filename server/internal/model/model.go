package model

import (
	"time"

	"gorm.io/gorm"
)

// User represents a registered user
type User struct {
	ID            string         `gorm:"primaryKey;size:64" json:"id"`
	Username      string         `gorm:"uniqueIndex;size:32;not null" json:"username"`
	Email         string         `gorm:"uniqueIndex;size:255;not null" json:"email"`
	PasswordHash  string         `gorm:"size:255;not null" json:"-"`
	Avatar        string         `gorm:"size:512" json:"avatar"`
	DisplayName   string         `gorm:"size:64" json:"displayName"`
	CustomStatus  string         `gorm:"size:128" json:"customStatus"`
	Role          string         `gorm:"size:16;default:'MEMBER';index" json:"role"` // OWNER, ADMIN, MEMBER
	IsActive      bool           `gorm:"default:true;index" json:"isActive"`
	TokenVersion  int64          `gorm:"default:0" json:"tokenVersion"`
	EmailVerified bool           `gorm:"default:false" json:"emailVerified"`
	Theme         string         `gorm:"size:16;default:'dark'" json:"theme"`
	LastLoginAt   *time.Time     `json:"lastLoginAt"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// Space represents a server/workspace
type Space struct {
	ID        string         `gorm:"primaryKey;size:64" json:"id"`
	Name      string         `gorm:"size:64;not null" json:"name"`
	Icon      string         `gorm:"size:512" json:"icon"`
	OwnerID   string         `gorm:"size:64;not null;index" json:"ownerId"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// Membership represents a user's membership in a space
type Membership struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	UserID    string    `gorm:"size:64;not null;index:idx_membership_space_user" json:"userId"`
	SpaceID   string    `gorm:"size:64;not null;index:idx_membership_space_user" json:"spaceId"`
	Role      string    `gorm:"size:16;default:'MEMBER';index" json:"role"` // OWNER, ADMIN, MEMBER
	JoinedAt  time.Time `json:"joinedAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// Channel represents a text or voice channel. Legacy DM rows may remain in
// storage, but are not part of the current public product surface.
type Channel struct {
	ID              string  `gorm:"primaryKey;size:64" json:"id"`
	SpaceID         string  `gorm:"size:64;not null;index;uniqueIndex:idx_channel_space_name_type;index:idx_channel_space_sort,priority:1" json:"spaceId"`
	Name            string  `gorm:"size:64;not null;uniqueIndex:idx_channel_space_name_type" json:"name"`
	Type            string  `gorm:"size:16;not null;uniqueIndex:idx_channel_space_name_type;index" json:"type"` // text, voice; dm is legacy-only
	Visibility      string  `gorm:"size:20;default:'public';index" json:"visibility"`                           // public, admin-only (private to admins), role-specific (private to granted roles)
	VoiceQuality    string  `gorm:"size:16;default:'standard'" json:"voiceQuality"`                             // fluent, standard, high, ultra
	PinnedMessageID *string `gorm:"size:64" json:"pinnedMessageId"`
	Position        int     `gorm:"default:0;index:idx_channel_space_sort,priority:2" json:"position"` // 兼容字段
	SortGroup       string  `gorm:"size:32;default:''" json:"sortGroup"`                               // 分组排序，如 "text", "voice"
	// H2: per-channel permission overrides stored as JSON.
	// Empty string means default behavior (driven by Visibility field).
	Permissions string         `gorm:"type:text" json:"permissions,omitempty"`
	CreatedBy   string         `gorm:"size:64;index" json:"createdBy"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// DMChannel links two users to a shared direct-message channel.
type DMChannel struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ChannelID string    `gorm:"size:64;not null;uniqueIndex:idx_dm_channel" json:"channelId"`
	UserAID   string    `gorm:"column:user_aid;size:64;not null;uniqueIndex:idx_dm_channel;index:idx_dm_user_a" json:"userAId"`
	UserBID   string    `gorm:"column:user_bid;size:64;not null;uniqueIndex:idx_dm_channel;index:idx_dm_user_b" json:"userBId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MessageAttachment represents a file/image/video attached to a chat message
type MessageAttachment struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	MessageID   string    `gorm:"size:64;not null;index" json:"messageId"`
	Type        string    `gorm:"size:16;not null" json:"type"` // image, video, file
	FileName    string    `gorm:"size:255;not null" json:"fileName"`
	FileSize    int64     `json:"fileSize"`
	MimeType    string    `gorm:"size:64" json:"mimeType"`
	FileURL     string    `gorm:"size:512" json:"fileUrl"`
	ThumbURL    string    `gorm:"size:512" json:"thumbUrl"`
	Width       int       `gorm:"default:0" json:"width"`
	Height      int       `gorm:"default:0" json:"height"`
	CloudFileID string    `gorm:"size:64" json:"cloudFileId"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Message represents a chat message
type Message struct {
	ID                string              `gorm:"primaryKey;size:64" json:"id"`
	ChannelID         string              `gorm:"size:64;not null;index;uniqueIndex:idx_msg_client_channel;index:idx_msg_channel_created,priority:1" json:"channelId"`
	UserID            string              `gorm:"size:64;not null;index" json:"userId"`
	AuthorUsername    string              `gorm:"size:32;default:''" json:"authorUsername"`
	AuthorDisplayName string              `gorm:"size:64;default:''" json:"authorDisplayName"`
	AuthorAvatarURL   string              `gorm:"size:512;default:''" json:"authorAvatarUrl"`
	AuthorRole        string              `gorm:"size:16;default:'MEMBER'" json:"authorRole"`
	Content           string              `gorm:"type:text;not null" json:"content"`
	Type              string              `gorm:"size:20;default:'text';index" json:"type"` // text, image, video, file, mixed, system
	ParentID          *string             `gorm:"size:64;index" json:"parentId"`
	ClientMessageID   *string             `gorm:"size:64;uniqueIndex:idx_msg_client_channel" json:"clientMessageId"`
	Reactions         []Reaction          `gorm:"-" json:"reactions,omitempty"`
	Attachments       []MessageAttachment `gorm:"-" json:"attachments,omitempty"`
	// MentionUserIDs is a transient set of user IDs mentioned in this message's
	// content (stored inline as <@user_xxx> tokens). Not persisted; used by the
	// handler to push mention notifications and bump MentionCount after insert.
	MentionUserIDs map[string]bool `gorm:"-" json:"-"`
	EditedAt       *time.Time      `json:"editedAt"`
	CreatedAt      time.Time       `gorm:"index:idx_msg_channel_created,priority:2" json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	DeletedAt      gorm.DeletedAt  `gorm:"index" json:"-"`
}

// Reaction represents an emoji reaction on a message
type Reaction struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	MessageID string    `gorm:"size:64;not null;uniqueIndex:idx_reaction_user_msg" json:"messageId"`
	UserID    string    `gorm:"size:64;not null;uniqueIndex:idx_reaction_user_msg" json:"userId"`
	Emoji     string    `gorm:"size:32;not null;uniqueIndex:idx_reaction_user_msg" json:"emoji"`
	CreatedAt time.Time `json:"createdAt"`
}

// UserSession stores active login sessions
type UserSession struct {
	ID             string `gorm:"primaryKey;size:64" json:"id"`
	UserID         string `gorm:"size:64;not null;index" json:"userId"`
	TokenHash      string `gorm:"size:255;uniqueIndex" json:"-"`
	ExpiresAt      time.Time `gorm:"not null" json:"expiresAt"`
	IsAdminSession bool      `gorm:"default:false" json:"isAdminSession"`
	// S-2 多设备标识：登录端上报的设备类型（如 windows-desktop/web）与设备名（如主机名）。
	// 均为可选字段，缺省为空串；写入前由 auth.Service 截断（type≤32 / name≤64 rune）。
	DeviceType string `gorm:"size:32;default:''" json:"deviceType"`
	DeviceName string `gorm:"size:64;default:''" json:"deviceName"`
	// A4-S1（DES-20261001-01 §5.2）：会话来源网络信息。由服务端在登录与
	// refresh 时自动采集（ip = ClientIP，user_agent = 请求头截断 256 rune），
	// 客户端不可自报；老会话行为空串。用途：登录设备列表展示来源 IP
	// （脱敏由客户端完成）+ 异地登录提醒的网段比对。仅会话归属者本人可见。
	IP        string `gorm:"size:45;default:''" json:"ip"`
	UserAgent string `gorm:"size:256;default:''" json:"userAgent"`
	// LastActiveAt 记录该会话最近一次通过 refresh 轮换续期的时间（refresh 是
	// 客户端活跃的确定性信号）；新登录时等于 CreatedAt。
	LastActiveAt time.Time `json:"lastActiveAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

// E2EEKey stores per-room end-to-end encryption keys
// The key is encrypted at rest using the server's JWTSecret.
type E2EEKey struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	RoomID    string    `gorm:"size:64;not null;index;uniqueIndex:idx_e2ee_key_room" json:"roomId"`
	KeyBytes  string    `gorm:"size:512;not null" json:"-"` // base64 encoded AES-256 encrypted key
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AdminBootstrapToken stores the one-time bootstrap token
// TokenHash is bcrypt hash of the plaintext token (never store plaintext)
type AdminBootstrapToken struct {
	ID        string     `gorm:"primaryKey;size:64" json:"id"`
	TokenHash string     `gorm:"size:255;not null;uniqueIndex" json:"-"`
	ExpiresAt *time.Time `gorm:"index" json:"expiresAt"`
	Used      bool       `gorm:"default:false" json:"used"`
	CreatedAt time.Time  `json:"createdAt"`
	UsedAt    *time.Time `json:"usedAt"`
}

// UserPresence tracks online status
type UserPresence struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	UserID       string    `gorm:"size:64;not null;uniqueIndex" json:"userId"`
	Status       string    `gorm:"size:16;default:'offline'" json:"status"` // online, away, dnd, offline, invisible, gaming
	CustomStatus string    `gorm:"size:128" json:"customStatus"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// UserChannelRead tracks last read message per channel
type UserChannelRead struct {
	ID                string    `gorm:"primaryKey;size:64" json:"id"`
	UserID            string    `gorm:"size:64;not null;uniqueIndex:idx_user_channel" json:"userId"`
	ChannelID         string    `gorm:"size:64;not null;uniqueIndex:idx_user_channel;index" json:"channelId"`
	MessageID         string    `gorm:"size:64;index" json:"messageId"` // legacy field, kept for backward compatibility
	LastReadMessageID string    `gorm:"size:64;index" json:"lastReadMessageId"`
	UnreadCount       int       `gorm:"default:0" json:"unreadCount"`
	MentionCount      int       `gorm:"default:0" json:"mentionCount"`
	ReadAt            time.Time `json:"readAt"`
}

// UserChannelMute 记录用户对某个频道的静音（T4/S-1 频道级静音）。
//
// 语义对齐 Discord：
//   - MutedUntil == nil 表示永久静音；
//   - MutedUntil 非 nil 且已过期视为未静音（查询侧过滤，不做后台清理任务）；
//   - 同一 (user_id, channel_id) 只允许一行，重复开启静音走删除重建（事务内）。
type UserChannelMute struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	UserID     string     `gorm:"size:64;not null;uniqueIndex:idx_user_channel_mute" json:"userId"`
	ChannelID  string     `gorm:"size:64;not null;uniqueIndex:idx_user_channel_mute;index" json:"channelId"`
	MutedUntil *time.Time `json:"mutedUntil"` // nil = 永久静音
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// ChannelRolePermission stores channel-level role permissions
type ChannelRolePermission struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ChannelID string    `gorm:"size:64;not null;uniqueIndex:idx_channel_role_perm" json:"channelId"`
	Role      string    `gorm:"size:16;not null;uniqueIndex:idx_channel_role_perm" json:"role"` // OWNER, ADMIN, MEMBER
	CanView   bool      `gorm:"default:true" json:"canView"`
	CanWrite  bool      `gorm:"default:true" json:"canWrite"`
	CanManage bool      `gorm:"default:false" json:"canManage"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OGCache caches Open Graph metadata
type OGCache struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	URL         string    `gorm:"size:1024;not null;uniqueIndex" json:"url"`
	Title       string    `gorm:"size:512" json:"title"`
	Description string    `gorm:"size:2048" json:"description"`
	Image       string    `gorm:"size:2048" json:"image"`
	ExpiresAt   time.Time `json:"expiresAt"`
	CreatedAt   time.Time `json:"createdAt"`
}

// VoiceRoom represents an independent voice room state.
// A room belongs to a Space and may optionally be bound to a text/voice channel.
type VoiceRoom struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	SpaceID       string    `gorm:"size:64;not null;index" json:"spaceId"`
	BindChannelID *string   `gorm:"size:64;index" json:"bindChannelId,omitempty"`
	LiveKitRoom   string    `gorm:"size:128" json:"liveKitRoom"`
	E2EEEnabled   bool      `gorm:"default:false" json:"e2eeEnabled"`
	Quality       string    `gorm:"size:16;default:'standard'" json:"quality"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// VoiceParticipant tracks who is in a voice room
type VoiceParticipant struct {
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	RoomID           string     `gorm:"size:64;not null;index:idx_voice_participant_room_user" json:"roomId"`
	UserID           string     `gorm:"size:64;not null;index:idx_voice_participant_room_user" json:"userId"`
	IsMuted          bool       `gorm:"default:false" json:"isMuted"`
	IsSpeaking       bool       `gorm:"default:false" json:"isSpeaking"`
	IsScreenSharing  bool       `gorm:"default:false" json:"isScreenSharing"`
	SpeakerMuted     bool       `gorm:"default:false" json:"speakerMuted"` // 扬声器静音
	LastLatencyMs    int        `gorm:"default:0" json:"lastLatencyMs"`    // 最后延迟
	JoinedAt         time.Time  `json:"joinedAt"`
	LastActiveAt     time.Time  `json:"lastActiveAt"`
	LeftAt           *time.Time `json:"leftAt"`                                                                       // 离开时间
	DisconnectReason string     `gorm:"size:32" json:"disconnectReason"`                                              // 离开原因
	LiveKitSid       string     `gorm:"column:livekit_sid;size:64;index:idx_voice_participant_sid" json:"livekitSid"` // LiveKit 参与者会话标识符，用于精确匹配会话
}

// BotPlayQueue stores the music queue
type BotPlayQueue struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	BotID     string    `gorm:"size:64;not null;index" json:"botId"`
	TrackID   string    `gorm:"size:128" json:"trackId"`
	Title     string    `gorm:"size:256" json:"title"`
	Artist    string    `gorm:"size:128" json:"artist"`
	Duration  int       `json:"duration"`
	Cover     string    `gorm:"size:512" json:"cover"`
	Album     string    `gorm:"size:128" json:"album"`
	Source    string    `gorm:"size:16;default:'netease'" json:"source"` // netease, upload, tts
	Priority  int       `gorm:"default:0" json:"priority"`
	Position  int       `gorm:"default:0" json:"position"`
	Volume    int       `gorm:"default:80" json:"volume"`               // 0-100
	Status    string    `gorm:"size:16;default:'queued'" json:"status"` // queued, playing, paused, skipped
	AddedBy   string    `gorm:"size:64" json:"addedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// BotUploadAudio stores uploaded audio files
type BotUploadAudio struct {
	ID            string         `gorm:"primaryKey;size:64" json:"id"`
	UserID        string         `gorm:"size:64;not null;index" json:"userId"`
	Title         string         `gorm:"size:256" json:"title"`
	Artist        string         `gorm:"size:128" json:"artist"`
	Duration      int            `json:"duration"`
	FilePath      string         `gorm:"size:512" json:"filePath"`
	FileSize      int64          `json:"fileSize"`
	MimeType      string         `gorm:"size:64" json:"mimeType"`
	LastPlayedAt  *time.Time     `gorm:"index" json:"lastPlayedAt,omitempty"`
	PendingDelete bool           `gorm:"default:false;index" json:"pendingDelete"`
	CreatedAt     time.Time      `json:"createdAt"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// UserSound stores a user-uploaded entrance/exit sound effect (shared soundboard library).
// Preset sounds (IsPreset=true) ship with the server, are available to everyone and cannot be deleted.
type UserSound struct {
	ID        string         `gorm:"primaryKey;size:64" json:"id"`
	UserID    string         `gorm:"size:64;not null;index" json:"userId"` // preset sounds use "system"
	Title     string         `gorm:"size:256" json:"title"`
	FilePath  string         `gorm:"size:512" json:"filePath"` // relative to LocalDataPath
	FileSize  int64          `json:"fileSize"`
	MimeType  string         `gorm:"size:64" json:"mimeType"`
	Duration  int            `json:"duration"` // seconds, probed via ffprobe
	IsPreset  bool           `gorm:"default:false;index" json:"isPreset"`
	CreatedAt time.Time      `json:"createdAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// UserSoundFavorite records a user's favorited sounds (any uploader's, for quick access).
type UserSoundFavorite struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	UserID    string    `gorm:"size:64;not null;index:idx_sound_fav_user_sound,unique" json:"userId"`
	SoundID   string    `gorm:"size:64;not null;index:idx_sound_fav_user_sound,unique" json:"soundId"`
	CreatedAt time.Time `json:"createdAt"`
}

// UserSoundSettings stores the user's picked join/leave sounds (server-side, cross-device).
type UserSoundSettings struct {
	UserID       string    `gorm:"primaryKey;size:64" json:"userId"`
	JoinSoundID  *string   `gorm:"size:64" json:"joinSoundId"`
	LeaveSoundID *string   `gorm:"size:64" json:"leaveSoundId"`
	JoinVolume   int       `gorm:"default:100" json:"joinVolume"`  // 0-200
	LeaveVolume  int       `gorm:"default:100" json:"leaveVolume"` // 0-200
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// BotPlayerState stores the current playback state for a bot
type BotPlayerState struct {
	BotID          string    `gorm:"primaryKey;size:64;not null" json:"botId"`
	CurrentTrackID string    `gorm:"size:64" json:"currentTrackId"`
	Playing        bool      `gorm:"default:false" json:"playing"`
	Paused         bool      `gorm:"default:false" json:"paused"`
	CurrentTime    int       `gorm:"default:0" json:"currentTime"`
	Volume         int       `gorm:"default:80" json:"volume"`
	PlayMode       string    `gorm:"size:16;default:'order'" json:"playMode"` // order, random, repeat-one, repeat-all
	UpdatedAt      time.Time `json:"updatedAt"`
}

// BotTTSMessage stores TTS synthesized messages
type BotTTSMessage struct {
	ID                 string     `gorm:"primaryKey;size:64" json:"id"`
	UserID             string     `gorm:"size:64;not null;index" json:"userId"`
	BotID              string     `gorm:"size:64;not null;index" json:"botId"`
	Text               string     `gorm:"type:text;not null" json:"text"`
	VoiceName          string     `gorm:"size:64" json:"voiceName"`
	Speed              float64    `gorm:"default:1.0" json:"speed"`
	Pitch              float64    `gorm:"default:0" json:"pitch"`
	Volume             float64    `gorm:"default:1.0" json:"volume"`
	VoiceDataPath      string     `gorm:"size:512" json:"voiceDataPath"`
	VoiceDataSensitive bool       `gorm:"default:true" json:"voiceDataSensitive"`
	PlayedAt           *time.Time `json:"playedAt"`
	ExpiresAt          time.Time  `gorm:"index" json:"expiresAt"`
	CreatedAt          time.Time  `json:"createdAt"`
}

// TTSConsent tracks user consent for voice data processing
type TTSConsent struct {
	ID          string     `gorm:"primaryKey;size:64" json:"id"`
	UserID      string     `gorm:"size:64;not null;uniqueIndex" json:"userId"`
	Consented   bool       `gorm:"default:false" json:"consented"`
	ConsentedAt *time.Time `json:"consentedAt"`
	RevokedAt   *time.Time `json:"revokedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Whiteboard represents an independent space-level whiteboard.
type Whiteboard struct {
	ID           string     `gorm:"primaryKey;size:64" json:"id"`
	SpaceID      string     `gorm:"size:64;not null;index" json:"spaceId"`
	Name         string     `gorm:"size:128;not null" json:"name"`
	Archived     bool       `gorm:"default:false;index" json:"archived"`
	CreatedBy    string     `gorm:"size:64;not null;index" json:"createdBy"`
	ThumbnailURL string     `gorm:"size:512" json:"thumbnailUrl"`
	StrokeCount  int64      `gorm:"default:0" json:"strokeCount"`
	LastDrawnAt  *time.Time `json:"lastDrawnAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// WhiteboardStroke stores drawing strokes
type WhiteboardStroke struct {
	ID              string    `gorm:"primaryKey;size:32" json:"id"` // L18: size 64→32 per design doc §7.6
	WhiteboardID    string    `gorm:"size:64;not null;index" json:"whiteboardId"`
	UserID          string    `gorm:"size:64;not null" json:"userId"`
	AuthorName      string    `gorm:"size:64;not null" json:"authorName"` // L20: 作者名快照
	Type            string    `gorm:"size:16" json:"type"`                // 旧字段，保留用于读取旧数据
	Tool            string    `gorm:"size:16;not null" json:"tool"`       // L19: 新字段，工具枚举 pen/eraser/rect/ellipse
	Data            string    `gorm:"type:text" json:"data"`              // JSON encoded stroke data (encrypted when Encrypted=true)
	Color           string    `gorm:"size:16" json:"color"`
	Width           float64   `json:"width"`
	Layer           int       `gorm:"default:0" json:"layer"`
	Encrypted       bool      `gorm:"default:false" json:"encrypted"`
	EncryptionKeyID string    `gorm:"size:64" json:"encryptionKeyId"`
	CreatedAt       time.Time `json:"createdAt"`
}

// FileMetadata stores uploaded file information
type FileMetadata struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	UserID    string    `gorm:"size:64;not null" json:"userId"`
	FileName  string    `gorm:"size:256" json:"fileName"`
	FilePath  string    `gorm:"size:512" json:"filePath"`
	FileSize  int64     `json:"fileSize"`
	MimeType  string    `gorm:"size:64" json:"mimeType"`
	Checksum  string    `gorm:"size:64" json:"checksum"`
	CreatedAt time.Time `json:"createdAt"`
}

// SharedFolder stores cloud folder structure
//
// DES-2026-0912-02 §2.5: idx_folder_parent_name is a PARTIAL unique index
// (WHERE deleted_at IS NULL). Rows are deleted via GORM soft delete
// (DeletedAt), so a full unique index would keep blocking re-creation of a
// name whose previous incarnation was deleted.
type SharedFolder struct {
	ID               string         `gorm:"primaryKey;size:64" json:"id"`
	SpaceID          string         `gorm:"size:64;not null;index" json:"spaceId"`
	ParentID         *string        `gorm:"size:64;index;uniqueIndex:idx_folder_parent_name,where:deleted_at IS NULL" json:"parentId"`
	Name             string         `gorm:"size:128;not null;uniqueIndex:idx_folder_parent_name,where:deleted_at IS NULL" json:"name"`
	Path             string         `gorm:"size:512;not null;index" json:"path"`
	OwnerID          string         `gorm:"size:64;not null" json:"ownerId"`
	PermissionPolicy string         `gorm:"type:text" json:"permissionPolicy"` // JSON: {"read":["role:admin"],"write":["user:xxx"]}
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

// SharedFileEntry stores files within cloud folders
//
// DES-2026-0912-02 §2.5: idx_file_folder_name is a PARTIAL unique index
// (WHERE deleted_at IS NULL) for the same reason as SharedFolder above.
type SharedFileEntry struct {
	ID           string         `gorm:"primaryKey;size:64" json:"id"`
	SpaceID      string         `gorm:"size:64;not null;index" json:"spaceId"`
	FolderID     string         `gorm:"size:64;not null;index;uniqueIndex:idx_file_folder_name,where:deleted_at IS NULL" json:"folderId"`
	FileName     string         `gorm:"size:256;not null;uniqueIndex:idx_file_folder_name,where:deleted_at IS NULL" json:"fileName"`
	PhysicalName string         `gorm:"size:512" json:"physicalName"`
	FilePath     string         `gorm:"size:512;index" json:"filePath"`
	FileSize     int64          `json:"fileSize"`
	MimeType     string         `gorm:"size:64" json:"mimeType"`
	FileExt      string         `gorm:"size:32" json:"fileExt"`     // L16: 扩展名
	VersionNo    int            `gorm:"default:1" json:"versionNo"` // L16: 版本号
	UploadedBy   string         `gorm:"size:64" json:"uploadedBy"`
	CreatedAt    time.Time      `json:"createdAt"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// SharedDocument stores collaborative documents
type SharedDocument struct {
	ID               string         `gorm:"primaryKey;size:64" json:"id"`
	SpaceID          string         `gorm:"size:64;not null;index" json:"spaceId"`
	Title            string         `gorm:"size:128;not null" json:"title"`
	Content          string         `gorm:"type:text" json:"content"`
	OwnerID          string         `gorm:"size:64;not null" json:"ownerId"`
	PermissionPolicy string         `gorm:"type:text" json:"permissionPolicy"` // JSON: {"read":["role:admin"],"write":["user:xxx"]}
	Version          int            `gorm:"default:1" json:"version"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

// SharedDocumentVersion stores document version history (M25)
type SharedDocumentVersion struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	DocumentID string    `gorm:"size:64;not null;index:idx_doc_version_doc,priority:1" json:"documentId"`
	VersionNo  int       `gorm:"not null;index:idx_doc_version_doc,priority:2" json:"versionNo"` // 版本号
	Title      string    `gorm:"size:128;not null" json:"title"`                                 // 标题快照
	Content    string    `gorm:"type:text" json:"content"`                                       // 内容快照
	EditedBy   string    `gorm:"size:64;not null" json:"editedBy"`                               // 编辑者
	Note       string    `gorm:"type:text" json:"note"`                                          // 版本备注（Phase 2）
	CreatedAt  time.Time `json:"createdAt"`                                                      // 版本创建时间
}

// SharedDocumentComment stores comments and replies on a document (Phase 2)
type SharedDocumentComment struct {
	ID           string         `gorm:"primaryKey;size:64" json:"id"`
	DocumentID   string         `gorm:"size:64;not null;index" json:"documentId"`
	ParentID     *string        `gorm:"size:64;index" json:"parentId,omitempty"`
	AnchorText   string         `gorm:"type:text" json:"anchorText,omitempty"`
	AnchorOffset *int           `gorm:"default:0" json:"anchorOffset,omitempty"`
	AnchorLength *int           `gorm:"default:0" json:"anchorLength,omitempty"`
	Content      string         `gorm:"type:text;not null" json:"content"`
	CreatedBy    string         `gorm:"size:64;not null" json:"createdBy"`
	Resolved     bool           `gorm:"default:false" json:"resolved"`
	ResolvedBy   *string        `gorm:"size:64" json:"resolvedBy,omitempty"`
	ResolvedAt   *time.Time     `json:"resolvedAt,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// ScheduleEvent stores calendar events
type ScheduleEvent struct {
	ID              string         `gorm:"primaryKey;size:64" json:"id"`
	SpaceID         string         `gorm:"size:64;not null;index:idx_schedule_space_date,priority:1" json:"spaceId"`
	Title           string         `gorm:"size:128;not null" json:"title"`
	Description     string         `gorm:"type:text" json:"description"`
	Location        string         `gorm:"size:256" json:"location"`
	EventDate       time.Time      `gorm:"type:date;not null;index:idx_schedule_event_date" json:"eventDate"` // L25: 日期（仅日期部分），用于月/周/日视图查询
	StartTime       time.Time      `gorm:"index:idx_schedule_space_date,priority:2" json:"startTime"`
	EndTime         time.Time      `json:"endTime"`
	AllDay          bool           `gorm:"default:false" json:"allDay"`
	Scope           string         `gorm:"size:32;not null;default:all;index" json:"scope"` // M24: all / admin_only
	ReminderMinutes int            `gorm:"default:0" json:"reminderMinutes"`                // L26: 提前提醒分钟数（0=不提醒）
	ReminderSent    bool           `gorm:"default:false" json:"reminderSent"`               // L26: 是否已发送提醒（防重复）
	InviteeIDs      []string       `gorm:"type:jsonb;serializer:json" json:"inviteeIds"`    // A8-S1: 受邀人快照（空数组=普通事件，行为与无邀请完全一致）
	CreatedBy       string         `gorm:"size:64;index" json:"createdBy"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

// ScheduleEventInvite stores per-user RSVP state for a schedule event (A8-S1).
// 一个事件对每个用户最多一条邀请（(event_id, user_id) 唯一）；
// status: pending / accepted / declined；responded_at 仅在受邀人应答后写入。
type ScheduleEventInvite struct {
	ID          string     `gorm:"primaryKey;size:64" json:"id"`
	EventID     string     `gorm:"size:64;not null;uniqueIndex:idx_schedule_invite_event_user,priority:1" json:"eventId"`
	UserID      string     `gorm:"size:64;not null;uniqueIndex:idx_schedule_invite_event_user,priority:2;index:idx_schedule_invite_user" json:"userId"`
	Status      string     `gorm:"size:16;not null;default:pending" json:"status"` // pending / accepted / declined
	RespondedAt *time.Time `json:"respondedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// MinigameSession stores active game sessions
type MinigameSession struct {
	ID             string     `gorm:"primaryKey;size:64" json:"id"`
	GameType       string     `gorm:"size:32;not null;index:idx_minigame_space" json:"gameType"`
	SpaceID        string     `gorm:"size:64;not null;index:idx_minigame_space" json:"spaceId"` // 房间归属 Space
	HostID         string     `gorm:"size:64;not null" json:"hostId"`
	State          string     `gorm:"type:text" json:"state"`                                           // JSON game state
	Status         string     `gorm:"size:16;default:'waiting';index:idx_minigame_space" json:"status"` // waiting, playing, paused, ended
	PlayersJSON    string     `gorm:"type:text" json:"playersJson"`                                     // JSON: [{"userId":"xxx","score":0,"joinedAt":"..."}]
	SpectatorsJSON string     `gorm:"type:text" json:"spectatorsJson"`                                  // JSON: [{"userId":"xxx","username":"...","joinedAt":"..."}]
	IsActive       bool       `gorm:"default:true" json:"isActive"`                                     // 兼容字段
	LastJoinedAt   *time.Time `json:"lastJoinedAt"`                                                     // 最后有玩家加入的时间，用于 10 分钟无进入清理
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// MinigameLeaderboard stores per-user best scores for single-player minigames.
type MinigameLeaderboard struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	GameType  string    `gorm:"size:32;not null;index:idx_minigame_leaderboard_game_score" json:"gameType"`
	UserID    string    `gorm:"size:64;not null;index:idx_minigame_leaderboard_user" json:"userId"`
	Username  string    `gorm:"size:64;not null" json:"username"`
	BestScore int64     `gorm:"default:0" json:"bestScore"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MinigameHistory records a completed game session for history/audit purposes.
// 每局游戏结束时写入一条记录，永久保留（参见设计文档 §15.4）。
type MinigameHistory struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	GameType    string    `gorm:"size:32;not null;index:idx_minigame_history_game" json:"gameType"`
	SpaceID     string    `gorm:"size:64;not null;index:idx_minigame_history_space" json:"spaceId"`
	SessionID   string    `gorm:"size:64" json:"sessionId"`
	HostID      string    `gorm:"size:64;not null" json:"hostId"`
	HostName    string    `gorm:"size:64" json:"hostName"`
	WinnerID    string    `gorm:"size:64" json:"winnerId"`
	WinnerName  string    `gorm:"size:64" json:"winnerName"`
	IsDraw      bool      `gorm:"default:false" json:"isDraw"`
	PlayersJSON string    `gorm:"type:text" json:"playersJson"`
	FinalScores string    `gorm:"type:text" json:"finalScores"`
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt"`
	Duration    int64     `gorm:"default:0" json:"duration"` // 持续时长（秒）
	CreatedAt   time.Time `json:"createdAt"`
}

// VirtualNetSession stores virtual network sessions
// DES-2026-0731-02: 移除 network_cidr 字段，新增 network_name 字段以适配 EasyTier。
// EasyTier 是去中心化组网工具，客户端通过相同的 network_name + network_secret 加入同一网络。
type VirtualNetSession struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	SpaceID     string    `gorm:"size:64;not null;index" json:"spaceId"`
	UserID      string    `gorm:"size:64;not null;index:idx_vnet_user_status" json:"userId"`
	NodeID      string    `gorm:"size:64" json:"nodeId"`
	Status      string    `gorm:"size:16;default:'disconnected';index:idx_vnet_user_status" json:"status"`
	IP          string    `gorm:"size:45" json:"ip"`
	NetworkName string    `gorm:"size:64" json:"networkName"` // DES-2026-0731-02: EasyTier 网络名称（替代 NetworkCIDR）
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// VirtualNetNode stores virtual network nodes
type VirtualNetNode struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	SpaceID    string     `gorm:"size:64;not null;index" json:"spaceId"`
	SessionID  string     `gorm:"size:64;not null;index" json:"sessionId"`
	Name       string     `gorm:"size:64" json:"name"`
	IP         string     `gorm:"size:45" json:"ip"`
	Status     string     `gorm:"size:16;default:'active'" json:"status"`
	LatencyMs  int        `gorm:"default:0" json:"latencyMs"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// AdminConfig stores server-wide admin configuration
type AdminConfig struct {
	ID             string `gorm:"primaryKey;size:64" json:"id"`
	ServerName     string `gorm:"size:64" json:"serverName"`
	AllowRegister  bool   `gorm:"default:true" json:"allowRegister"`
	APIPort        int    `gorm:"default:8080" json:"apiPort"`
	AdminPort      int    `gorm:"default:9090" json:"adminPort"`
	LiveKitPort    int    `gorm:"default:7880" json:"livekitPort"`
	LiveKitTCPPort int    `gorm:"default:7881" json:"liveKitTcpPort"` // TCP fallback 端口
	LiveKitUDPPort int    `gorm:"default:7882" json:"liveKitUdpPort"` // UDP 媒体端口
	VPNPort        int    `gorm:"default:41641" json:"vpnPort"`
	PublicAddress  string `gorm:"size:255" json:"publicAddress"`
	MaxUsers       int    `gorm:"default:50" json:"maxUsers"`
	DeployMode     string `gorm:"size:16;default:'native'" json:"deployMode"`
	// MaxFileSize 限制云文件上传的单个文件大小（单位 MB，0=不限）。
	// 由管理后台系统设置维护，cloudfs 上传 handler 据此校验。
	MaxFileSize int `gorm:"default:0" json:"maxFileSize"`
	// MaxStorageGB 限制软件最大磁盘占用（单位 GB，默认 10）。
	// 由管理后台存储设置维护，超限时触发自动清理。
	MaxStorageGB int       `gorm:"default:10" json:"maxStorageGB"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// AuditLog stores security audit events
type AuditLog struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	UserID    string    `gorm:"size:64;index" json:"userId"`
	Action    string    `gorm:"size:64;not null;index" json:"action"`
	Resource  string    `gorm:"size:64;index" json:"resource"`
	Details   string    `gorm:"type:text" json:"details"`
	IPAddress string    `gorm:"size:45" json:"ipAddress"`
	UserAgent string    `gorm:"size:512" json:"userAgent"`
	Success   bool      `gorm:"default:true" json:"success"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// ModuleRuntimeStatus tracks enabled/disabled modules
type ModuleRuntimeStatus struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	ModuleName        string     `gorm:"size:32;not null;uniqueIndex" json:"moduleName"`
	Enabled           bool       `gorm:"default:true" json:"enabled"`
	GracePeriodEndsAt *time.Time `json:"gracePeriodEndsAt"`
	UpdatedBy         string     `gorm:"size:64" json:"updatedBy"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	CPU               float64    `gorm:"default:0" json:"cpu"`
	Memory            float64    `gorm:"default:0" json:"memory"`
}

// AdminAlert stores one operational alert raised by the admin evaluator
// (internal/admin/alerts.go, DES-20261001-01 §12).
//
// Type is one of: disk | health | livekit | database | tts_worker.
// Severity is one of: warn | alert (alert = more severe, red banner in UI).
//
// Invariant: at most one open row (resolved_at IS NULL) per type — the
// evaluator reuses the open row to refresh last_seen/message instead of
// inserting duplicates. MutedUntil silences WS pushes for the same type while
// set; the row itself is still written (mute only suppresses notification).
type AdminAlert struct {
	ID           string     `gorm:"primaryKey;size:64" json:"id"`
	Type         string     `gorm:"size:32;not null;index" json:"type"`
	Severity     string     `gorm:"size:8;not null" json:"severity"`
	Message      string     `gorm:"type:text" json:"message"`
	FirstSeenAt  time.Time  `json:"firstSeenAt"`
	LastSeenAt   time.Time  `json:"lastSeenAt"`
	ResolvedAt   *time.Time `json:"resolvedAt"`
	MutedUntil   *time.Time `json:"mutedUntil"`
}

// Open reports whether the alert is still firing (not resolved).
func (a *AdminAlert) Open() bool { return a.ResolvedAt == nil }

// Muted reports whether the alert is currently silenced.
func (a *AdminAlert) Muted(now time.Time) bool {
	return a.MutedUntil != nil && now.Before(*a.MutedUntil)
}

// ScreenShareSession stores active screen share sessions.
// A session is bound to a voice channel (ChannelID) inside a Space.
// ShareType is one of: screen, window, application.
// Status is one of: active, paused, ended, force_stopped.
type ScreenShareSession struct {
	ID            string     `gorm:"primaryKey;size:64" json:"id"`
	UserID        string     `gorm:"size:64;not null;index" json:"userId"`
	SpaceID       string     `gorm:"size:64;not null;index" json:"spaceId"`
	ChannelID     string     `gorm:"column:channel_id;size:64;not null;index" json:"channelId"`
	ShareType     string     `gorm:"size:32;default:screen" json:"shareType"` // screen/window/application
	SourceID      string     `gorm:"size:128" json:"sourceId"`
	Resolution    string     `gorm:"size:16" json:"resolution"`
	FrameRate     int        `json:"frameRate"`
	MaxBitrate    int        `json:"maxBitrate"`
	ShareAudio    bool       `gorm:"default:false" json:"shareAudio"`
	SuppressVoice bool       `gorm:"default:false" json:"suppressVoice"`
	MaxViewers    int        `gorm:"default:50" json:"maxViewers"`
	ViewerCount   int        `gorm:"default:0" json:"viewerCount"`
	Status        string     `gorm:"size:32;default:active" json:"status"`    // active/paused/ended/force_stopped
	ViewerPolicy  string     `gorm:"size:32;default:all" json:"viewerPolicy"` // all/admin_only
	Active        bool       `gorm:"default:false;index" json:"active"`
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt"`
	EndedReason   string     `gorm:"size:32" json:"endedReason"`
}

// MessageAck stores message acknowledgment records
type MessageAck struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	MessageID string    `gorm:"size:64;not null;uniqueIndex:idx_msg_ack_user_msg" json:"messageId"`
	UserID    string    `gorm:"size:64;not null;uniqueIndex:idx_msg_ack_user_msg" json:"userId"`
	ChannelID string    `gorm:"size:64;not null" json:"channelId"`
	AckedAt   time.Time `json:"ackedAt"`
}

// SecurityAuditLog stores security audit events (180 days retention, not deletable)
type SecurityAuditLog struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	UserID       string    `gorm:"size:64;index" json:"userId"`
	Action       string    `gorm:"size:64;not null;index" json:"action"`
	ResourceType string    `gorm:"size:64;index" json:"resourceType"`
	ResourceID   string    `gorm:"size:64;index" json:"resourceId"`
	IPMasked     string    `gorm:"size:64" json:"ipMasked"`
	DetailsJSON  string    `gorm:"type:text" json:"detailsJson"`
	CreatedAt    time.Time `gorm:"index" json:"createdAt"`
}

// PasswordHistory stores user password hashes for history-based reuse prevention.
// H10: prevents users from reusing recent passwords when changing/resetting password.
type PasswordHistory struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	UserID       string    `gorm:"size:64;not null;index:idx_password_history_user_created" json:"userId"`
	PasswordHash string    `gorm:"size:255;not null" json:"-"`
	CreatedAt    time.Time `gorm:"index:idx_password_history_user_created" json:"createdAt"`
}

// TableName maps PasswordHistory to the singular password_history table
// created by baseline migration 000001_baseline.up.sql.
func (PasswordHistory) TableName() string {
	return "password_history"
}

// SecurityQuestion stores a user's security question and hashed answer for
// self-service password recovery. 2026-07-04: replaces email/SMS verification.
type SecurityQuestion struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	UserID     string    `gorm:"size:64;not null;index" json:"userId"`
	Question   string    `gorm:"size:255;not null" json:"question"`
	AnswerHash string    `gorm:"size:255;not null" json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// PasswordResetToken stores one-time tokens issued after security-question
// verification. 2026-07-04: method is "security_question".
type PasswordResetToken struct {
	ID        string     `gorm:"primaryKey;size:64" json:"id"`
	UserID    string     `gorm:"size:64;not null;index" json:"userId"`
	TokenHash string     `gorm:"size:255;not null" json:"-"`
	Method    string     `gorm:"size:32;not null" json:"method"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// VoiceRecording stores voice room recording records
type VoiceRecording struct {
	ID                 string     `gorm:"primaryKey;size:64" json:"id"`
	RoomID             string     `gorm:"size:64;not null;index" json:"roomId"`
	StartedBy          string     `gorm:"size:64;not null" json:"startedBy"`
	StartedAt          time.Time  `json:"startedAt"`
	StoppedAt          *time.Time `json:"stoppedAt"`
	FileURL            string     `gorm:"size:512" json:"fileUrl"`
	FileSizeMB         float64    `json:"fileSizeMb"`
	DurationSeconds    int        `json:"durationSeconds"`
	Status             string     `gorm:"size:16;default:'recording';index" json:"status"` // recording, processing, completed, failed
	IncludeScreenShare bool       `gorm:"default:false" json:"includeScreenShare"`
	EgressID           string     `gorm:"size:64" json:"egressId"` // LiveKit Egress ID for stopping recording
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// NeteaseAuth stores persistent Netease Cloud Music authentication state
type NeteaseAuth struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	UserID    string    `gorm:"size:64;not null;uniqueIndex" json:"userId"`
	Cookie    string    `gorm:"type:text" json:"-"` // encrypted cookie data
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Bot represents a music bot with an access token for API control.
// M22: Enables BotToken-based authorization for bot control operations,
// allowing external integrations to invoke bot APIs without user JWT.
// The bot is scoped to a Space and may optionally be bound to a voice room
// (OutputRoomID) as its audio output target.
type Bot struct {
	ID           string         `gorm:"primaryKey;size:64" json:"id"`
	SpaceID      string         `gorm:"size:64;not null;index" json:"spaceId"`
	OutputRoomID *string        `gorm:"size:64;index" json:"outputRoomId,omitempty"`
	Name         string         `gorm:"size:64" json:"name"`
	Token        string         `gorm:"size:128;not null;uniqueIndex" json:"-"`
	CreatedBy    string         `gorm:"size:64" json:"createdBy"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// CloudFileShare stores a public, tokenised share link for a single CloudFS
// file (DES-2026-0912-05 云文件分享链接安全设计).
//
// Security-relevant properties of this row:
//   - TokenHash is the SHA-256 of the token; the plaintext token is returned
//     exactly once (at creation) and is never persisted (§2/§4).
//   - PasswordHash is optional and stores a bcrypt hash at the project's
//     configured cost, never the password itself (§5).
//   - ExpiresAt is mandatory (§6) — there is no "permanent link" state.
//   - DownloadCount is only ever mutated by the atomic claim UPDATE in
//     cloudfs.Service.ClaimShareDownload (§6).
//   - SpaceID is denormalised from the file entry so that authorisation,
//     listing and the public download path never need a join, and so a share
//     keeps its space scope even if the file row is later soft-deleted.
type CloudFileShare struct {
	ID            string     `gorm:"primaryKey;size:64" json:"id"`
	FileID        string     `gorm:"size:64;not null;index:idx_cloud_file_shares_file_id" json:"fileId"`
	SpaceID       string     `gorm:"size:64;not null;index:idx_cloud_file_shares_space_id" json:"spaceId"`
	OwnerID       string     `gorm:"size:64;not null;index:idx_cloud_file_shares_owner_id" json:"ownerId"`
	TokenHash     string     `gorm:"size:64;not null;uniqueIndex:idx_cloud_file_shares_token_hash" json:"-"`
	TokenPrefix   string     `gorm:"size:16;not null;default:''" json:"tokenPrefix"`
	PasswordHash  string     `gorm:"size:255;not null;default:''" json:"-"`
	ExpiresAt     time.Time  `gorm:"not null;index:idx_cloud_file_shares_expires_at" json:"expiresAt"`
	MaxDownloads  int        `gorm:"not null;default:0" json:"maxDownloads"`  // 0 = 不限
	DownloadCount int        `gorm:"not null;default:0" json:"downloadCount"` // 原子递增
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	LastAccessAt  *time.Time `json:"lastAccessAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// RemoteAssistSession stores a remote assist control session between two users.
// T49: A requester asks a target for remote control of their computer; the
// target must explicitly authorize the request before any control events
// can be exchanged. Status transitions:
//
//	pending → authorized | rejected | timeout
//	authorized → ended
type RemoteAssistSession struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	RequesterID string    `gorm:"size:64;not null;index" json:"requesterId"`
	TargetID    string    `gorm:"size:64;not null;index" json:"targetId"`
	ChannelID   string    `gorm:"size:64;not null;index" json:"channelId"`
	Status      string    `gorm:"size:16;default:'pending';index" json:"status"` // pending/authorized/rejected/ended/timeout
	Permissions string    `gorm:"type:text" json:"permissions"`                  // JSON: {"mouse":bool,"keyboard":bool}
	ExpiresAt   time.Time `json:"expiresAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
