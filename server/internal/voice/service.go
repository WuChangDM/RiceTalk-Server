package voice

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/repositories"
)

// Service handles voice business logic
// SfxTrigger is the seam the sfx module implements so voice can fire entrance/exit
// sounds without importing sfx (avoids a package cycle). All guards (dedup, binding
// lookup, bot filtering) live inside the sfx implementation.
type SfxTrigger interface {
	TriggerJoin(userID, roomID string)
	TriggerLeave(userID, roomID string)
}

type Service struct {
	db                   *gorm.DB
	cfg                  *config.Config
	hub                  *realtime.Hub
	roomClient           *lksdk.RoomServiceClient
	egressClient         *lksdk.EgressClient
	voiceRoomRepo        repositories.VoiceRoomRepository
	voiceParticipantRepo repositories.VoiceParticipantRepository
	sfx                  SfxTrigger
}

// SetSfxTrigger injects the sfx trigger (called once during app wiring).
func (s *Service) SetSfxTrigger(t SfxTrigger) { s.sfx = t }

// NewService creates a new voice service with default GORM-backed repositories.
// Use NewServiceWithRepos to inject mock repositories.
func NewService(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Service {
	s := &Service{
		db:                   db,
		cfg:                  cfg,
		hub:                  hub,
		voiceRoomRepo:        gormrepo.NewGormVoiceRoomRepository(db),
		voiceParticipantRepo: gormrepo.NewGormVoiceParticipantRepository(db),
	}
	// Initialize LiveKit service clients if configured
	if cfg.LiveKitURL != "" && cfg.LiveKitAPIKey != "" && cfg.LiveKitAPISecret != "" {
		s.roomClient = lksdk.NewRoomServiceClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
		s.egressClient = lksdk.NewEgressClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	}
	return s
}

// NewServiceWithRepos creates a new voice service with the given repositories.
func NewServiceWithRepos(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, voiceRoomRepo repositories.VoiceRoomRepository, voiceParticipantRepo repositories.VoiceParticipantRepository) *Service {
	s := &Service{
		db:                   db,
		cfg:                  cfg,
		hub:                  hub,
		voiceRoomRepo:        voiceRoomRepo,
		voiceParticipantRepo: voiceParticipantRepo,
	}
	if voiceRoomRepo == nil {
		s.voiceRoomRepo = gormrepo.NewGormVoiceRoomRepository(db)
	}
	if voiceParticipantRepo == nil {
		s.voiceParticipantRepo = gormrepo.NewGormVoiceParticipantRepository(db)
	}
	if cfg.LiveKitURL != "" && cfg.LiveKitAPIKey != "" && cfg.LiveKitAPISecret != "" {
		s.roomClient = lksdk.NewRoomServiceClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
		s.egressClient = lksdk.NewEgressClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	}
	return s
}

// GetStatus returns voice service status
func (s *Service) GetStatus() map[string]interface{} {
	return map[string]interface{}{
		"status":      "ready",
		"livekit_url": s.cfg.LiveKitURLForClient(),
		"e2ee":        s.cfg.LiveKitE2EEEnabled,
	}
}

// roomName returns the LiveKit room name for a voice room ID.
func roomName(roomID string) string {
	return "rrt-room-" + roomID
}

// getVoiceRoomByID fetches a voice room by its primary key.
func (s *Service) getVoiceRoomByID(roomID string) (*model.VoiceRoom, error) {
	room, err := s.voiceRoomRepo.GetByID(context.Background(), roomID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.VOICE_ROOM_NOT_FOUND, "voice room not found")
		}
		return nil, errors.ErrInternal
	}
	return room, nil
}

