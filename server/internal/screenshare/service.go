package screenshare

import (
	"context"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/repositories"
)

// ShareIdentitySuffix 与 voice 包保持一致：{uid}:share 虚身份由屏幕共享原生
// 模块使用（独立 LiveKit 连接）。踢出/级联清理时必须同时覆盖主身份与虚身份。
// 不 import voice（避免包环），两个常量须保持同值。
const ShareIdentitySuffix = ":share"

// Service handles screenshare business logic
type Service struct {
	db          *gorm.DB
	cfg         *config.Config
	hub         *realtime.Hub
	roomClient  *lksdk.RoomServiceClient
	sessionRepo repositories.ScreenShareSessionRepository
	// kickParticipants 踢出 LiveKit 参与者的实现，默认走 roomClient，
	// 测试可注入替身以断言踢出行为。livekitRoom 为空时先经 livekitRoomForChannel 解析。
	kickParticipants func(livekitRoom, userID string)
	// kickVirtualParticipant 只踢 :share 虚身份的注入点（默认走 roomClient）。
	// 主动停止共享只清理原生模块虚身份，保留主身份语音连接；测试可注入断言。
	kickVirtualParticipant func(livekitRoom, userID string)
}

// NewService creates a new screenshare service with a default GORM-backed
// ScreenShareSessionRepository. Use NewServiceWithRepos to inject a mock.
func NewService(db *gorm.DB, cfg *config.Config) *Service {
	s := &Service{
		db:          db,
		cfg:         cfg,
		sessionRepo: gormrepo.NewGormScreenShareSessionRepository(db),
	}
	if cfg.LiveKitURL != "" && cfg.LiveKitAPIKey != "" && cfg.LiveKitAPISecret != "" {
		s.roomClient = lksdk.NewRoomServiceClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	}
	return s
}

// NewServiceWithRepos creates a new screenshare service with the given
// ScreenShareSessionRepository.
func NewServiceWithRepos(db *gorm.DB, cfg *config.Config, sessionRepo repositories.ScreenShareSessionRepository) *Service {
	if sessionRepo == nil {
		sessionRepo = gormrepo.NewGormScreenShareSessionRepository(db)
	}
	s := &Service{db: db, cfg: cfg, sessionRepo: sessionRepo}
	if cfg != nil && cfg.LiveKitURL != "" && cfg.LiveKitAPIKey != "" && cfg.LiveKitAPISecret != "" {
		s.roomClient = lksdk.NewRoomServiceClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	}
	return s
}

// SetHub injects the realtime hub（用于级联结束时广播 screenshare_state）。
// 由路由装配处调用一次；不设置时广播跳过，不影响其余逻辑。
func (s *Service) SetHub(h *realtime.Hub) { s.hub = h }

// kickShareParticipants 把共享者的主身份与 :share 虚身份都踢出 LiveKit 房间
// （best effort）。虚身份是独立连接，只踢主身份会让虚身份悬挂在房间里、
// 观看端黑屏。roomClient 为 nil（未配置 LiveKit）时跳过。
func (s *Service) kickShareParticipants(livekitRoom, userID string) {
	if s.roomClient == nil || livekitRoom == "" || userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.roomClient.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
		Room:     livekitRoom,
		Identity: userID,
	})
	_, _ = s.roomClient.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
		Room:     livekitRoom,
		Identity: userID + ShareIdentitySuffix,
	})
}

// livekitRoomForChannel 解析频道对应的 LiveKit 房间名。VoiceRoom 主键与绑定
// 频道同 ID；查不到（频道尚无语音房）时回退命名规则。
func (s *Service) livekitRoomForChannel(channelID string) string {
	var room model.VoiceRoom
	if err := s.db.Select("livekit_room").First(&room, "id = ?", channelID).Error; err == nil {
		if room.LiveKitRoom != "" {
			return room.LiveKitRoom
		}
	}
	return "rrt-room-" + channelID
}

// StartRequest contains the V2 screen share capture settings.
type StartRequest struct {
	ShareType     string `json:"shareType"`
	SourceID      string `json:"sourceId"`
	Resolution    string `json:"resolution"`
	FrameRate     int    `json:"frameRate"`
	MaxBitrate    int    `json:"maxBitrate"`
	ShareAudio    bool   `json:"shareAudio"`
	SuppressVoice bool   `json:"suppressVoice"`
	MaxViewers    int    `json:"maxViewers"`
}

