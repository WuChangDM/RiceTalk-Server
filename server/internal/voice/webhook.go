package voice

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lkwebhook "github.com/livekit/protocol/webhook"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

// isBotIdentity reports whether a LiveKit participant identity belongs to a bot
// (music/TTS/sfx). Bots must never trigger entrance/exit sounds — critically, the sfx
// bot itself joining a room would otherwise retrigger sfx playback in an infinite loop.
// Filtering here also prevents bots from writing ghost VoiceParticipant rows.
func isBotIdentity(id string) bool {
	return strings.HasPrefix(id, "bot-music-") ||
		id == "bot-tts" ||
		strings.HasPrefix(id, "bot-sfx-")
}

// isVirtualIdentity reports whether an identity is a bot or a screenshare virtual
// identity ({uid}:share)。虚身份由屏幕共享原生模块使用，与 renderer 主身份共存于
// 同一房间：不写参与者表、不触发出入音效，避免成员列表/音效被双连接污染。
func isVirtualIdentity(id string) bool {
	if isBotIdentity(id) {
		return true
	}
	// {uid}:share 虚身份（uid 为 idgen 生成的无冒号 ID）
	if strings.HasSuffix(id, ShareIdentitySuffix) {
		prefix := strings.TrimSuffix(id, ShareIdentitySuffix)
		return prefix != "" && !strings.HasPrefix(prefix, "bot-")
	}
	return false
}

// normalizeIdentity 剥离 :share 后缀，返回真实 uid；非虚身份原样返回。
// track_published/unpublished 的 is_screen_sharing 状态必须落在真实用户行上，
// 否则双轨发布时观看端永远看不到"正在共享"。
func normalizeIdentity(id string) string {
	return strings.TrimSuffix(id, ShareIdentitySuffix)
}

// WebhookEvent represents a LiveKit webhook event
// LiveKit sends webhook events as POST requests with a JSON body.
// The event type is in the "event" field.
type WebhookEvent struct {
	Event           string                   `json:"event"`
	Room            *livekit.Room            `json:"room,omitempty"`
	Participant     *livekit.ParticipantInfo `json:"participant,omitempty"`
	Track           *livekit.TrackInfo       `json:"track,omitempty"`
	EgressInfo      *livekit.EgressInfo      `json:"egress_info,omitempty"`
	Id              string                   `json:"id,omitempty"`
	CreatedAt       int64                    `json:"created_at,omitempty"`
	RoomName        string                   `json:"roomName,omitempty"`
	NumParticipants uint32                   `json:"numParticipants,omitempty"`
	NumPublishers   uint32                   `json:"numPublishers,omitempty"`
}

// HandleWebhook processes LiveKit webhook events.
// LiveKit sends webhook events as POST requests to this endpoint.
// The endpoint should be configured in LiveKit's webhook settings.
// Signature is verified using the LiveKit API key/secret.
func (h *Handler) HandleWebhook(c *gin.Context) {
	// Verify webhook signature using LiveKit protocol SDK
	provider := auth.NewSimpleKeyProvider(h.cfg.LiveKitAPIKey, h.cfg.LiveKitAPISecret)
	verifiedEvent, err := lkwebhook.ReceiveWebhookEvent(c.Request, provider)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid webhook signature"})
		return
	}

	// Convert the verified protobuf event to our internal WebhookEvent structure
	event := WebhookEvent{
		Event:       verifiedEvent.Event,
		Room:        verifiedEvent.Room,
		Participant: verifiedEvent.Participant,
		Track:       verifiedEvent.Track,
		EgressInfo:  verifiedEvent.EgressInfo,
		Id:          verifiedEvent.Id,
		CreatedAt:   verifiedEvent.CreatedAt,
	}

	switch event.Event {
	case "participant_joined":
		h.handleParticipantJoined(&event)
	case "participant_left":
		h.handleParticipantLeft(&event)
	case "participant_updated":
		h.handleParticipantUpdated(&event)
	case "track_published":
		h.handleTrackPublished(&event)
	case "track_unpublished":
		h.handleTrackUnpublished(&event)
	case "track_muted":
		h.handleTrackMuted(&event, true)
	case "track_unmuted":
		h.handleTrackMuted(&event, false)
	case "egress_started":
		h.handleEgressStarted(&event)
	case "egress_ended":
		h.handleEgressEnded(&event)
	case "room_started":
		// Room created — no action needed
	case "room_finished":
		// Room destroyed — clean up VoiceRoom
		h.handleRoomFinished(&event)
	}

	c.Status(http.StatusOK)
}