// findOrCreateVoiceRoom returns an existing VoiceRoom or auto-creates one.
// Auto-creation is supported when roomID matches an existing channel; in that
// case the new room inherits the channel's space and quality settings and binds
// to the channel. This preserves backward compatibility for clients that still
// pass a channel ID as the room identifier while keeping the service centred on
// VoiceRoom as the source of truth.
func (s *Service) findOrCreateVoiceRoom(roomID string) (*model.VoiceRoom, error) {
	var room model.VoiceRoom
	if err := s.db.First(&room, "id = ?", roomID).Error; err == nil {
		return &room, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, errors.ErrInternal
	}

	// Reuse an existing room bound to this channel (e.g. an independently-created
	// room whose ID differs from the channel ID). This prevents duplicate-room
	// creation and keeps the token/DB state consistent with the actual room.
	if err := s.db.First(&room, "bind_channel_id = ?", roomID).Error; err == nil {
		return &room, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, errors.ErrInternal
	}

	// Fallback: if roomID refers to a channel, provision an independent room.
	var channel model.Channel
	if err := s.db.First(&channel, "id = ?", roomID).Error; err == nil {
		quality := channel.VoiceQuality
		if quality == "" {
			quality = "standard"
		}
		bind := channel.ID
		room = model.VoiceRoom{
			ID:            roomID,
			SpaceID:       channel.SpaceID,
			BindChannelID: &bind,
			LiveKitRoom:   roomName(roomID),
			E2EEEnabled:   s.cfg.LiveKitE2EEEnabled,
			Quality:       quality,
		}
		if err := s.db.Create(&room).Error; err != nil {
			return nil, errors.New(errors.VOICE_SERVICE_UNAVAILABLE, "failed to create voice room: "+err.Error())
		}
		return &room, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, errors.ErrInternal
	}

	return nil, errors.New(errors.VOICE_ROOM_NOT_FOUND, "voice room not found")
}

// GenerateToken creates a LiveKit-compatible token for a user to join a voice room.
// Uses LiveKit's official auth package (go-jose signing, correct claim structure).
func (s *Service) GenerateToken(userID, username, role, roomID string) (string, error) {
	return s.GenerateTokenWithIdentity(userID, username, role, roomID, "")
}

// GenerateTokenWithIdentity 支持自定义 identity（屏幕共享原生模块以 {uid}:share
// 虚身份加入房间，与 renderer 主身份共存）。identity 为空时退化为 userID（现状行为）。
// userID 始终是真实用户 ID：空间成员校验、越权校验都以它为准，identity 只影响
// 签进 token 的 identity 字段。
func (s *Service) GenerateTokenWithIdentity(userID, username, role, roomID, identity string) (string, error) {
	// 诊断日志：确认共享双轨虚身份 token 请求是否带上 identity（排查连接重建）
	if identity != "" {
		log.Printf("[voice] GenerateTokenWithIdentity user=%s room=%s identity=%s", userID, roomID, identity)
	}
	if identity == "" {
		identity = userID
	}
	if err := validateVirtualIdentity(identity, userID); err != nil {
		return "", err
	}

	room, err := s.findOrCreateVoiceRoom(roomID)
	if err != nil {
		return "", err
	}

	// Verify user is a member of the space (OWNER and ADMIN are always allowed).
	if role != "OWNER" && role != "ADMIN" {
		var memberCount int64
		if err := s.db.Model(&model.Membership{}).Where("space_id = ? AND user_id = ?", room.SpaceID, userID).Count(&memberCount).Error; err != nil {
			return "", errors.ErrInternal
		}
		if memberCount == 0 {
			return "", errors.ErrForbidden
		}
	}

	quality := room.Quality
	if quality == "" {
		quality = "standard"
	}

	lkRoom := room.LiveKitRoom
	if lkRoom == "" {
		lkRoom = roomName(room.ID)
	}

	// 登录名 username 已退役（仅开发/运维定位用），token 的 name claim 必须用
	// 显示名 displayName——否则观看端会显示登录名（如 "alice"）而非 "爱丽丝"。
	// 非真实用户（bot 等）查库失败时回退传入的 username。
	tokenName := username
	var nameUser model.User
	if err := s.db.Select("display_name", "username").First(&nameUser, "id = ?", userID).Error; err == nil {
		if nameUser.DisplayName != "" {
			tokenName = nameUser.DisplayName
		} else if nameUser.Username != "" {
			tokenName = nameUser.Username
		}
	}

	at := auth.NewAccessToken(s.cfg.LiveKitAPIKey, s.cfg.LiveKitAPISecret)
	// L13: 在 metadata 中标记 E2EE 启用状态，客户端据此决定是否启用 E2EE
	metadataMap := map[string]string{"voice_quality": quality}
	if s.cfg.LiveKitE2EEEnabled {
		metadataMap["e2ee_enabled"] = "true"
	}
	metadata, _ := json.Marshal(metadataMap)
	at.SetIdentity(identity).
		SetName(tokenName).
		SetValidFor(8 * time.Minute).
		SetMetadata(string(metadata)).
		SetVideoGrant(&auth.VideoGrant{
			RoomJoin:       true,
			Room:           lkRoom,
			CanPublish:     boolPtr(true),
			CanSubscribe:   boolPtr(true),
			CanPublishData: boolPtr(true),
		})

	tokenString, err := at.ToJWT()
	if err != nil {
		return "", errors.New(errors.VOICE_SERVICE_UNAVAILABLE, "failed to generate voice token: "+err.Error())
	}

	// Persist the latest room metadata. A failure here is non-fatal for the caller
	// (the LiveKit token has already been generated) but we log it so
	// operators can detect persistent DB issues.
	room.LiveKitRoom = lkRoom
	room.E2EEEnabled = s.cfg.LiveKitE2EEEnabled
	if err := s.db.Save(room).Error; err != nil {
		return "", errors.New(errors.VOICE_SERVICE_UNAVAILABLE, "failed to record voice room: "+err.Error())
	}

	return tokenString, nil
}

