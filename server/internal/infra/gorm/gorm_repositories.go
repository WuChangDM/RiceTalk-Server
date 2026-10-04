// Package gormrepo provides GORM implementations of Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.2.
package gormrepo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// Ensure GormUserRepository implements UserRepository.
var _ repositories.UserRepository = (*GormUserRepository)(nil)

// GormUserRepository implements repositories.UserRepository using GORM.
type GormUserRepository struct {
	db *gorm.DB
}

// NewGormUserRepository creates a new GormUserRepository.
func NewGormUserRepository(db *gorm.DB) *GormUserRepository {
	return &GormUserRepository{db: db}
}

func (r *GormUserRepository) GetByID(ctx context.Context, id string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *GormUserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *GormUserRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *GormUserRepository) Create(ctx context.Context, user *model.User) error {
	return r.db.WithContext(ctx).Create(user).Error
}

func (r *GormUserRepository) Update(ctx context.Context, user *model.User) error {
	return r.db.WithContext(ctx).Save(user).Error
}

func (r *GormUserRepository) UpdateLastLogin(ctx context.Context, userID string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Update("last_login_at", now).Error
}

func (r *GormUserRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.User{}, "id = ?", id).Error
}

// Ensure GormChannelRepository implements ChannelRepository.
var _ repositories.ChannelRepository = (*GormChannelRepository)(nil)

// GormChannelRepository implements repositories.ChannelRepository using GORM.
type GormChannelRepository struct {
	db *gorm.DB
}

// NewGormChannelRepository creates a new GormChannelRepository.
func NewGormChannelRepository(db *gorm.DB) *GormChannelRepository {
	return &GormChannelRepository{db: db}
}