func (h *Handler) handleParticipantJoined(event *WebhookEvent) {
	if event.Participant == nil || event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	userID := event.Participant.Identity
	if isVirtualIdentity(userID) {
		return // 机器人与 :share 虚身份进出不记参与者、不触发入场音效（防 sfx 无限循环、防双连接污染成员列表）
	}

	// Upsert participant in DB
	var room model.VoiceRoom
	if err := h.db.First(&room, "id = ?", roomID).Error; err != nil {
		return
	}
	var existing model.VoiceParticipant
	if err := h.db.Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).First(&existing).Error; err != nil {
		// Create new participant
		now := time.Now()
		joinTime := time.Unix(event.Participant.JoinedAt, 0)
		if event.Participant.JoinedAt == 0 {
			joinTime = now
		}
		h.db.Create(&model.VoiceParticipant{
			ID:           idgen.GenerateID(idgen.PrefixSession),
			RoomID:       room.ID,
			UserID:       userID,
			LiveKitSid:   event.Participant.Sid,
			JoinedAt:     joinTime,
			LastActiveAt: now,
		})
	} else {
		// User already in DB (e.g. rejoining) — refresh LastActiveAt and backfill LiveKitSid for old records
		h.db.Model(&model.VoiceParticipant{}).
			Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID).
			Updates(map[string]interface{}{
				"last_active_at": time.Now(),
				"livekit_sid":    event.Participant.Sid,
			})
	}

	// 广播完整成员列表（快照携带音效绑定 ID，客户端本地播放进出音；不再服务端广播音频）
	h.service.BroadcastParticipantsList(roomID)
}

func (h *Handler) handleParticipantLeft(event *WebhookEvent) {
	if event.Participant == nil || event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	userID := event.Participant.Identity
	sid := event.Participant.Sid
	if isVirtualIdentity(userID) {
		return // 机器人与 :share 虚身份离开不触发出场音效（虚身份从未写参与者表，也无需软删）
	}

	// 软删除：记录离开时间和原因
	// 优先使用 LiveKit Sid 精确匹配会话，避免旧会话的延迟 webhook 误删新会话的记录
	var room model.VoiceRoom
	if err := h.db.First(&room, "id = ?", roomID).Error; err == nil {
		now := time.Now().UTC()
		query := h.db.Model(&model.VoiceParticipant{})
		if sid != "" {
			query = query.Where("room_id = ? AND livekit_sid = ?", room.ID, sid)
		} else {
			query = query.Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID)
		}
		query.Updates(map[string]interface{}{
			"left_at":           &now,
			"disconnect_reason": "webhook_left",
		})
	}

	// 级联结束该用户在此房间的活跃屏幕共享（对齐 KOOK/Oopz/TeamSpeak：
	// 连接断开则共享终止，避免重进频道时被回填"仍在共享"的幽灵状态）。
	h.cleanupScreenShareOnLeave(userID, roomID, "participant_left")

	// 广播完整成员列表（快照携带音效绑定 ID，客户端本地播放进出音；不再服务端广播音频）
	h.service.BroadcastParticipantsList(roomID)
	// 额外定向推给离开者本人：其断连后可能已退订频道，收不到上面的房间广播，
	// 会让自己在本地成员列表里残留。对齐 KOOK/Discord：变化对所有相关在线用户可见。
	h.service.BroadcastParticipantsListToUser(userID, roomID)
}

// handleParticipantUpdated 处理参与者状态更新事件
func (h *Handler) handleParticipantUpdated(event *WebhookEvent) {
	if event.Participant == nil || event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	userID := event.Participant.Identity
	sid := event.Participant.Sid
	if isVirtualIdentity(userID) {
		return // 虚身份状态更新不落库（此前连 bot 都未过滤，一并收紧）
	}

	var room model.VoiceRoom
	if err := h.db.First(&room, "id = ?", roomID).Error; err == nil {
		// 刷新最后活跃时间；is_speaking 由客户端/track 事件维护
		query := h.db.Model(&model.VoiceParticipant{})
		if sid != "" {
			query = query.Where("room_id = ? AND livekit_sid = ?", room.ID, sid)
		} else {
			query = query.Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, userID)
		}
		query.Update("last_active_at", time.Now())
	}
}

