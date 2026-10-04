package voice

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
)

// endActiveScreenShares 级联结束某用户在指定房间内的活跃屏幕共享会话，并广播停止事件。
//
// 设计语义（对齐 KOOK / Oopz / TeamSpeak）：屏幕共享绑定在语音连接上，连接断开
// （主动退出 / 意外掉线 / 被踢 / 进程崩溃）时共享必须随之终止，否则共享者重进频道时
// 会被错误地回填"仍在共享"的状态。
//
// 背景：ScreenShareSession.active 是持久化状态，此前只有显式 Stop 才清理；LiveKit
// participant_left / LeaveVoice / KickParticipant 等离开路径都只更新 VoiceParticipant，
// 不级联共享，导致残留 active=true 的"幽灵共享"。
//
// 幂等：仅影响 active=true 的会话，无活跃共享时为 0 行更新，安全重复调用。
// kicker 用于踢出该用户的 :share 虚身份 LiveKit 连接，可为 nil（测试/无 LiveKit）。
// 返回被结束的会话数。hub 可为 nil（测试或不广播场景）。
func endActiveScreenShares(db *gorm.DB, hub *realtime.Hub, userID, roomID, reason string, kicker func(userID string)) int {
	if db == nil || userID == "" || roomID == "" {
		return 0
	}
	now := time.Now().UTC()

	// 先查出将被结束的会话（用于广播 userId 等信息）
	var sessions []model.ScreenShareSession
	db.Where("channel_id = ? AND user_id = ? AND active = ?", roomID, userID, true).
		Find(&sessions)
	if len(sessions) == 0 {
		return 0
	}

	result := db.Model(&model.ScreenShareSession{}).
		Where("channel_id = ? AND user_id = ? AND active = ?", roomID, userID, true).
		Updates(map[string]interface{}{
			"active":       false,
			"status":       "ended",
			"ended_at":     &now,
			"ended_reason": reason,
		})
	if result.Error != nil {
		return 0
	}

	// 广播 screenshare_state(active=false) 给频道订阅者，让频道内其他成员
	// 的观看端/预览卡同步关闭（与 screenshare handler 的正常 stop 广播格式一致）。
	if hub != nil {
		for _, sess := range sessions {
			payload := map[string]interface{}{
				"active":        false,
				"channelId":     roomID,
				"userId":        sess.UserID,
				"username":      "",
				"shareType":     sess.ShareType,
				"sourceId":      sess.SourceID,
				"resolution":    sess.Resolution,
				"frameRate":     sess.FrameRate,
				"maxBitrate":    sess.MaxBitrate,
				"shareAudio":    sess.ShareAudio,
				"suppressVoice": sess.SuppressVoice,
				"viewerCount":   0,
				"maxViewers":    sess.MaxViewers,
				"status":        "ended",
			}
			data, _ := json.Marshal(map[string]interface{}{
				"type":    "screenshare_state",
				"payload": payload,
			})
			hub.BroadcastToChannel(roomID, data)
		}
	}

	// 踢出该用户的 :share 虚身份连接（best effort）：级联结束共享时，原生模块
	// 的独立 LiveKit 连接必须一并断开，否则虚身份悬挂在房间里。
	if kicker != nil {
		kicker(userID)
	}

	return int(result.RowsAffected)
}

// cleanupScreenShareOnLeave 是 Handler 侧的便捷包装（webhook participant_left 用）。
// 踢出 :share 虚身份复用 Service.shareVirtualKicker（同一实现，避免两处发散）。
func (h *Handler) cleanupScreenShareOnLeave(userID, roomID, reason string) int {
	var kicker func(userID string)
	if h.service != nil {
		kicker = h.service.shareVirtualKicker(roomID)
	}
	return endActiveScreenShares(h.db, h.hub, userID, roomID, reason, kicker)
}