func (r *GormChannelRepository) GetByID(ctx context.Context, id string) (*model.Channel, error) {
	var ch model.Channel
	if err := r.db.WithContext(ctx).First(&ch, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

func (r *GormChannelRepository) GetBySpaceID(ctx context.Context, spaceID string) ([]model.Channel, error) {
	var channels []model.Channel
	if err := r.db.WithContext(ctx).Where("space_id = ?", spaceID).Order("position ASC, created_at ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

func (r *GormChannelRepository) Create(ctx context.Context, channel *model.Channel) error {
	return r.db.WithContext(ctx).Create(channel).Error
}

func (r *GormChannelRepository) Update(ctx context.Context, channel *model.Channel) error {
	return r.db.WithContext(ctx).Save(channel).Error
}

func (r *GormChannelRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.Channel{}, "id = ?", id).Error
}

// Ensure GormMessageRepository implements MessageRepository.
var _ repositories.MessageRepository = (*GormMessageRepository)(nil)

// GormMessageRepository implements repositories.MessageRepository using GORM.
type GormMessageRepository struct {
	db *gorm.DB
}

// NewGormMessageRepository creates a new GormMessageRepository.
func NewGormMessageRepository(db *gorm.DB) *GormMessageRepository {
	return &GormMessageRepository{db: db}
}

func (r *GormMessageRepository) GetByID(ctx context.Context, id string) (*model.Message, error) {
	var msg model.Message
	if err := r.db.WithContext(ctx).First(&msg, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &msg, nil
}

func (r *GormMessageRepository) GetByChannelID(ctx context.Context, channelID string, limit, offset int) ([]model.Message, error) {
	var messages []model.Message
	q := r.db.WithContext(ctx).Where("channel_id = ?", channelID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&messages).Error; err != nil {
		return nil, err
	}
	return messages, nil
}

func (r *GormMessageRepository) Create(ctx context.Context, message *model.Message) error {
	return r.db.WithContext(ctx).Create(message).Error
}

func (r *GormMessageRepository) Update(ctx context.Context, message *model.Message) error {
	return r.db.WithContext(ctx).Save(message).Error
}

func (r *GormMessageRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.Message{}, "id = ?", id).Error
}

// Ensure GormDMRepository implements DMRepository.
var _ repositories.DMRepository = (*GormDMRepository)(nil)

// GormDMRepository implements repositories.DMRepository using GORM.
type GormDMRepository struct {
	db *gorm.DB
}

// NewGormDMRepository creates a new GormDMRepository.
func NewGormDMRepository(db *gorm.DB) *GormDMRepository {
	return &GormDMRepository{db: db}
}

func (r *GormDMRepository) GetByID(ctx context.Context, id string) (*model.DMChannel, error) {
	var dm model.DMChannel
	if err := r.db.WithContext(ctx).First(&dm, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &dm, nil
}

func (r *GormDMRepository) GetByChannelID(ctx context.Context, channelID string) (*model.DMChannel, error) {
	var dm model.DMChannel
	if err := r.db.WithContext(ctx).Where("channel_id = ?", channelID).First(&dm).Error; err != nil {
		return nil, err
	}
	return &dm, nil
}

func (r *GormDMRepository) GetByUserID(ctx context.Context, userID string) ([]model.DMChannel, error) {
	var dmChannels []model.DMChannel
	if err := r.db.WithContext(ctx).Where("user_aid = ? OR user_bid = ?", userID, userID).Find(&dmChannels).Error; err != nil {
		return nil, err
	}
	return dmChannels, nil
}

func (r *GormDMRepository) Create(ctx context.Context, dm *model.DMChannel) error {
	return r.db.WithContext(ctx).Create(dm).Error
}

func (r *GormDMRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.DMChannel{}, "id = ?", id).Error
}

func (r *GormDMRepository) CountByChannelAndUser(ctx context.Context, channelID, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.DMChannel{}).
		Where("channel_id = ? AND (user_aid = ? OR user_bid = ?)", channelID, userID, userID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// Ensure GormVoiceRoomRepository implements VoiceRoomRepository.
var _ repositories.VoiceRoomRepository = (*GormVoiceRoomRepository)(nil)

// GormVoiceRoomRepository implements repositories.VoiceRoomRepository using GORM.
type GormVoiceRoomRepository struct {
	db *gorm.DB
}

// NewGormVoiceRoomRepository creates a new GormVoiceRoomRepository.
func NewGormVoiceRoomRepository(db *gorm.DB) *GormVoiceRoomRepository {
	return &GormVoiceRoomRepository{db: db}
}

func (r *GormVoiceRoomRepository) GetByID(ctx context.Context, id string) (*model.VoiceRoom, error) {
	var room model.VoiceRoom
	if err := r.db.WithContext(ctx).First(&room, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &room, nil
}

func (r *GormVoiceRoomRepository) GetByBindChannelID(ctx context.Context, channelID string) (*model.VoiceRoom, error) {
	var room model.VoiceRoom
	if err := r.db.WithContext(ctx).First(&room, "bind_channel_id = ?", channelID).Error; err != nil {
		return nil, err
	}
	return &room, nil
}

func (r *GormVoiceRoomRepository) Create(ctx context.Context, room *model.VoiceRoom) error {
	return r.db.WithContext(ctx).Create(room).Error
}

func (r *GormVoiceRoomRepository) Update(ctx context.Context, room *model.VoiceRoom) error {
	return r.db.WithContext(ctx).Save(room).Error
}

// Ensure GormVoiceParticipantRepository implements VoiceParticipantRepository.
var _ repositories.VoiceParticipantRepository = (*GormVoiceParticipantRepository)(nil)

// GormVoiceParticipantRepository implements repositories.VoiceParticipantRepository using GORM.
type GormVoiceParticipantRepository struct {
	db *gorm.DB
}

// NewGormVoiceParticipantRepository creates a new GormVoiceParticipantRepository.
func NewGormVoiceParticipantRepository(db *gorm.DB) *GormVoiceParticipantRepository {
	return &GormVoiceParticipantRepository{db: db}
}

func (r *GormVoiceParticipantRepository) GetActiveByRoomID(ctx context.Context, roomID string) ([]model.VoiceParticipant, error) {
	var participants []model.VoiceParticipant
	if err := r.db.WithContext(ctx).Where("room_id = ? AND left_at IS NULL", roomID).Find(&participants).Error; err != nil {
		return nil, err
	}
	return participants, nil
}

func (r *GormVoiceParticipantRepository) Create(ctx context.Context, p *model.VoiceParticipant) error {
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *GormVoiceParticipantRepository) Update(ctx context.Context, p *model.VoiceParticipant) error {
	return r.db.WithContext(ctx).Save(p).Error
}

// Ensure GormAdminConfigRepository implements AdminConfigRepository.
var _ repositories.AdminConfigRepository = (*GormAdminConfigRepository)(nil)

// GormAdminConfigRepository implements repositories.AdminConfigRepository using GORM.
type GormAdminConfigRepository struct {
	db *gorm.DB
}

// NewGormAdminConfigRepository creates a new GormAdminConfigRepository.
func NewGormAdminConfigRepository(db *gorm.DB) *GormAdminConfigRepository {
	return &GormAdminConfigRepository{db: db}
}

func (r *GormAdminConfigRepository) Get(ctx context.Context) (*model.AdminConfig, error) {
	var cfg model.AdminConfig
	if err := r.db.WithContext(ctx).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (r *GormAdminConfigRepository) Create(ctx context.Context, cfg *model.AdminConfig) error {
	return r.db.WithContext(ctx).Create(cfg).Error
}

func (r *GormAdminConfigRepository) Update(ctx context.Context, cfg *model.AdminConfig) error {
	return r.db.WithContext(ctx).Save(cfg).Error
}

// Ensure GormSecurityAuditLogRepository implements SecurityAuditLogRepository.
var _ repositories.SecurityAuditLogRepository = (*GormSecurityAuditLogRepository)(nil)

// GormSecurityAuditLogRepository implements repositories.SecurityAuditLogRepository using GORM.
type GormSecurityAuditLogRepository struct {
	db *gorm.DB
}

// NewGormSecurityAuditLogRepository creates a new GormSecurityAuditLogRepository.
func NewGormSecurityAuditLogRepository(db *gorm.DB) *GormSecurityAuditLogRepository {
	return &GormSecurityAuditLogRepository{db: db}
}

func (r *GormSecurityAuditLogRepository) Create(ctx context.Context, log *model.SecurityAuditLog) error {
	return r.db.WithContext(ctx).Create(log).Error
}

func (r *GormSecurityAuditLogRepository) List(ctx context.Context, page, pageSize int, eventType, userID string) ([]model.SecurityAuditLog, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.SecurityAuditLog{})
	if eventType != "" {
		q = q.Where("action = ?", eventType)
	}
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var logs []model.SecurityAuditLog
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

// Ensure GormScreenShareSessionRepository implements ScreenShareSessionRepository.
var _ repositories.ScreenShareSessionRepository = (*GormScreenShareSessionRepository)(nil)

// GormScreenShareSessionRepository implements repositories.ScreenShareSessionRepository using GORM.
type GormScreenShareSessionRepository struct {
	db *gorm.DB
}

// NewGormScreenShareSessionRepository creates a new GormScreenShareSessionRepository.
func NewGormScreenShareSessionRepository(db *gorm.DB) *GormScreenShareSessionRepository {
	return &GormScreenShareSessionRepository{db: db}
}

func (r *GormScreenShareSessionRepository) GetByID(ctx context.Context, id string) (*model.ScreenShareSession, error) {
	var session model.ScreenShareSession
	if err := r.db.WithContext(ctx).First(&session, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *GormScreenShareSessionRepository) GetActiveByChannelID(ctx context.Context, channelID string) (*model.ScreenShareSession, error) {
	var session model.ScreenShareSession
	if err := r.db.WithContext(ctx).Where("channel_id = ? AND active = ?", channelID, true).
		Order("started_at DESC").First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *GormScreenShareSessionRepository) GetActiveBySpaceID(ctx context.Context, spaceID string) ([]model.ScreenShareSession, error) {
	var sessions []model.ScreenShareSession
	if err := r.db.WithContext(ctx).Where("space_id = ? AND active = ?", spaceID, true).
		Order("started_at DESC").Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

func (r *GormScreenShareSessionRepository) Create(ctx context.Context, session *model.ScreenShareSession) error {
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *GormScreenShareSessionRepository) Update(ctx context.Context, session *model.ScreenShareSession) error {
	return r.db.WithContext(ctx).Save(session).Error
}

func (r *GormScreenShareSessionRepository) CountActiveByChannelID(ctx context.Context, channelID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.ScreenShareSession{}).
		Where("channel_id = ? AND active = ?", channelID, true).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// Ensure GormBotRepository implements BotRepository.
var _ repositories.BotRepository = (*GormBotRepository)(nil)

// GormBotRepository implements repositories.BotRepository using GORM.
type GormBotRepository struct {
	db *gorm.DB
}

// NewGormBotRepository creates a new GormBotRepository.
func NewGormBotRepository(db *gorm.DB) *GormBotRepository {
	return &GormBotRepository{db: db}
}

func (r *GormBotRepository) GetByID(ctx context.Context, id string) (*model.Bot, error) {
	var bot model.Bot
	if err := r.db.WithContext(ctx).First(&bot, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &bot, nil
}

func (r *GormBotRepository) Create(ctx context.Context, bot *model.Bot) error {
	return r.db.WithContext(ctx).Create(bot).Error
}

func (r *GormBotRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.Bot{}, "id = ?", id).Error
}

func (r *GormBotRepository) ListBySpaceID(ctx context.Context, spaceID string) ([]model.Bot, error) {
	var bots []model.Bot
	if err := r.db.WithContext(ctx).Where("space_id = ?", spaceID).Order("created_at DESC").Find(&bots).Error; err != nil {
		return nil, err
	}
	return bots, nil
}

// Ensure GormBotPlayQueueRepository implements BotPlayQueueRepository.
var _ repositories.BotPlayQueueRepository = (*GormBotPlayQueueRepository)(nil)

// GormBotPlayQueueRepository implements repositories.BotPlayQueueRepository using GORM.
type GormBotPlayQueueRepository struct {
	db *gorm.DB
}

// NewGormBotPlayQueueRepository creates a new GormBotPlayQueueRepository.
func NewGormBotPlayQueueRepository(db *gorm.DB) *GormBotPlayQueueRepository {
	return &GormBotPlayQueueRepository{db: db}
}

func (r *GormBotPlayQueueRepository) ListByBotID(ctx context.Context, botID string) ([]model.BotPlayQueue, error) {
	var queue []model.BotPlayQueue
	if err := r.db.WithContext(ctx).Where("bot_id = ?", botID).Order("position ASC").Find(&queue).Error; err != nil {
		return nil, err
	}
	return queue, nil
}

func (r *GormBotPlayQueueRepository) Create(ctx context.Context, q *model.BotPlayQueue) error {
	return r.db.WithContext(ctx).Create(q).Error
}

func (r *GormBotPlayQueueRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.BotPlayQueue{}, "id = ?", id).Error
}

// Ensure GormBotUploadAudioRepository implements BotUploadAudioRepository.
var _ repositories.BotUploadAudioRepository = (*GormBotUploadAudioRepository)(nil)

// GormBotUploadAudioRepository implements repositories.BotUploadAudioRepository using GORM.
type GormBotUploadAudioRepository struct {
	db *gorm.DB
}

// NewGormBotUploadAudioRepository creates a new GormBotUploadAudioRepository.
func NewGormBotUploadAudioRepository(db *gorm.DB) *GormBotUploadAudioRepository {
	return &GormBotUploadAudioRepository{db: db}
}

func (r *GormBotUploadAudioRepository) GetByID(ctx context.Context, id string) (*model.BotUploadAudio, error) {
	var a model.BotUploadAudio
	if err := r.db.WithContext(ctx).First(&a, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *GormBotUploadAudioRepository) Create(ctx context.Context, a *model.BotUploadAudio) error {
	return r.db.WithContext(ctx).Create(a).Error
}

func (r *GormBotUploadAudioRepository) ListByUserID(ctx context.Context, userID string) ([]model.BotUploadAudio, error) {
	var uploads []model.BotUploadAudio
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&uploads).Error; err != nil {
		return nil, err
	}
	return uploads, nil
}

// ListAll 返回全部上传记录，供空间内所有用户共享音乐列表使用。
// 不按 user_id 过滤，调用方需自行关联 users 表补全上传者信息。
func (r *GormBotUploadAudioRepository) ListAll(ctx context.Context) ([]model.BotUploadAudio, error) {
	var uploads []model.BotUploadAudio
	if err := r.db.WithContext(ctx).Order("created_at DESC").Find(&uploads).Error; err != nil {
		return nil, err
	}
	return uploads, nil
}

func (r *GormBotUploadAudioRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.BotUploadAudio{}, "id = ?", id).Error
}

// Ensure GormSharedFolderRepository implements SharedFolderRepository.
var _ repositories.SharedFolderRepository = (*GormSharedFolderRepository)(nil)

// GormSharedFolderRepository implements repositories.SharedFolderRepository using GORM.
type GormSharedFolderRepository struct {
	db *gorm.DB
}

// NewGormSharedFolderRepository creates a new GormSharedFolderRepository.
func NewGormSharedFolderRepository(db *gorm.DB) *GormSharedFolderRepository {
	return &GormSharedFolderRepository{db: db}
}

func (r *GormSharedFolderRepository) GetBySpaceID(ctx context.Context, spaceID string) ([]model.SharedFolder, error) {
	var folders []model.SharedFolder
	if err := r.db.WithContext(ctx).Where("space_id = ?", spaceID).Find(&folders).Error; err != nil {
		return nil, err
	}
	return folders, nil
}

func (r *GormSharedFolderRepository) GetByPath(ctx context.Context, spaceID, path string) (*model.SharedFolder, error) {
	var folder model.SharedFolder
	if err := r.db.WithContext(ctx).Where("space_id = ? AND path = ?", spaceID, path).First(&folder).Error; err != nil {
		return nil, err
	}
	return &folder, nil
}

func (r *GormSharedFolderRepository) GetByParentID(ctx context.Context, parentID string) ([]model.SharedFolder, error) {
	var folders []model.SharedFolder
	if err := r.db.WithContext(ctx).Where("parent_id = ?", parentID).Find(&folders).Error; err != nil {
		return nil, err
	}
	return folders, nil
}

func (r *GormSharedFolderRepository) Create(ctx context.Context, folder *model.SharedFolder) error {
	return r.db.WithContext(ctx).Create(folder).Error
}

func (r *GormSharedFolderRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.SharedFolder{}, "id = ?", id).Error
}

// Ensure GormSharedFileEntryRepository implements SharedFileEntryRepository.
var _ repositories.SharedFileEntryRepository = (*GormSharedFileEntryRepository)(nil)

// GormSharedFileEntryRepository implements repositories.SharedFileEntryRepository using GORM.
type GormSharedFileEntryRepository struct {
	db *gorm.DB
}

// NewGormSharedFileEntryRepository creates a new GormSharedFileEntryRepository.
func NewGormSharedFileEntryRepository(db *gorm.DB) *GormSharedFileEntryRepository {
	return &GormSharedFileEntryRepository{db: db}
}

func (r *GormSharedFileEntryRepository) GetBySpaceID(ctx context.Context, spaceID string) ([]model.SharedFileEntry, error) {
	var entries []model.SharedFileEntry
	if err := r.db.WithContext(ctx).Where("space_id = ?", spaceID).Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *GormSharedFileEntryRepository) GetByFolderID(ctx context.Context, folderID string) ([]model.SharedFileEntry, error) {
	var entries []model.SharedFileEntry
	if err := r.db.WithContext(ctx).Where("folder_id = ?", folderID).Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *GormSharedFileEntryRepository) GetByFilePath(ctx context.Context, spaceID, filePath string) (*model.SharedFileEntry, error) {
	var entry model.SharedFileEntry
	if err := r.db.WithContext(ctx).Where("space_id = ? AND file_path = ?", spaceID, filePath).First(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

func (r *GormSharedFileEntryRepository) Create(ctx context.Context, entry *model.SharedFileEntry) error {
	return r.db.WithContext(ctx).Create(entry).Error
}

func (r *GormSharedFileEntryRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.SharedFileEntry{}, "id = ?", id).Error
}

// Ensure GormOGCacheRepository implements OGCacheRepository.
var _ repositories.OGCacheRepository = (*GormOGCacheRepository)(nil)

// GormOGCacheRepository implements repositories.OGCacheRepository using GORM.
type GormOGCacheRepository struct {
	db *gorm.DB
}

// NewGormOGCacheRepository creates a new GormOGCacheRepository.
func NewGormOGCacheRepository(db *gorm.DB) *GormOGCacheRepository {
	return &GormOGCacheRepository{db: db}
}

func (r *GormOGCacheRepository) GetByURL(ctx context.Context, url string) (*model.OGCache, error) {
	var cached model.OGCache
	if err := r.db.WithContext(ctx).Where("url = ? AND expires_at > ?", url, time.Now()).First(&cached).Error; err != nil {
		return nil, err
	}
	return &cached, nil
}

func (r *GormOGCacheRepository) Create(ctx context.Context, cache *model.OGCache) error {
	return r.db.WithContext(ctx).Create(cache).Error
}

func (r *GormOGCacheRepository) DeleteByURL(ctx context.Context, url string) error {
	return r.db.WithContext(ctx).Where("url = ?", url).Delete(&model.OGCache{}).Error
}

// Ensure GormWhiteboardRepository implements WhiteboardRepository.
var _ repositories.WhiteboardRepository = (*GormWhiteboardRepository)(nil)

// GormWhiteboardRepository implements repositories.WhiteboardRepository using GORM.
type GormWhiteboardRepository struct {
	db *gorm.DB
}

// NewGormWhiteboardRepository creates a new GormWhiteboardRepository.
func NewGormWhiteboardRepository(db *gorm.DB) *GormWhiteboardRepository {
	return &GormWhiteboardRepository{db: db}
}

func (r *GormWhiteboardRepository) ListBySpaceID(ctx context.Context, spaceID string, includeArchived bool) ([]model.Whiteboard, error) {
	var list []model.Whiteboard
	q := r.db.WithContext(ctx).Where("space_id = ?", spaceID).Order("created_at DESC")
	if !includeArchived {
		q = q.Where("archived = ?", false)
	}
	if err := q.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *GormWhiteboardRepository) GetByID(ctx context.Context, id string) (*model.Whiteboard, error) {
	var wb model.Whiteboard
	if err := r.db.WithContext(ctx).First(&wb, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &wb, nil
}

func (r *GormWhiteboardRepository) Create(ctx context.Context, wb *model.Whiteboard) error {
	return r.db.WithContext(ctx).Create(wb).Error
}

func (r *GormWhiteboardRepository) Update(ctx context.Context, wb *model.Whiteboard) error {
	return r.db.WithContext(ctx).Save(wb).Error
}

func (r *GormWhiteboardRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.Whiteboard{}, "id = ?", id).Error
}

func (r *GormWhiteboardRepository) ListStrokesByWhiteboardID(ctx context.Context, whiteboardID string) ([]model.WhiteboardStroke, error) {
	var strokes []model.WhiteboardStroke
	if err := r.db.WithContext(ctx).Where("whiteboard_id = ?", whiteboardID).Order("created_at ASC").Find(&strokes).Error; err != nil {
		return nil, err
	}
	return strokes, nil
}

func (r *GormWhiteboardRepository) CreateStroke(ctx context.Context, stroke *model.WhiteboardStroke) error {
	return r.db.WithContext(ctx).Create(stroke).Error
}

// DeleteStrokeByID 删除指定白板下的单条笔迹（S-3 选中删除）。
// 白板 ID 一并进 WHERE：既防跨白板误删，也用影响行数区分「不存在」（0 行 → 404）。
func (r *GormWhiteboardRepository) DeleteStrokeByID(ctx context.Context, whiteboardID, strokeID string) (int64, error) {
	res := r.db.WithContext(ctx).Where("whiteboard_id = ? AND id = ?", whiteboardID, strokeID).Delete(&model.WhiteboardStroke{})
	return res.RowsAffected, res.Error
}

func (r *GormWhiteboardRepository) DeleteStrokesByWhiteboardID(ctx context.Context, whiteboardID string) error {
	return r.db.WithContext(ctx).Where("whiteboard_id = ?", whiteboardID).Delete(&model.WhiteboardStroke{}).Error
}

func (r *GormWhiteboardRepository) CountStrokesByWhiteboardID(ctx context.Context, whiteboardID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.WhiteboardStroke{}).Where("whiteboard_id = ?", whiteboardID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *GormWhiteboardRepository) UpdateStats(ctx context.Context, whiteboardID string, strokeCount int64, lastDrawnAt time.Time) error {
	return r.db.WithContext(ctx).Model(&model.Whiteboard{}).Where("id = ?", whiteboardID).Updates(map[string]interface{}{
		"stroke_count":  strokeCount,
		"last_drawn_at": lastDrawnAt,
		"updated_at":    time.Now(),
	}).Error
}

func (r *GormWhiteboardRepository) UpdateThumbnail(ctx context.Context, whiteboardID string, thumbnailURL string) error {
	return r.db.WithContext(ctx).Model(&model.Whiteboard{}).Where("id = ?", whiteboardID).Updates(map[string]interface{}{
		"thumbnail_url": thumbnailURL,
		"updated_at":    time.Now(),
	}).Error
}