// Session represents an active screenshare in API responses.
type Session struct {
	ID            string `json:"id,omitempty"`
	Active        bool   `json:"active"`
	UserID        string `json:"userId,omitempty"`
	Username      string `json:"username,omitempty"`
	SpaceID       string `json:"spaceId,omitempty"`
	ChannelID     string `json:"channelId,omitempty"`
	ShareType     string `json:"shareType,omitempty"`
	SourceID      string `json:"sourceId,omitempty"`
	Resolution    string `json:"resolution,omitempty"`
	FrameRate     int    `json:"frameRate,omitempty"`
	MaxBitrate    int    `json:"maxBitrate,omitempty"`
	ShareAudio    bool   `json:"shareAudio"`
	SuppressVoice bool   `json:"suppressVoice"`
	MaxViewers    int    `json:"maxViewers,omitempty"`
	ViewerCount   int    `json:"viewerCount,omitempty"`
	Status        string `json:"status,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
}

func (s *Service) toSessionResponse(session *model.ScreenShareSession) *Session {
	var user model.User
	s.db.Select("username").First(&user, "id = ?", session.UserID)

	return &Session{
		ID:            session.ID,
		Active:        session.Active,
		UserID:        session.UserID,
		Username:      user.Username,
		SpaceID:       session.SpaceID,
		ChannelID:     session.ChannelID,
		ShareType:     session.ShareType,
		SourceID:      session.SourceID,
		Resolution:    session.Resolution,
		FrameRate:     session.FrameRate,
		MaxBitrate:    session.MaxBitrate,
		ShareAudio:    session.ShareAudio,
		SuppressVoice: session.SuppressVoice,
		MaxViewers:    session.MaxViewers,
		ViewerCount:   s.countViewers(session.ChannelID, session.UserID),
		Status:        session.Status,
		StartedAt:     session.StartedAt.Format(time.RFC3339),
	}
}

// countViewers returns the number of active voice participants in the channel
// other than the sharer. VoiceRoom IDs are equal to the bound channel ID.
func (s *Service) countViewers(channelID, ownerID string) int {
	if channelID == "" {
		return 0
	}
	var count int64
	s.db.Model(&model.VoiceParticipant{}).
		Where("room_id = ? AND user_id != ? AND left_at IS NULL", channelID, ownerID).
		Count(&count)
	return int(count)
}

// GetSessionByID returns a raw screenshare session model by ID.
func (s *Service) GetSessionByID(sessionID string) (*model.ScreenShareSession, error) {
	session, err := s.sessionRepo.GetByID(context.Background(), sessionID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return session, nil
}

// GetStatus returns a screenshare session by ID.
func (s *Service) GetStatus(sessionID string) (*Session, error) {
	session, err := s.GetSessionByID(sessionID)
	if err != nil {
		return nil, err
	}
	return s.toSessionResponse(session), nil
}

// GetActiveSessionByChannel returns the active screenshare session bound to a channel.
func (s *Service) GetActiveSessionByChannel(channelID string) (*model.ScreenShareSession, error) {
	if channelID == "" {
		return nil, errors.ErrNotFound
	}
	session, err := s.sessionRepo.GetActiveByChannelID(context.Background(), channelID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return session, nil
}

// GetStatusByChannel returns the active screenshare state for a channel.
// If no session is active, it returns an inactive state.
func (s *Service) GetStatusByChannel(channelID string) (*Session, error) {
	session, err := s.GetActiveSessionByChannel(channelID)
	if err != nil {
		if err == errors.ErrNotFound {
			return &Session{Active: false, ChannelID: channelID}, nil
		}
		return nil, err
	}
	return s.toSessionResponse(session), nil
}

// GetActiveSessionsBySpace returns all active screenshare sessions in a space.
func (s *Service) GetActiveSessionsBySpace(spaceID string) ([]*Session, error) {
	sessions, err := s.sessionRepo.GetActiveBySpaceID(context.Background(), spaceID)
	if err != nil {
		return nil, errors.ErrInternal
	}

	result := make([]*Session, 0, len(sessions))
	for i := range sessions {
		result = append(result, s.toSessionResponse(&sessions[i]))
	}
	return result, nil
}

// Start creates a new screenshare session bound to a voice channel.
// It fails if another active session already exists on the same channel.
func (s *Service) Start(userID, spaceID, channelID string, req StartRequest) (string, error) {
	if channelID == "" {
		return "", errors.ErrBadRequest
	}

	now := time.Now().UTC()

	// Only one active session is allowed per channel.
	existing, err := s.sessionRepo.CountActiveByChannelID(context.Background(), channelID)
	if err != nil {
		return "", errors.ErrInternal
	}
	if existing > 0 {
		return "", errors.New(errors.SCREEN_SHARE_ALREADY_ACTIVE, "an active screen share already exists on this channel")
	}

	shareType := req.ShareType
	if shareType == "" {
		shareType = "screen"
	}
	if shareType != "screen" && shareType != "window" && shareType != "application" {
		return "", errors.New(errors.SCREEN_SOURCE_INVALID, "invalid screen share type")
	}
	if shareType != "screen" && req.SourceID == "" {
		return "", errors.New(errors.SCREEN_SOURCE_INVALID, "source id is required for window/application shares")
	}

	maxViewers := req.MaxViewers
	if maxViewers <= 0 {
		maxViewers = 50
	}

	session := model.ScreenShareSession{
		ID:            idgen.GenerateID(idgen.PrefixSession),
		UserID:        userID,
		SpaceID:       spaceID,
		ChannelID:     channelID,
		ShareType:     shareType,
		SourceID:      req.SourceID,
		Resolution:    req.Resolution,
		FrameRate:     req.FrameRate,
		MaxBitrate:    req.MaxBitrate,
		ShareAudio:    req.ShareAudio,
		SuppressVoice: req.SuppressVoice,
		MaxViewers:    maxViewers,
		ViewerCount:   0,
		Status:        "active",
		ViewerPolicy:  "all",
		Active:        true,
		StartedAt:     now,
	}
	if err := s.sessionRepo.Create(context.Background(), &session); err != nil {
		return "", errors.ErrInternal
	}
	return session.ID, nil
}

// StopByChannel marks the active screenshare on a channel as ended.
// The caller must be the sharer.
func (s *Service) StopByChannel(userID, channelID string) error {
	session, err := s.GetActiveSessionByChannel(channelID)
	if err != nil {
		return err
	}
	if session.UserID != userID {
		return errors.ErrForbidden
	}
	return s.stopSession(session.ID)
}

func (s *Service) stopSession(sessionID string) error {
	now := time.Now().UTC()
	result := s.db.Model(&model.ScreenShareSession{}).
		Where("id = ? AND active = ?", sessionID, true).
		Updates(map[string]interface{}{
			"active":       false,
			"status":       "ended",
			"ended_at":     &now,
			"ended_reason": "user_stopped",
		})
	if result.Error != nil {
		return errors.ErrInternal
	}
	if result.RowsAffected == 0 {
		return errors.ErrNotFound
	}
	// 级联踢出 LiveKit 侧的 :share 虚身份（best effort）：
	// 共享者主动停止时主身份仍在语音通话，必须保留；只清理原生模块的
	// 独立虚身份连接，否则虚身份悬挂在房间里、观看端黑屏。
	if sess, err := s.sessionRepo.GetByID(context.Background(), sessionID); err == nil {
		s.kickShareVirtualByChannel(sess.ChannelID, sess.UserID)
	}
	return nil
}

// kickShareVirtualParticipant 只踢出共享者的 :share 虚身份连接（best effort）。
// 用于共享者主动停止场景：主身份是共享者仍在进行的语音通话连接，绝不能踢，
// 只清理原生模块的独立虚身份，否则主身份被踢出频道、观看端黑屏。
func (s *Service) kickShareVirtualParticipant(livekitRoom, userID string) {
	if s.roomClient == nil || livekitRoom == "" || userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.roomClient.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
		Room:     livekitRoom,
		Identity: userID + ShareIdentitySuffix,
	})
}

// kickShareParticipantsByChannel 按频道踢出主身份与 :share 虚身份。
// channelID 即 VoiceRoom 主键（房间与绑定频道同 ID）。
func (s *Service) kickShareParticipantsByChannel(channelID, userID string) {
	if s.kickParticipants != nil {
		s.kickParticipants(s.livekitRoomForChannel(channelID), userID)
		return
	}
	s.kickShareParticipants(s.livekitRoomForChannel(channelID), userID)
}

// kickShareVirtualByChannel 按频道只踢出 :share 虚身份（保留主身份语音连接）。
func (s *Service) kickShareVirtualByChannel(channelID, userID string) {
	if s.kickVirtualParticipant != nil {
		s.kickVirtualParticipant(s.livekitRoomForChannel(channelID), userID)
		return
	}
	s.kickShareVirtualParticipant(s.livekitRoomForChannel(channelID), userID)
}

// PauseByChannel marks the active screenshare on a channel as paused.
// The caller must be the sharer.
func (s *Service) PauseByChannel(userID, channelID string) error {
	session, err := s.GetActiveSessionByChannel(channelID)
	if err != nil {
		return err
	}
	if session.UserID != userID {
		return errors.ErrForbidden
	}
	return s.updateStatus(session.ID, "paused")
}

// ResumeByChannel resumes a paused screenshare on a channel.
// The caller must be the sharer.
func (s *Service) ResumeByChannel(userID, channelID string) error {
	session, err := s.GetActiveSessionByChannel(channelID)
	if err != nil {
		return err
	}
	if session.UserID != userID {
		return errors.ErrForbidden
	}
	return s.updateStatus(session.ID, "active")
}

func (s *Service) updateStatus(sessionID, status string) error {
	result := s.db.Model(&model.ScreenShareSession{}).
		Where("id = ? AND active = ?", sessionID, true).
		Update("status", status)
	if result.Error != nil {
		return errors.ErrInternal
	}
	if result.RowsAffected == 0 {
		return errors.ErrNotFound
	}
	return nil
}

// ForceStopByChannel allows an Owner/Admin to force-stop the active screenshare on a channel.
func (s *Service) ForceStopByChannel(adminID, channelID string) error {
	session, err := s.GetActiveSessionByChannel(channelID)
	if err != nil {
		return err
	}
	isAdmin, err := s.IsUserSpaceAdmin(adminID, session.SpaceID)
	if err != nil {
		return err
	}
	if !isAdmin {
		return errors.New(errors.SCREENSHARE_FORCE_STOP_FORBIDDEN, "only admin/owner can force-stop screen share")
	}

	now := time.Now().UTC()
	result := s.db.Model(&model.ScreenShareSession{}).
		Where("id = ? AND active = ?", session.ID, true).
		Updates(map[string]interface{}{
			"active":       false,
			"status":       "force_stopped",
			"ended_at":     &now,
			"ended_reason": "force_stopped_by_admin",
		})
	if result.Error != nil {
		return errors.ErrInternal
	}
	if result.RowsAffected == 0 {
		return errors.ErrNotFound
	}
	// 管理员强制停止：踢出 LiveKit 侧的主身份与 :share 虚身份，
	// 否则只更新 DB，虚身份连接悬挂、观看端黑屏（T4 修正项）。
	s.kickShareParticipantsByChannel(session.ChannelID, session.UserID)
	return nil
}

// IsUserInSpace checks if a user is a member of a space.
func (s *Service) IsUserInSpace(userID, spaceID string) (bool, error) {
	var count int64
	if err := s.db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id = ?", spaceID, userID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// IsUserSpaceAdmin checks whether a user is an OWNER or ADMIN of a space.
func (s *Service) IsUserSpaceAdmin(userID, spaceID string) (bool, error) {
	var membership model.Membership
	if err := s.db.Where("space_id = ? AND user_id = ?", spaceID, userID).First(&membership).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	return membership.Role == "OWNER" || membership.Role == "ADMIN", nil
}

// CanUserStartScreenShare checks if a user can start a screen share in a space.
// Rules: user must be a member of the space.
func (s *Service) CanUserStartScreenShare(userID, spaceID string) (bool, error) {
	return s.IsUserInSpace(userID, spaceID)
}

// CanUserManageSession checks whether the user is allowed to view or manage a
// specific session. Rules: the session owner is always allowed; other users
// must be a member of the session's space and, for viewerPolicy=admin_only,
// must be an owner/admin of the space.
func (s *Service) CanUserManageSession(userID string, isGlobalAdmin bool, session *model.ScreenShareSession) (bool, error) {
	// The sharer always retains full control over their own session, regardless
	// of the current viewer policy.
	if session.UserID == userID {
		return true, nil
	}

	inSpace, err := s.IsUserInSpace(userID, session.SpaceID)
	if err != nil || !inSpace {
		return false, err
	}
	if session.ViewerPolicy == "admin_only" && !isGlobalAdmin {
		// Space-level admin check
		var membership model.Membership
		if err := s.db.Where("space_id = ? AND user_id = ?", session.SpaceID, userID).First(&membership).Error; err != nil {
			return false, err
		}
		if membership.Role != "OWNER" && membership.Role != "ADMIN" {
			return false, nil
		}
	}
	return true, nil
}