func boolPtr(b bool) *bool {
	return &b
}

// ShareIdentitySuffix 是屏幕共享虚身份的固定后缀：{uid}:share。
const ShareIdentitySuffix = ":share"

// VoiceIdentitySuffix 是原生语音发布虚身份的固定后缀：{uid}:voice-native。
// T28（DES-20261001-01 §13.4）：原生音频经 native-screenshare 模块以该虚身份
// 独立连接 LiveKit 发布麦克风轨（LiveKit 同身份第二连接会踢掉原连接，故不能
// 复用主 token 主连接）。与 :share 同权限模型：归属校验以 userID 为准。
const VoiceIdentitySuffix = ":voice-native"

// validateVirtualIdentity 校验虚身份格式与归属：仅允许真实身份本身，或该用户
// 自己的 {uid}:share / {uid}:voice-native 虚身份，其余一律拒绝（防止冒用他人
// 虚身份进房）。
func validateVirtualIdentity(identity, userID string) error {
	if identity == userID {
		return nil
	}
	if identity == userID+ShareIdentitySuffix {
		return nil
	}
	if identity == userID+VoiceIdentitySuffix {
		return nil
	}
	return errors.ErrForbidden
}

// shareVirtualKicker 返回踢出该房间 :share 虚身份连接的闭包；roomClient 不可用
// 时返回 nil。LeaveVoice/KickParticipant/CleanupParticipant 级联结束共享时调用，
// 防止原生模块的独立 LiveKit 连接悬挂在房间里。
func (s *Service) shareVirtualKicker(roomID string) func(userID string) {
	if s.roomClient == nil {
		return nil
	}
	livekitRoom := "rrt-room-" + roomID
	if room, err := s.getVoiceRoomByID(roomID); err == nil && room.LiveKitRoom != "" {
		livekitRoom = room.LiveKitRoom
	}
	return func(userID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.roomClient.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
			Room:     livekitRoom,
			Identity: userID + ShareIdentitySuffix,
		})
	}
}

// JoinVoice records a user joining a voice room.
func (s *Service) JoinVoice(userID, roomID string) error {
	room, err := s.findOrCreateVoiceRoom(roomID)
	if err != nil {
		return err
	}

	// 检查用户是否已在房间中（去重，仅检查活跃参与者）
	var existing model.VoiceParticipant
	if err := s.db.Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).First(&existing).Error; err == nil {
		s.BroadcastParticipantsList(room.ID)
		return nil
	} else if err != gorm.ErrRecordNotFound {
		return err
	}

	// Add participant
	now := time.Now()
	participant := &model.VoiceParticipant{
		ID:           idgen.GenerateID(idgen.PrefixSession),
		RoomID:       room.ID,
		UserID:       userID,
		JoinedAt:     now,
		LastActiveAt: now,
	}
	if err := s.db.Create(participant).Error; err != nil {
		return err
	}
	// 广播完整列表给房间订阅者（成员快照携带每个人的音效绑定 ID，客户端本地播放进出音）
	s.BroadcastParticipantsList(room.ID)
	return nil
}