func (h *Handler) handleTrackPublished(event *WebhookEvent) {
	if event.Track == nil || event.Participant == nil || event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	userID := event.Participant.Identity
	sid := event.Participant.Sid

	// Update screen share status if track is screen share
	if event.Track.Source == livekit.TrackSource_SCREEN_SHARE {
		// :share 虚身份发布的双轨，其共享状态必须归一化到真实用户行，
		// 否则观看端永远看不到"正在共享"（虚身份没有参与者行）。
		userID = normalizeIdentity(userID)
		var room model.VoiceRoom
		if err := h.db.First(&room, "id = ?", roomID).Error; err == nil {
			query := h.db.Model(&model.VoiceParticipant{})
			if sid != "" {
				query = query.Where("room_id = ? AND user_id = ? AND livekit_sid = ?", room.ID, userID, sid)
			} else {
				query = query.Where("room_id = ? AND user_id = ?", room.ID, userID)
			}
			query.Update("is_screen_sharing", true)
		}
	}
}

func (h *Handler) handleTrackUnpublished(event *WebhookEvent) {
	if event.Track == nil || event.Participant == nil || event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	userID := event.Participant.Identity
	sid := event.Participant.Sid

	if event.Track.Source == livekit.TrackSource_SCREEN_SHARE {
		userID = normalizeIdentity(userID)
		var room model.VoiceRoom
		if err := h.db.First(&room, "id = ?", roomID).Error; err == nil {
			query := h.db.Model(&model.VoiceParticipant{})
			if sid != "" {
				query = query.Where("room_id = ? AND user_id = ? AND livekit_sid = ?", room.ID, userID, sid)
			} else {
				query = query.Where("room_id = ? AND user_id = ?", room.ID, userID)
			}
			query.Update("is_screen_sharing", false)
		}
	}
}

func (h *Handler) handleTrackMuted(event *WebhookEvent, muted bool) {
	if event.Participant == nil || event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	userID := event.Participant.Identity
	sid := event.Participant.Sid
	if isVirtualIdentity(userID) {
		return // 虚身份的轨道静音事件不落库、不广播
	}

	var room model.VoiceRoom
	if err := h.db.First(&room, "id = ?", roomID).Error; err == nil {
		query := h.db.Model(&model.VoiceParticipant{})
		if sid != "" {
			query = query.Where("room_id = ? AND user_id = ? AND livekit_sid = ?", room.ID, userID, sid)
		} else {
			query = query.Where("room_id = ? AND user_id = ?", room.ID, userID)
		}
		query.Update("is_muted", muted)
	}

	// Broadcast mute state change
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "voice_state_change",
			"payload": map[string]interface{}{
				"room_id":    roomID,
				"channel_id": roomID, // backward compatibility
				"user_id":    userID,
				"event":      map[bool]string{true: "muted", false: "unmuted"}[muted],
			},
		})
		h.broadcastVoiceEvent(roomID, data)
	}
}

func (h *Handler) handleEgressStarted(event *WebhookEvent) {
	if event.EgressInfo == nil {
		return
	}
	// Egress started — no DB update needed (already created by StartRecording)
}

func (h *Handler) handleEgressEnded(event *WebhookEvent) {
	if event.EgressInfo == nil {
		return
	}
	// Update recording status to completed if egress finished successfully
	egressID := event.EgressInfo.EgressId
	h.db.Model(&model.VoiceRecording{}).
		Where("egress_id = ? AND status = ?", egressID, "recording").
		Updates(map[string]interface{}{
			"status":           "completed",
			"duration_seconds": int(event.EgressInfo.EndedAt - event.EgressInfo.StartedAt),
		})
}

func (h *Handler) handleRoomFinished(event *WebhookEvent) {
	if event.Room == nil {
		return
	}
	roomID := roomNameToRoomID(event.Room.Name)
	if roomID == "" {
		return
	}
	// 软删除：标记所有活跃参与者为已离开
	var room model.VoiceRoom
	if err := h.db.First(&room, "id = ?", roomID).Error; err == nil {
		now := time.Now().UTC()
		h.db.Model(&model.VoiceParticipant{}).
			Where("room_id = ? AND left_at IS NULL", room.ID).
			Updates(map[string]interface{}{
				"left_at":           &now,
				"disconnect_reason": "room_finished",
			})
	}
}

// roomNameToRoomID extracts room ID from LiveKit room name.
// LiveKit room name format: "rrt-room-{roomID}"
func roomNameToRoomID(roomName string) string {
	prefix := "rrt-room-"
	if len(roomName) > len(prefix) && roomName[:len(prefix)] == prefix {
		return roomName[len(prefix):]
	}
	return ""
}
