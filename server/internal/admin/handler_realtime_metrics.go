package admin

import (
	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
)

// RealtimeRoomStat 是单个语音房间的实时人数统计（M2 观测性）。
type RealtimeRoomStat struct {
	RoomID           string `json:"roomId"`
	ParticipantCount int    `json:"participantCount"`
}

// RealtimeMetrics 是 GET /api/admin/metrics/realtime 的响应体。字段命名与
// admin API 既有风格一致（camelCase）：
//   - rooms:               语音房间逐房人数（源自 voice_participants，left_at IS NULL）
//   - onlineUsers:         在线用户数（hub 内存态，去重用户）
//   - activeScreenshares:  进行中的屏幕共享数（screenshare_sessions.active = true）
//   - voiceRooms:          语音房间数（= len(rooms)，冗余字段便于前端直读）
type RealtimeMetrics struct {
	Rooms              []RealtimeRoomStat `json:"rooms"`
	OnlineUsers        int                `json:"onlineUsers"`
	ActiveScreenshares int                `json:"activeScreenshares"`
	VoiceRooms         int                `json:"voiceRooms"`
}

// GetRealtimeMetrics 聚合实时房间/在线/屏幕共享指标。数据源：
//   - hub.OnlineUserIDs()：内存态读锁快照（不新增锁，也不久持）；
//   - voice_participants / screenshare_sessions：两条只读聚合查询，与
//     voice/screenshare 各自的「活跃」口径一致（left_at IS NULL / active=true）。
//
// 轮询节奏为 admin 前端 5s 一拍，两条聚合都走索引列（left_at 无索引但表以
// 活跃行为主、清理任务定期硬删历史行；active 有索引），单次查询代价可控。
func (s *Service) GetRealtimeMetrics() (*RealtimeMetrics, error) {
	metrics := &RealtimeMetrics{Rooms: []RealtimeRoomStat{}}

	// 在线用户：hub 内存态，nil hub（部分测试/降级路径）时保持 0。
	if s.hub != nil {
		metrics.OnlineUsers = len(s.hub.OnlineUserIDs())
	}

	// 语音房间逐房人数（软删口径：left_at IS NULL 为在房成员）。
	type roomRow struct {
		RoomID           string `gorm:"column:room_id"`
		ParticipantCount int    `gorm:"column:participant_count"`
	}
	var rows []roomRow
	if err := s.db.Model(&model.VoiceParticipant{}).
		Select("room_id, COUNT(*) as participant_count").
		Where("left_at IS NULL").
		Group("room_id").
		Scan(&rows).Error; err != nil {
		return nil, errors.ErrInternal
	}
	for _, r := range rows {
		metrics.Rooms = append(metrics.Rooms, RealtimeRoomStat{
			RoomID:           r.RoomID,
			ParticipantCount: r.ParticipantCount,
		})
	}
	metrics.VoiceRooms = len(metrics.Rooms)

	// 进行中屏幕共享（与 screenshare 仓储 GetActiveByChannelID 同口径）。
	var shareCount int64
	if err := s.db.Model(&model.ScreenShareSession{}).
		Where("active = ?", true).
		Count(&shareCount).Error; err != nil {
		return nil, errors.ErrInternal
	}
	metrics.ActiveScreenshares = int(shareCount)

	return metrics, nil
}

// GetRealtimeMetrics handles GET /api/admin/metrics/realtime.
func (h *Handler) GetRealtimeMetrics(c *gin.Context) {
	metrics, err := h.service.GetRealtimeMetrics()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, metrics)
}