// LeaveVoice removes a user from a voice room.
func (s *Service) LeaveVoice(userID, roomID string) error {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		if appErr, ok := err.(*errors.AppError); ok && appErr.Code == errors.VOICE_ROOM_NOT_FOUND {
			return nil // Room doesn't exist, nothing to do
		}
		return err
	}

	// 软删除：记录离开时间和原因，便于后续诊断；定期清理超过7天的记录
	now := time.Now().UTC()
	res := s.db.Model(&model.VoiceParticipant{}).
		Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).
		Updates(map[string]interface{}{
			"left_at":           &now,
			"disconnect_reason": "user_left",
		})
	if res.Error != nil {
		return res.Error
	}
	// 级联结束该用户在此房间的活跃屏幕共享（连接断开则共享终止）
	endActiveScreenShares(s.db, s.hub, userID, room.ID, "user_left", s.shareVirtualKicker(room.ID))
	// 广播完整列表给房间订阅者（成员快照携带音效绑定 ID，客户端本地播放进出音）
	s.BroadcastParticipantsList(room.ID)
	// 额外定向推给离开者本人（其可能已退订频道，收不到上面的房间广播）
	s.BroadcastParticipantsListToUser(userID, room.ID)
	return nil
}

// CleanupParticipant removes a user from a specific room and broadcasts the updated list.
// Used by OnUserFullyOffline when a user fully disconnects.
func (s *Service) CleanupParticipant(userID, roomID string) error {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		if appErr, ok := err.(*errors.AppError); ok && appErr.Code == errors.VOICE_ROOM_NOT_FOUND {
			return nil
		}
		return err
	}
	// 软删除：记录离开时间和原因
	now := time.Now().UTC()
	res := s.db.Model(&model.VoiceParticipant{}).
		Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).
		Updates(map[string]interface{}{
			"left_at":           &now,
			"disconnect_reason": "server_cleanup",
		})
	if res.Error != nil {
		return res.Error
	}
	// 级联结束该用户在此房间的活跃屏幕共享（用户完全离线则共享终止）
	endActiveScreenShares(s.db, s.hub, userID, room.ID, "server_cleanup", s.shareVirtualKicker(room.ID))
	s.BroadcastParticipantsList(room.ID)
	// 定向推给离开者本人（覆盖其已退订的情况）
	s.BroadcastParticipantsListToUser(userID, room.ID)
	return nil
}

// broadcastTargets returns the destinations for a room's real-time events.
func broadcastTargets(room *model.VoiceRoom) []string {
	if room.BindChannelID != nil && *room.BindChannelID != "" {
		return []string{room.ID, *room.BindChannelID}
	}
	return []string{room.ID}
}

// buildParticipantsSnapshot 构造 voice_participants_updated 消息体（房间当前权威成员列表）。
func (s *Service) buildParticipantsSnapshot(roomID string) ([]byte, error) {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		return nil, err
	}
	participants, err := s.GetParticipants(roomID)
	if err != nil {
		return nil, err
	}

	payload := map[string]interface{}{
		"roomId":       room.ID,
		"participants": participants,
	}
	if room.BindChannelID != nil && *room.BindChannelID != "" {
		payload["channelId"] = *room.BindChannelID
	}

	return json.Marshal(map[string]interface{}{
		"type":    "voice_participants_updated",
		"payload": payload,
	})
}

// BroadcastParticipantsList 广播房间内完整参与者列表给所有订阅者
func (s *Service) BroadcastParticipantsList(roomID string) {
	if s.hub == nil {
		return
	}
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		return
	}
	data, err := s.buildParticipantsSnapshot(roomID)
	if err != nil {
		return
	}
	for _, target := range broadcastTargets(room) {
		if room.BindChannelID != nil && target == *room.BindChannelID {
			s.hub.BroadcastToChannel(target, data)
		} else {
			s.hub.BroadcastToRoom(target, data)
		}
	}
}

// BroadcastParticipantsListToUser 把房间内完整参与者列表定向推送给指定用户。
// 彻底解决"离开者收不到自己已离开的快照"问题：BroadcastToChannel 只发给仍订阅
// 该频道的客户端，刚离开/断连的用户可能已退订，恰恰收不到最该收到的快照。
// 对齐 KOOK/Discord：成员变化对所有相关在线用户可见，不依赖客户端订阅时序。
func (s *Service) BroadcastParticipantsListToUser(userID, roomID string) {
	if s.hub == nil || userID == "" {
		return
	}
	data, err := s.buildParticipantsSnapshot(roomID)
	if err != nil {
		return
	}
	s.hub.BroadcastToUser(userID, data)
}

