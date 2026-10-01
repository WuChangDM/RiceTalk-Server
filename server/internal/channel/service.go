package channel

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/core/validator"
	"ridgericetalk/internal/database"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// Service handles channel business logic.
//
// C6 migration: simple Channel reads/creates are delegated to
// ChannelRepository (which may be wrapped by a cache layer). Transactional
// deletes, space operations, and partial updates still use *gorm.DB directly
// per the design doc's risk-control guidance (§5B / 批次9h 设计方案 §三).
type Service struct {
	db          *gorm.DB
	channelRepo repositories.ChannelRepository
}

// channelCacheInvalidator is an optional interface implemented by CachedChannelRepository.
type channelCacheInvalidator interface {
	InvalidateChannel(channelID, spaceID string)
}

func (s *Service) invalidateChannelCache(channelID, spaceID string) {
	if inv, ok := s.channelRepo.(channelCacheInvalidator); ok {
		inv.InvalidateChannel(channelID, spaceID)
	}
}

// NewService creates a new channel service with a default GORM-backed
// ChannelRepository (no cache). Use NewServiceWithRepos to inject a cached or
// mock repository.
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:          db,
		channelRepo: gormrepo.NewGormChannelRepository(db),
	}
}

// NewServiceWithRepos creates a new channel service with the given
// ChannelRepository. Pass a cache-wrapped repository
// (cacheinfra.NewCachedChannelRepository) to enable TTL caching per design
// doc §5B.3.
func NewServiceWithRepos(db *gorm.DB, channelRepo repositories.ChannelRepository) *Service {
	if channelRepo == nil {
		channelRepo = gormrepo.NewGormChannelRepository(db)
	}
	return &Service{db: db, channelRepo: channelRepo}
}

// GetSpace retrieves the default space. When multiple spaces exist, the
// default is deterministic: the earliest-created one (the originally
// bootstrapped space), not whichever row the table scan happens to hit first.
func (s *Service) GetSpace() (*model.Space, error) {
	var space model.Space
	if err := s.db.Order("created_at ASC, id ASC").First(&space).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return &space, nil
}

// UpdateSpace updates the space info
func (s *Service) UpdateSpace(updates map[string]interface{}) error {
	var space model.Space
	if err := s.db.Order("created_at ASC, id ASC").First(&space).Error; err != nil {
		return database.ClassifyError(err)
	}
	if err := s.db.Model(&space).Updates(updates).Error; err != nil {
		return database.ClassifyError(err)
	}
	return nil
}

// channelScopeSpaceIDs returns the IDs of the spaces whose channels the user
// may list: every space the user is a member of, falling back to the default
// (earliest-created) space when the user has no membership yet.
func (s *Service) channelScopeSpaceIDs(userID string) []string {
	var spaceIDs []string
	if userID != "" {
		s.db.Model(&model.Membership{}).Where("user_id = ?", userID).Pluck("space_id", &spaceIDs)
	}
	if len(spaceIDs) == 0 {
		if space, err := s.GetSpace(); err == nil {
			spaceIDs = []string{space.ID}
		}
	}
	return spaceIDs
}

// SortGroupMaxLen 是频道分组名（Channel.SortGroup）的最大 rune 数，与模型
// `gorm:"size:32"` 对齐（DES-20261001-01 §2.2）。空串表示「未分组」。
const SortGroupMaxLen = 32

// NormalizeSortGroup 规范化并校验频道分组名（A1-S1）：
// 去除首尾空白；空串视为「未分组」合法值；rune 数超过 SortGroupMaxLen 返回 400。
// CreateChannel（service 层）与 channel/admin 两个 UpdateChannel handler 共用，
// 保证三条写入路径的校验口径完全一致。
func NormalizeSortGroup(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if utf8.RuneCountInString(trimmed) > SortGroupMaxLen {
		return "", errors.New(errors.SYSTEM_BAD_REQUEST, "分组名过长，最多 32 个字符")
	}
	return trimmed, nil
}

