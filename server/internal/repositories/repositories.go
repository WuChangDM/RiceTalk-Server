// Package repositories defines Repository interfaces for data access.
// Design doc: 服务端详细开发文档 §5B Repository 模式设计.
//
// Business layers depend on these interfaces, not on *gorm.DB directly.
// GORM implementations live in internal/infra/gorm/, and cache wrappers
// live in internal/infra/cache/.
package repositories

import (
	"context"
	"time"

	"ridgericetalk/internal/model"
)

// UserRepository provides data access for User entities.
type UserRepository interface {
	GetByID(ctx context.Context, id string) (*model.User, error)
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	GetByUsername(ctx context.Context, username string) (*model.User, error)
	Create(ctx context.Context, user *model.User) error
	Update(ctx context.Context, user *model.User) error
	UpdateLastLogin(ctx context.Context, userID string) error
	Delete(ctx context.Context, id string) error
}

// ChannelRepository provides data access for Channel entities.
type ChannelRepository interface {
	GetByID(ctx context.Context, id string) (*model.Channel, error)
	GetBySpaceID(ctx context.Context, spaceID string) ([]model.Channel, error)
	Create(ctx context.Context, channel *model.Channel) error
	Update(ctx context.Context, channel *model.Channel) error
	Delete(ctx context.Context, id string) error
}

// MessageRepository provides data access for Message entities.
type MessageRepository interface {
	GetByID(ctx context.Context, id string) (*model.Message, error)
	GetByChannelID(ctx context.Context, channelID string, limit, offset int) ([]model.Message, error)
	Create(ctx context.Context, message *model.Message) error
	Update(ctx context.Context, message *model.Message) error
	Delete(ctx context.Context, id string) error
}

// DMRepository provides data access for DMChannel entities.
type DMRepository interface {
	GetByID(ctx context.Context, id string) (*model.DMChannel, error)
	GetByChannelID(ctx context.Context, channelID string) (*model.DMChannel, error)
	GetByUserID(ctx context.Context, userID string) ([]model.DMChannel, error)
	Create(ctx context.Context, dm *model.DMChannel) error
	Delete(ctx context.Context, id string) error
	CountByChannelAndUser(ctx context.Context, channelID, userID string) (int64, error)
}

// VoiceRoomRepository provides data access for VoiceRoom entities.
type VoiceRoomRepository interface {
	GetByID(ctx context.Context, id string) (*model.VoiceRoom, error)
	GetByBindChannelID(ctx context.Context, channelID string) (*model.VoiceRoom, error)
	Create(ctx context.Context, room *model.VoiceRoom) error
	Update(ctx context.Context, room *model.VoiceRoom) error
}

// VoiceParticipantRepository provides data access for VoiceParticipant entities.
type VoiceParticipantRepository interface {
	GetActiveByRoomID(ctx context.Context, roomID string) ([]model.VoiceParticipant, error)
	Create(ctx context.Context, p *model.VoiceParticipant) error
	Update(ctx context.Context, p *model.VoiceParticipant) error
}

// AdminConfigRepository provides data access for AdminConfig entities.
type AdminConfigRepository interface {
	Get(ctx context.Context) (*model.AdminConfig, error)
	Create(ctx context.Context, cfg *model.AdminConfig) error
	Update(ctx context.Context, cfg *model.AdminConfig) error
}

// SecurityAuditLogRepository provides data access for SecurityAuditLog entities.
type SecurityAuditLogRepository interface {
	Create(ctx context.Context, log *model.SecurityAuditLog) error
	List(ctx context.Context, page, pageSize int, eventType, userID string) ([]model.SecurityAuditLog, int64, error)
}

// ScreenShareSessionRepository provides data access for ScreenShareSession entities.
type ScreenShareSessionRepository interface {
	GetByID(ctx context.Context, id string) (*model.ScreenShareSession, error)
	GetActiveByChannelID(ctx context.Context, channelID string) (*model.ScreenShareSession, error)
	GetActiveBySpaceID(ctx context.Context, spaceID string) ([]model.ScreenShareSession, error)
	Create(ctx context.Context, session *model.ScreenShareSession) error
	Update(ctx context.Context, session *model.ScreenShareSession) error
	CountActiveByChannelID(ctx context.Context, channelID string) (int64, error)
}