// CleanupOnStartup reconciles voice_participants with the actual LiveKit
// state on server boot. Any record that does not correspond to a participant
// currently present in the LiveKit room is removed, and the affected rooms
// receive a fresh voice_participants_updated so already-connected clients
// see the correct state immediately.
//
// On boot there is no reliable in-process source of truth for "is this user
// really in the room?" — the previous server may have been killed before its
// OnUserFullyOffline hook could fire, and the user_presences table tracks
// connection state that is not 1:1 with voice room membership. We therefore
// cross-check against LiveKit, with a safe fallback when LiveKit is not
// available: drop any record that fails basic sanity checks (orphan, or
// older than 24h).
func (s *Service) CleanupOnStartup() {
	// 宽限期：等待 LiveKit 子进程就绪 + 已连接客户端重连。服务端重启时内嵌
	// LiveKit 会一并重启，客户端 LiveKit 连接在秒级内自动重连（媒体保持正常），
	// 若本函数立即执行，新 LiveKit 房间还是空的，ListParticipants 返回空集合，
	// 活跃的 voice_participants 会被误判为 stale 而全部软删——这是成员列表
	// 塌陷（多人仍能通话/共享但列表只剩自己）的根因。等待重连后再核对 LiveKit。
	time.Sleep(10 * time.Second)

	// 1. Find affected rooms
	var affectedRoomIDs []string
	_ = s.db.Model(&model.VoiceParticipant{}).
		Distinct("voice_rooms.id").
		Joins("JOIN voice_rooms ON voice_rooms.id = voice_participants.room_id").
		Pluck("voice_rooms.id", &affectedRoomIDs)
	// Note: GORM may wrap a nil error in a non-nil interface. The query
	// succeeded (affectedRoomIDs is populated) so we proceed regardless.
	if len(affectedRoomIDs) == 0 {
		return
	}

	// 2. Try to build the LiveKit truth set.
	liveSet := make(map[string]map[string]bool) // roomID -> userID set
	liveKitReachable := false
	if s.roomClient != nil {
		var rooms []model.VoiceRoom
		if err := s.db.Find(&rooms).Error; err == nil {
			for _, room := range rooms {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, err := s.roomClient.ListParticipants(ctx, &livekit.ListParticipantsRequest{
					Room: room.LiveKitRoom,
				})
				cancel()
				if err != nil || resp == nil {
					continue
				}
				liveKitReachable = true
				liveSet[room.ID] = make(map[string]bool)
				for _, p := range resp.Participants {
					liveSet[room.ID][p.Identity] = true
				}
			}
		}
	}

	// 3. Decide which DB rows to keep, then bulk-soft-delete the rest.
	// 仅检查活跃参与者（left_at IS NULL），已离开的记录由定期清理任务处理
	var participants []model.VoiceParticipant
	if err := s.db.Where("left_at IS NULL").Find(&participants).Error; err != nil {
		return
	}
	// On server boot, the previous process is gone. Any voice_participants
	// row that does not match a real LiveKit participant is stale — the
	// user's WebSocket / LiveKit connection was severed when the server
	// died. LiveKit's webhook will re-insert a record for anyone who is
	// truly still connected (their LiveKit client will re-establish).
	staleCutoff := time.Now().Add(-1 * time.Hour) // generous grace window
	var toDelete []string
	for _, p := range participants {
		// Always drop orphan records.
		var userExists int64
		if err := s.db.Model(&model.User{}).Where("id = ?", p.UserID).Count(&userExists).Error; err != nil {
			// Conservative: don't delete on query failure
			continue
		}
		if userExists == 0 {
			toDelete = append(toDelete, p.ID)
			continue
		}
		var roomExists int64
		if err := s.db.Model(&model.VoiceRoom{}).Where("id = ?", p.RoomID).Count(&roomExists).Error; err != nil {
			continue
		}
		if roomExists == 0 {
			toDelete = append(toDelete, p.ID)
			continue
		}
		// If LiveKit is reachable, cross-check: only keep records that match
		// a real LiveKit participant.
		if liveKitReachable {
			if liveSet[p.RoomID] == nil || !liveSet[p.RoomID][p.UserID] {
				toDelete = append(toDelete, p.ID)
				continue
			}
		}
		// LiveKit not reachable (or participant is real): fall back to the
		// staleCutoff check. Records older than this are presumed dead.
		if p.LastActiveAt.Before(staleCutoff) {
			toDelete = append(toDelete, p.ID)
		}
	}
	if len(toDelete) > 0 {
		// 软删除：标记为启动清理，保留诊断信息
		now := time.Now().UTC()
		s.db.Model(&model.VoiceParticipant{}).
			Where("id IN ?", toDelete).
			Updates(map[string]interface{}{
				"left_at":           &now,
				"disconnect_reason": "startup_cleanup",
			})
	}

	// 4. Broadcast updated list to each affected room
	for _, roomID := range affectedRoomIDs {
		s.BroadcastParticipantsList(roomID)
	}
}