// GetChannels retrieves channels visible to the given user in their own
// space(s). Channels from spaces the user is not a member of are never
// returned: joining a voice channel or posting in a text channel is guarded
// by per-space membership checks, so listing foreign channels would only
// produce 403s later. Users without any membership (e.g. OWNER/ADMIN before
// joining) see the default space's channels, keeping bootstrap flows working.
// OWNER/ADMIN see all channels in scope; MEMBER sees only channels whose
// visibility policy allows them (public, or role-specific with a matching
// CanView grant).
func (s *Service) GetChannels(userID, userRole string) ([]model.Channel, error) {
	var channels []model.Channel
	if err := s.db.Where("space_id IN ?", s.channelScopeSpaceIDs(userID)).
		Where("LOWER(type) <> ?", "dm").
		// A1-S1 分组排序：未分组段（sort_group = ''）永远排最前，其后按组名字典序，
		// 组内按 position、created_at 稳定排序。SQLite 与 PostgreSQL 均支持
		// 布尔表达式的 DESC（1=未分组在前）。
		Order("(sort_group = '') DESC, sort_group ASC, position ASC, created_at ASC").Find(&channels).Error; err != nil {
		return nil, errors.ErrInternal
	}

	// OWNER/ADMIN see everything
	if userRole == "OWNER" || userRole == "ADMIN" {
		return channels, nil
	}

	// MEMBER: filter by visibility policy
	visible := make([]model.Channel, 0, len(channels))
	// Preload role-specific permissions for this user's role in one query
	allowedChannelIDs := make(map[string]bool)
	var perms []model.ChannelRolePermission
	s.db.Where("role = ?", userRole).Find(&perms)
	for _, p := range perms {
		if p.CanView {
			allowedChannelIDs[p.ChannelID] = true
		}
	}

	for _, ch := range channels {
		switch ch.Visibility {
		case "public", "":
			visible = append(visible, ch)
		case "admin-only":
			// MEMBER cannot see admin-only channels
			continue
		case "role-specific":
			if allowedChannelIDs[ch.ID] {
				visible = append(visible, ch)
			}
		default:
			// Unknown visibility — skip (deny by default)
		}
	}
	return visible, nil
}

// GetChannel retrieves a channel by ID
func (s *Service) GetChannel(channelID string) (*model.Channel, error) {
	// C6: delegate to ChannelRepository (cache-wrapped in production).
	ch, err := s.channelRepo.GetByID(context.Background(), channelID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return nil, errors.ErrInternal
	}
	if strings.EqualFold(ch.Type, "dm") {
		return nil, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}
	return ch, nil
}

// CreateChannel creates a new channel
//
// sortGroup 为可选分组名（A1-S1）：落库前经 NormalizeSortGroup 规范化（trim +
// 长度校验），空串表示「未分组」。
func (s *Service) CreateChannel(name, channelType string, isPrivate bool, audioQuality, visibility, sortGroup string) (*model.Channel, error) {
	if !validator.ValidateChannelName(name) {
		return nil, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid channel name")
	}

	// Direct messages are not a current product capability. Keep the legacy
	// storage model for historical data, but do not create new DM channels.
	switch channelType {
	case "text", "voice":
		// ok
	default:
		return nil, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid channel type, must be one of: text, voice")
	}

	// Validate audio quality (per design doc §6.5: fluent/standard/high/ultra)
	voiceQuality := "standard"
	if audioQuality != "" {
		switch audioQuality {
		case "fluent", "standard", "high", "ultra":
			voiceQuality = audioQuality
		default:
			return nil, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid audioQuality, must be one of: fluent, standard, high, ultra")
		}
	}

	// Check for duplicate channel name within the same space
	space, err := s.GetSpace()
	if err != nil {
		return nil, err
	}

	var existingCount int64
	if err := s.db.Model(&model.Channel{}).Where("space_id = ? AND name = ?", space.ID, name).Count(&existingCount).Error; err != nil {
		return nil, errors.ErrInternal
	}
	if existingCount > 0 {
		return nil, errors.New(errors.CHANNEL_ALREADY_EXISTS, "channel name already exists")
	}

	// Get max position
	var maxPos int
	if err := s.db.Model(&model.Channel{}).Select("COALESCE(MAX(position), -1)").Scan(&maxPos).Error; err != nil {
		return nil, errors.ErrInternal
	}

	// Resolve visibility: explicit visibility param takes precedence;
	// otherwise fall back to legacy isPrivate flag (private=true → admin-only).
	resolvedVisibility := visibility
	if resolvedVisibility == "" {
		if isPrivate {
			resolvedVisibility = "admin-only"
		} else {
			resolvedVisibility = "public"
		}
	}
	// Validate visibility value
	switch resolvedVisibility {
	case "public", "admin-only", "role-specific":
		// ok
	default:
		return nil, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid visibility")
	}

	// A1-S1：分组名规范化与校验（trim + rune 数上限），空串 = 未分组。
	normalizedSortGroup, err := NormalizeSortGroup(sortGroup)
	if err != nil {
		return nil, err
	}

	ch := &model.Channel{
		ID:           idgen.GenerateID(idgen.PrefixChannel),
		SpaceID:      space.ID,
		Name:         name,
		Type:         channelType,
		Position:     maxPos + 1,
		SortGroup:    normalizedSortGroup,
		Visibility:   resolvedVisibility,
		VoiceQuality: voiceQuality,
	}

	// C6: delegate create to ChannelRepository (cache-wrapped in production).
	if err := s.channelRepo.Create(context.Background(), ch); err != nil {
		return nil, errors.ErrInternal
	}
	return ch, nil
}

