package server

import (
	"encoding/json"
	"time"

	"ridgericetalk/features/sharedoc"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/logger"
	"ridgericetalk/internal/metrics"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/user"
	"ridgericetalk/internal/voice"
)

// wireHubCallbacks attaches all realtime hub event handlers.
func wireHubCallbacks(hub *realtime.Hub, db *database.DB, userSvc *user.Service, voiceHandler *voice.Handler, sharedocHandler *sharedoc.Handler, log *logger.Logger) {
	hub.OnClientConnect = func(total int) {
		metrics.SetWSConnections(float64(total))
	}
	hub.OnClientDisconnect = func(total int) {
		metrics.SetWSConnections(float64(total))
	}
	hub.OnMessage = func(msgType string) {
		metrics.RecordWSMessage(msgType)
	}

	// Persist presence changes to database
	hub.OnPresenceUpdate = func(userID, status, customStatus string) error {
		if err := userSvc.UpdateStatus(userID, status, customStatus); err != nil {
			log.Warn("failed to persist presence", "user_id", userID, "error", err)
			return err
		}
		return nil
	}

	// Auto-mark user offline when all connections disconnect
	hub.OnUserFullyOffline = func(userID string) {
		if err := userSvc.UpdateStatus(userID, "offline", ""); err != nil {
			log.Warn("failed to set user offline", "user_id", userID, "error", err)
		}
		// Clean up any voice channel participation for this user.
		// This handles the case where the user closed the browser without
		// calling POST /api/voice/leave, leaving ghost records behind.
		var rooms []model.VoiceRoom
		if err := db.DB.Model(&model.VoiceRoom{}).Find(&rooms).Error; err == nil {
			for _, room := range rooms {
				if err := voiceHandler.CleanupParticipantForUser(userID, room.ID); err != nil {
					log.Warn("failed to cleanup voice participant", "user_id", userID, "room_id", room.ID, "error", err)
				}
			}
		} else {
			log.Warn("failed to list voice rooms on user offline", "user_id", userID, "error", err)
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "presence_update",
			"payload": map[string]interface{}{
				"userId":       userID,
				"status":       "offline",
				"customStatus": "",
			},
		})
		hub.Broadcast(data)
		if sharedocHandler != nil {
			sharedocHandler.OnUserFullyOffline(userID)
		}
		log.Info("user fully offline", "user_id", userID)
	}

	// Auto-mark user online when first WS connection is established.
	hub.OnUserCameOnline = func(userID string) {
		if err := userSvc.UpdateStatus(userID, "online", ""); err != nil {
			log.Warn("failed to set user online", "user_id", userID, "error", err)
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "presence_update",
			"payload": map[string]interface{}{
				"userId":       userID,
				"status":       "online",
				"customStatus": "",
			},
		})
		hub.Broadcast(data)
	}

	// WebSocket channel authorization: verify user has access to the channel
	// based on the channel's visibility policy (§6.5A).
	hub.AuthorizeChannel = func(userID, channelID string) bool {
		var channel model.Channel
		if err := db.DB.Select("id, visibility").First(&channel, "id = ?", channelID).Error; err != nil {
			return false // channel doesn't exist
		}

		// Look up the user's role (OWNER/ADMIN/MEMBER)
		var user model.User
		if err := db.DB.Select("id, role").First(&user, "id = ?", userID).Error; err != nil {
			return false
		}
		// OWNER always has access
		if user.Role == "OWNER" {
			return true
		}

		switch channel.Visibility {
		case "public", "":
			return true
		case "admin-only":
			return user.Role == "ADMIN"
		case "role-specific":
			var perm model.ChannelRolePermission
			if err := db.DB.Where("channel_id = ? AND role = ?", channelID, user.Role).First(&perm).Error; err != nil {
				return false
			}
			return perm.CanView
		default:
			return false
		}
	}

	// M11: 白板订阅授权——白板笔迹是定向广播，订阅前必须确认请求者对
	// 白板所属空间有成员权限。全局 OWNER（部署所有者）沿用上方
	// AuthorizeChannel 的放行先例；白板不存在或任一查询失败一律拒绝
	// （fail-closed：hub 侧回调未装配时同样拒绝，见 hub.go）。
	hub.AuthorizeWhiteboard = func(userID, whiteboardID string) bool {
		var wb model.Whiteboard
		if err := db.DB.Select("id, space_id").First(&wb, "id = ?", whiteboardID).Error; err != nil {
			return false // whiteboard doesn't exist
		}
		var u model.User
		if err := db.DB.Select("id, role").First(&u, "id = ?", userID).Error; err != nil {
			return false
		}
		if u.Role == "OWNER" {
			return true
		}
		var count int64
		if err := db.DB.Model(&model.Membership{}).
			Where("space_id = ? AND user_id = ?", wb.SpaceID, userID).
			Count(&count).Error; err != nil {
			return false
		}
		return count > 0
	}

	// H16: 共享文档订阅授权——文档实时流（doc_op / doc_editor_joined/left /
	// doc_cursor / doc_selection）按文档原始 id（"doc_<snowflake>"）定向广播，
	// 订阅键即文档 id 本身，与 channels 表无关。订阅与事件转发前必须确认
	// 请求者对文档所属空间有成员权限；全局 OWNER 沿用 AuthorizeChannel /
	// AuthorizeWhiteboard 的放行先例（空间管理员本身是成员，经 Membership
	// 放行）。文档不存在或任一查询失败一律拒绝（fail-closed：hub 侧回调
	// 未装配时同样拒绝，见 hub.go AuthorizeDoc）。canEditDoc 的
	// owner/admin 编辑权限面不受影响——这里只管「实时流的可见性」，
	// 与 canAccessDoc（空间成员可读）同口径。
	hub.AuthorizeDoc = func(userID, docID string) bool {
		var doc model.SharedDocument
		if err := db.DB.Select("id, space_id").First(&doc, "id = ?", docID).Error; err != nil {
			return false // document doesn't exist
		}
		var u model.User
		if err := db.DB.Select("id, role").First(&u, "id = ?", userID).Error; err != nil {
			return false
		}
		if u.Role == "OWNER" {
			return true
		}
		var count int64
		if err := db.DB.Model(&model.Membership{}).
			Where("space_id = ? AND user_id = ?", doc.SpaceID, userID).
			Count(&count).Error; err != nil {
			return false
		}
		return count > 0
	}

	// H18: Build sync_snapshot payload when a client subscribes to a channel.
	// Includes recent messages, pinned message ids (L8) and unread count so
	// the client can render initial state without extra HTTP requests.
	hub.OnSubscribeSync = func(userID, channelID string) map[string]interface{} {
		snapshot := map[string]interface{}{}

		// Recent messages (latest 20, oldest first for rendering)
		var recent []model.Message
		if err := db.DB.Where("channel_id = ?", channelID).
			Order("created_at DESC").
			Limit(20).
			Find(&recent).Error; err == nil {
			// Reverse to chronological order
			for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
				recent[i], recent[j] = recent[j], recent[i]
			}
			snapshot["recentMessages"] = recent
		}

		// L8: pinned_message_ids — channel stores a single pinned message id
		var pinnedIDs []string
		var ch model.Channel
		if err := db.DB.Select("pinned_message_id").First(&ch, "id = ?", channelID).Error; err == nil {
			if ch.PinnedMessageID != nil && *ch.PinnedMessageID != "" {
				pinnedIDs = append(pinnedIDs, *ch.PinnedMessageID)
			}
		}
		snapshot["pinnedMessageIds"] = pinnedIDs

		// Unread count: messages in channel not authored by this user after
		// their last read position. Falls back to total count if no read record.
		var lastRead model.UserChannelRead
		if err := db.DB.Where("user_id = ? AND channel_id = ?", userID, channelID).
			First(&lastRead).Error; err == nil {
			var unread int64
			db.DB.Model(&model.Message{}).
				Where("channel_id = ? AND user_id != ? AND created_at > ?",
					channelID, userID, lastRead.ReadAt).
				Count(&unread)
			snapshot["unreadCount"] = unread
		} else {
			var total int64
			db.DB.Model(&model.Message{}).
				Where("channel_id = ? AND user_id != ?", channelID, userID).
				Count(&total)
			snapshot["unreadCount"] = total
		}

		// M20: Include active screenshare state so new subscribers can show visual hint.
		// Screen share sessions are now space-scoped; only bound channels receive the hint.
		// 列名是 channel_id（000008 迁移把 bind_channel_id 改名回来了）。
		var ss model.ScreenShareSession
		if err := db.DB.Where("channel_id = ? AND active = ?", channelID, true).First(&ss).Error; err == nil {
			var sharer model.User
			db.DB.Select("username").First(&sharer, "id = ?", ss.UserID)
			snapshot["screenshareState"] = map[string]interface{}{
				"active":    true,
				"userId":    ss.UserID,
				"username":  sharer.Username,
				"shareType": ss.ShareType,
				"startedAt": ss.StartedAt.Format(time.RFC3339),
			}
		} else {
			snapshot["screenshareState"] = map[string]interface{}{"active": false}
		}

		return snapshot
	}
}