// GetParticipants returns participants in a voice room.
func (s *Service) GetParticipants(roomID string) ([]map[string]interface{}, error) {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		if appErr, ok := err.(*errors.AppError); ok && appErr.Code == errors.VOICE_ROOM_NOT_FOUND {
			return []map[string]interface{}{}, nil
		}
		return nil, errors.ErrInternal
	}

	// Return all current participants in this room.
	// 软删除模式：仅返回 left_at IS NULL 的活跃参与者
	// Joining/leaving is now fully responsible for keeping the table accurate
	// (JoinVoice / LeaveVoice / Webhook / OnUserFullyOffline). No 24h filter.
	participants, err := s.voiceParticipantRepo.GetActiveByRoomID(context.Background(), room.ID)
	if err != nil {
		return nil, errors.ErrInternal
	}

	// Batch query user info
	userIDs := make([]string, 0, len(participants))
	for _, p := range participants {
		userIDs = append(userIDs, p.UserID)
	}
	var users []model.User
	userMap := make(map[string]*model.User)
	if len(userIDs) > 0 {
		if err := s.db.Select("id, username, display_name, avatar").Where("id IN ?", userIDs).Find(&users).Error; err == nil {
			for i := range users {
				userMap[users[i].ID] = &users[i]
			}
		}
	}

	result := make([]map[string]interface{}, 0, len(participants))
	// 批量加载每个参与者的音效绑定（joinSoundId/leaveSoundId），随成员快照下发。
	// 客户端据此在成员 diff 时本地播放对应音效（TeamSpeak 本地事件音模型，不再服务端广播音频）。
	soundSettings := map[string]*model.UserSoundSettings{}
	if len(userIDs) > 0 {
		var sts []model.UserSoundSettings
		if err := s.db.Where("user_id IN ? AND (join_sound_id IS NOT NULL OR leave_sound_id IS NOT NULL)", userIDs).Find(&sts).Error; err == nil {
			for i := range sts {
				soundSettings[sts[i].UserID] = &sts[i]
			}
		}
	}
	for _, p := range participants {
		user := userMap[p.UserID]
		username := ""
		displayName := ""
		avatar := ""
		if user != nil {
			username = user.Username
			displayName = user.DisplayName
			if displayName == "" {
				displayName = username
			}
			avatar = user.Avatar
		}
		item := map[string]interface{}{
			"userId":          p.UserID,
			"username":        username,
			"displayName":     displayName,
			"avatar":          avatar,
			"isMuted":         p.IsMuted,
			"isSpeaking":      p.IsSpeaking,
			"isScreenSharing": p.IsScreenSharing,
			"speakerMuted":    p.SpeakerMuted,
			"lastLatencyMs":   p.LastLatencyMs,
			"joinedAt":        p.JoinedAt,
		}
		if st := soundSettings[p.UserID]; st != nil {
			if st.JoinSoundID != nil {
				item["joinSoundId"] = *st.JoinSoundID
			}
			if st.LeaveSoundID != nil {
				item["leaveSoundId"] = *st.LeaveSoundID
			}
		}
		result = append(result, item)
	}

	return result, nil
}

// getParticipantAudioTrackSIDs returns audio track SIDs for a participant in a room.
func (s *Service) getParticipantAudioTrackSIDs(ctx context.Context, roomName, identity string) ([]string, error) {
	if s.roomClient == nil {
		return nil, nil
	}
	resp, err := s.roomClient.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: roomName})
	if err != nil {
		return nil, err
	}
	var sids []string
	for _, p := range resp.Participants {
		if p.Identity == identity {
			for _, t := range p.Tracks {
				if t.Source == livekit.TrackSource_MICROPHONE {
					sids = append(sids, t.Sid)
				}
			}
		}
	}
	return sids, nil
}