// UpdateChannel updates a channel
func (s *Service) UpdateChannel(channelID string, updates map[string]interface{}) error {
	var ch model.Channel
	if err := s.db.First(&ch, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return errors.ErrInternal
	}
	if strings.EqualFold(ch.Type, "dm") {
		return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}
	if err := s.db.Model(&ch).Updates(updates).Error; err != nil {
		return errors.ErrInternal
	}
	s.invalidateChannelCache(ch.ID, ch.SpaceID)
	return nil
}

// DeleteChannel deletes a channel and all its associated data
func (s *Service) DeleteChannel(channelID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var ch model.Channel
		if err := tx.Select("id, type").First(&ch, "id = ?", channelID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
			}
			return errors.ErrInternal
		}
		if strings.EqualFold(ch.Type, "dm") {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}

		// 1. Delete reactions for messages in this channel
		var messageIDs []string
		if err := tx.Model(&model.Message{}).Where("channel_id = ?", channelID).Pluck("id", &messageIDs).Error; err != nil {
			return errors.ErrInternal
		}
		if len(messageIDs) > 0 {
			if err := tx.Where("message_id IN ?", messageIDs).Delete(&model.Reaction{}).Error; err != nil {
				return errors.ErrInternal
			}
		}

		// 2. Delete messages (soft delete)
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.Message{}).Error; err != nil {
			return errors.ErrInternal
		}

		// 3. Delete voice participants for voice rooms in this channel
		var roomIDs []string
		if err := tx.Model(&model.VoiceRoom{}).Where("bind_channel_id = ?", channelID).Pluck("id", &roomIDs).Error; err != nil {
			return errors.ErrInternal
		}
		if len(roomIDs) > 0 {
			if err := tx.Where("room_id IN ?", roomIDs).Delete(&model.VoiceParticipant{}).Error; err != nil {
				return errors.ErrInternal
			}
		}

		// 4. Delete voice rooms
		if err := tx.Where("bind_channel_id = ?", channelID).Delete(&model.VoiceRoom{}).Error; err != nil {
			return errors.ErrInternal
		}

		// 5. Delete screen share sessions bound to this channel
		// ScreenShareSession 的列名是 channel_id（000008 迁移把 bind_channel_id 改名回来了）。
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.ScreenShareSession{}).Error; err != nil {
			return errors.ErrInternal
		}

		// 6. Delete channel role permissions
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.ChannelRolePermission{}).Error; err != nil {
			return errors.ErrInternal
		}

		// 7. Delete user channel reads
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.UserChannelRead{}).Error; err != nil {
			return errors.ErrInternal
		}

		// 7b. T4/S-1: 删除该频道的用户静音行（静音是按频道的个人偏好，频道没了就无意义）
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.UserChannelMute{}).Error; err != nil {
			return errors.ErrInternal
		}

		// 8. Finally delete channel
		result := tx.Delete(&model.Channel{}, "id = ?", channelID)
		if result.Error != nil {
			return errors.ErrInternal
		}
		if result.RowsAffected == 0 {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return nil
	})
}