// BotRepository provides data access for Bot entities.
type BotRepository interface {
	GetByID(ctx context.Context, id string) (*model.Bot, error)
	Create(ctx context.Context, bot *model.Bot) error
	Delete(ctx context.Context, id string) error
	ListBySpaceID(ctx context.Context, spaceID string) ([]model.Bot, error)
}

// BotPlayQueueRepository provides data access for BotPlayQueue entities.
type BotPlayQueueRepository interface {
	ListByBotID(ctx context.Context, botID string) ([]model.BotPlayQueue, error)
	Create(ctx context.Context, q *model.BotPlayQueue) error
	Delete(ctx context.Context, id string) error
}

// BotUploadAudioRepository provides data access for BotUploadAudio entities.
type BotUploadAudioRepository interface {
	GetByID(ctx context.Context, id string) (*model.BotUploadAudio, error)
	Create(ctx context.Context, a *model.BotUploadAudio) error
	ListByUserID(ctx context.Context, userID string) ([]model.BotUploadAudio, error)
	// ListAll 返回全部上传记录，用于空间内所有用户共享音乐列表。
	// uploaderUsername 由调用方通过 users 表关联查询补全。
	ListAll(ctx context.Context) ([]model.BotUploadAudio, error)
	Delete(ctx context.Context, id string) error
}

// SharedFolderRepository provides data access for SharedFolder entities.
type SharedFolderRepository interface {
	GetBySpaceID(ctx context.Context, spaceID string) ([]model.SharedFolder, error)
	GetByPath(ctx context.Context, spaceID, path string) (*model.SharedFolder, error)
	GetByParentID(ctx context.Context, parentID string) ([]model.SharedFolder, error)
	Create(ctx context.Context, folder *model.SharedFolder) error
	Delete(ctx context.Context, id string) error
}

// SharedFileEntryRepository provides data access for SharedFileEntry entities.
type SharedFileEntryRepository interface {
	GetBySpaceID(ctx context.Context, spaceID string) ([]model.SharedFileEntry, error)
	GetByFolderID(ctx context.Context, folderID string) ([]model.SharedFileEntry, error)
	GetByFilePath(ctx context.Context, spaceID, filePath string) (*model.SharedFileEntry, error)
	Create(ctx context.Context, entry *model.SharedFileEntry) error
	Delete(ctx context.Context, id string) error
}

// OGCacheRepository provides data access for OGCache entities.
type OGCacheRepository interface {
	GetByURL(ctx context.Context, url string) (*model.OGCache, error)
	Create(ctx context.Context, cache *model.OGCache) error
	DeleteByURL(ctx context.Context, url string) error
}

// WhiteboardRepository provides data access for Whiteboard and WhiteboardStroke entities.
type WhiteboardRepository interface {
	ListBySpaceID(ctx context.Context, spaceID string, includeArchived bool) ([]model.Whiteboard, error)
	GetByID(ctx context.Context, id string) (*model.Whiteboard, error)
	Create(ctx context.Context, wb *model.Whiteboard) error
	Update(ctx context.Context, wb *model.Whiteboard) error
	Delete(ctx context.Context, id string) error
	ListStrokesByWhiteboardID(ctx context.Context, whiteboardID string) ([]model.WhiteboardStroke, error)
	CreateStroke(ctx context.Context, stroke *model.WhiteboardStroke) error
	// DeleteStrokeByID 删除指定白板下的单条笔迹（S-3 选中删除），
	// 返回影响行数：0 表示笔迹不存在（调用方映射 404）。
	DeleteStrokeByID(ctx context.Context, whiteboardID, strokeID string) (int64, error)
	DeleteStrokesByWhiteboardID(ctx context.Context, whiteboardID string) error
	CountStrokesByWhiteboardID(ctx context.Context, whiteboardID string) (int64, error)
	UpdateStats(ctx context.Context, whiteboardID string, strokeCount int64, lastDrawnAt time.Time) error
	UpdateThumbnail(ctx context.Context, whiteboardID string, thumbnailURL string) error
}