// MuteParticipant force mutes/unmutes a user (DB + LiveKit)
func (s *Service) MuteParticipant(userID, roomID string, muted bool) error {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		return err
	}
	// Update DB first (source of truth) — 仅更新活跃参与者
	if err := s.db.Model(&model.VoiceParticipant{}).
		Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).
		Update("is_muted", muted).Error; err != nil {
		return err
	}
	// Call LiveKit to actually mute/unmute audio tracks (best effort)
	if s.roomClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sids, err := s.getParticipantAudioTrackSIDs(ctx, room.LiveKitRoom, userID)
		if err == nil {
			for _, sid := range sids {
				_, _ = s.roomClient.MutePublishedTrack(ctx, &livekit.MuteRoomTrackRequest{
					Room:     room.LiveKitRoom,
					TrackSid: sid,
					Muted:    muted,
				})
			}
		}
	}
	return nil
}

// KickParticipant removes a user from a voice room (DB + LiveKit)
func (s *Service) KickParticipant(userID, roomID string) error {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		return err
	}
	// 软删除：记录离开时间和原因
	now := time.Now().UTC()
	if err := s.db.Model(&model.VoiceParticipant{}).
		Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).
		Updates(map[string]interface{}{
			"left_at":           &now,
			"disconnect_reason": "kicked",
		}).Error; err != nil {
		return err
	}
	// 级联结束该用户在此房间的活跃屏幕共享（被踢则共享终止）
	endActiveScreenShares(s.db, s.hub, userID, room.ID, "kicked", s.shareVirtualKicker(room.ID))
	// Call LiveKit to remove participant (best effort)
	if s.roomClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.roomClient.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
			Room:     room.LiveKitRoom,
			Identity: userID,
		})
	}
	// 定向推给被踢者本人（其可能已退订频道，收不到房间广播的成员快照）
	s.BroadcastParticipantsListToUser(userID, room.ID)
	return nil
}

// StartRecording creates a recording record and starts LiveKit Egress
func (s *Service) StartRecording(userID, roomID string) (*model.VoiceRecording, error) {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		return nil, err
	}

	// DES-2026-0912-06 §4.6：E2EE 房间的加密轨在 Egress 侧无法解密
	// （egress 参与者拿不到客户端密钥），录出来必然是噪声/黑屏。
	// 与其产出坏录像，不如明确拒绝并给出原因。
	if room.E2EEEnabled {
		return nil, errors.New(
			errors.VOICE_E2EE_RECORDING_UNSUPPORTED,
			"room "+room.ID+" has end-to-end encryption enabled, recording is not supported",
		)
	}

	rec := &model.VoiceRecording{
		ID:        idgen.GenerateID(idgen.PrefixSession),
		RoomID:    room.ID,
		StartedBy: userID,
		StartedAt: time.Now(),
		Status:    "recording",
	}
	if err := s.db.Create(rec).Error; err != nil {
		return nil, errors.New(errors.VOICE_SERVICE_UNAVAILABLE, "failed to create recording")
	}
	// Start LiveKit Egress (best effort)
	if s.egressClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		egressInfo, err := s.egressClient.StartRoomCompositeEgress(ctx, &livekit.RoomCompositeEgressRequest{
			RoomName: room.LiveKitRoom,
			FileOutputs: []*livekit.EncodedFileOutput{
				{
					FileType: livekit.EncodedFileType_MP4,
					Filepath: "recordings/" + rec.ID + ".mp4",
				},
			},
		})
		if err == nil && egressInfo != nil {
			s.db.Model(rec).Update("egress_id", egressInfo.EgressId)
		} else if err != nil {
			// Mark recording as failed so clients don't wait forever.
			s.db.Model(rec).Update("status", "failed")
		}
	}
	return rec, nil
}