// ReorderChannels updates channel positions
func (s *Service) ReorderChannels(channelIDs []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range channelIDs {
			if err := tx.Model(&model.Channel{}).Where("id = ?", id).Update("position", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateMemberRole updates a member's role.
func (s *Service) UpdateMemberRole(userID, role string) error {
	return s.db.Model(&model.User{}).Where("id = ?", userID).Update("role", role).Error
}

// RemoveMember removes a member from the space (soft delete)
func (s *Service) RemoveMember(userID string) error {
	return s.db.Delete(&model.User{ID: userID}).Error
}

// PinMessage pins a message to a channel
func (s *Service) PinMessage(channelID, messageID string) error {
	var ch model.Channel
	if err := s.db.Select("id, type").First(&ch, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return errors.ErrInternal
	}
	if strings.EqualFold(ch.Type, "dm") {
		return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}

	var msg model.Message
	if err := s.db.First(&msg, "id = ? AND channel_id = ?", messageID, channelID).Error; err != nil {
		return errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
	}
	if err := s.db.Model(&model.Channel{}).Where("id = ?", channelID).Update("pinned_message_id", messageID).Error; err != nil {
		return errors.ErrInternal
	}
	s.invalidateChannelCache(channelID, "")
	return nil
}

// UnpinMessage removes the pinned message from a channel
func (s *Service) UnpinMessage(channelID string) error {
	var ch model.Channel
	if err := s.db.Select("id, type").First(&ch, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return errors.ErrInternal
	}
	if strings.EqualFold(ch.Type, "dm") {
		return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}

	if err := s.db.Model(&model.Channel{}).Where("id = ?", channelID).Update("pinned_message_id", nil).Error; err != nil {
		return errors.ErrInternal
	}
	s.invalidateChannelCache(channelID, "")
	return nil
}

// SetChannelMute 设置/取消用户对某个频道的静音（T4/S-1）。
//
//   - muted=false：删除该 (user_id, channel_id) 静音行（幂等，行不存在也是成功）；
//   - muted=true：重建静音行。durationMinutes > 0 表示限时静音（MutedUntil=now+n 分钟），
//     否则为永久静音（MutedUntil=nil）。删除重建放在同一事务内，靠唯一索引兜底并发。
//
// 与 GetChannel 一致：频道必须存在，DM 类型不允许静音。
func (s *Service) SetChannelMute(userID, channelID string, muted bool, durationMinutes int) error {
	if userID == "" {
		return errors.ErrUnauthorized
	}

	var ch model.Channel
	if err := s.db.Select("id, type").First(&ch, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return errors.ErrInternal
	}
	if strings.EqualFold(ch.Type, "dm") {
		return errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
	}

	if !muted {
		if err := s.db.Where("user_id = ? AND channel_id = ?", userID, channelID).
			Delete(&model.UserChannelMute{}).Error; err != nil {
			return errors.ErrInternal
		}
		return nil
	}

	var until *time.Time
	if durationMinutes > 0 {
		t := time.Now().UTC().Add(time.Duration(durationMinutes) * time.Minute)
		until = &t
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND channel_id = ?", userID, channelID).
			Delete(&model.UserChannelMute{}).Error; err != nil {
			return errors.ErrInternal
		}
		row := model.UserChannelMute{
			ID:         idgen.GenerateID(idgen.PrefixEvent),
			UserID:     userID,
			ChannelID:  channelID,
			MutedUntil: until,
		}
		if err := tx.Create(&row).Error; err != nil {
			return errors.ErrInternal
		}
		return nil
	})
}

// ListChannelMutes 返回当前用户仍然生效的静音清单（永久 + 未到期的限时静音）。
// 已过期的限时静音行不返回（留在表里无副作用，下次开启静音时被删除重建覆盖）。
func (s *Service) ListChannelMutes(userID string) ([]model.UserChannelMute, error) {
	if userID == "" {
		return nil, errors.ErrUnauthorized
	}
	var mutes []model.UserChannelMute
	if err := s.db.
		Where("user_id = ? AND (muted_until IS NULL OR muted_until > ?)", userID, time.Now().UTC()).
		Order("created_at ASC").Find(&mutes).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return mutes, nil
}