// StopRecording stops an active recording (DB + LiveKit Egress)
func (s *Service) StopRecording(roomID string) (*model.VoiceRecording, error) {
	var rec model.VoiceRecording
	if err := s.db.Where("room_id = ? AND status = ?", roomID, "recording").First(&rec).Error; err != nil {
		return nil, errors.New(errors.VOICE_RECORDING_NOT_FOUND, "no active recording found")
	}
	// Stop LiveKit Egress if we have an egress ID
	if s.egressClient != nil && rec.EgressID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.egressClient.StopEgress(ctx, &livekit.StopEgressRequest{
			EgressId: rec.EgressID,
		}); err != nil {
			// Continue to update DB; the Egress may already be stopped.
		}
	}
	now := time.Now()
	fileURL := "/storage/recordings/" + roomID + "_" + now.Format("20060102_150405") + ".mp4"
	durationSeconds := int(now.Sub(rec.StartedAt).Seconds())
	if durationSeconds < 0 {
		durationSeconds = 0
	}

	// Compute file size if the recording file already exists on local storage.
	fileSizeMB := 0.0
	filename := filepath.Base(fileURL)
	diskPath := filepath.Join(s.cfg.LocalDataPath, "recordings", filename)
	if fi, err := os.Stat(diskPath); err == nil {
		fileSizeMB = float64(fi.Size()) / (1024 * 1024)
	}

	if err := s.db.Model(&rec).Updates(map[string]interface{}{
		"stopped_at":       now,
		"status":           "completed",
		"file_url":         fileURL,
		"duration_seconds": durationSeconds,
		"file_size_mb":     fileSizeMB,
	}).Error; err != nil {
		return nil, errors.New(errors.VOICE_SERVICE_UNAVAILABLE, "failed to stop recording")
	}
	rec.StoppedAt = &now
	rec.FileURL = fileURL
	rec.Status = "completed"
	rec.DurationSeconds = durationSeconds
	rec.FileSizeMB = fileSizeMB
	return &rec, nil
}

// GetRecordings returns recording history for a room.
func (s *Service) GetRecordings(roomID string) ([]model.VoiceRecording, error) {
	var recordings []model.VoiceRecording
	query := s.db.Order("started_at DESC").Limit(50)
	if roomID != "" {
		query = query.Where("room_id = ?", roomID)
	}
	if err := query.Find(&recordings).Error; err != nil {
		return nil, errors.New(errors.VOICE_SERVICE_UNAVAILABLE, "failed to get recordings")
	}
	return recordings, nil
}

// GetE2EEKey returns the E2EE key for a room. If the key doesn't exist, it generates one.
// 密钥在存储时使用 AES-256 加密，返回给客户端的是解密后的原始字节。
func (s *Service) GetE2EEKey(roomID, userID string) (string, error) {
	room, err := s.getVoiceRoomByID(roomID)
	if err != nil {
		return "", err
	}

	// Verify user is a member of the room's space.
	var count int64
	if err := s.db.Model(&model.Membership{}).Where("space_id = ? AND user_id = ?", room.SpaceID, userID).Count(&count).Error; err != nil {
		return "", errors.ErrInternal
	}
	if count == 0 {
		return "", errors.ErrForbidden
	}

	// Find or create key
	var key model.E2EEKey
	if err := s.db.Where("room_id = ?", roomID).First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// Generate new key
			keyBytes, err := crypto.RandomToken(32)
			if err != nil {
				return "", errors.ErrInternal
			}
			// Encrypt key with JWTSecret before storage
			encryptedKey, err := crypto.EncryptAES(keyBytes, s.cfg.JWTSecret)
			if err != nil {
				return "", errors.ErrInternal
			}
			key = model.E2EEKey{
				ID:       idgen.GenerateID(idgen.PrefixSession),
				RoomID:   roomID,
				KeyBytes: encryptedKey,
			}
			if err := s.db.Create(&key).Error; err != nil {
				return "", errors.ErrInternal
			}
			return keyBytes, nil
		}
		return "", errors.ErrInternal
	}

	// Decrypt existing key
	decryptedKey, err := crypto.DecryptAES(key.KeyBytes, s.cfg.JWTSecret)
	if err != nil {
		return "", errors.ErrInternal
	}
	return decryptedKey, nil
}

// RotateE2EEKey generates a new E2EE key for a room, invalidating the old one.
// 删除旧密钥后立即生成新密钥，避免密钥空窗期
func (s *Service) RotateE2EEKey(roomID string) error {
	if _, err := s.getVoiceRoomByID(roomID); err != nil {
		return err
	}
	// Delete old key
	if err := s.db.Where("room_id = ?", roomID).Delete(&model.E2EEKey{}).Error; err != nil {
		return errors.ErrInternal
	}
	// Generate new key immediately
	keyBytes, err := crypto.RandomToken(32)
	if err != nil {
		return errors.ErrInternal
	}
	encryptedKey, err := crypto.EncryptAES(keyBytes, s.cfg.JWTSecret)
	if err != nil {
		return errors.ErrInternal
	}
	newKey := &model.E2EEKey{
		ID:       idgen.GenerateID(idgen.PrefixSession),
		RoomID:   roomID,
		KeyBytes: encryptedKey,
	}
	if err := s.db.Create(newKey).Error; err != nil {
		return errors.ErrInternal
	}
	return nil
}
