package automigrate

import (
	"ridgericetalk/internal/model"
)

// AllModels 返回生产 AutoMigrate 使用的全部持久化模型清单（**唯一真源**）。
//
// SQLite 部署建表、cmd/migrate 兜底与测试库（server/tests/testutil）都必须复用它，
// 禁止各自维护一份清单：历史上测试库与生产清单各写一份，已经出现多次漂移
// （MessageAttachment 漏注册 → SQLite 部署下附件消息 500；MinigameHistory 缺表 →
// 小游戏历史静默写失败），由 task-69 收敛到本函数，并由
// internal/database/automigrate_coverage_test.go 做双向守卫。
//
// 依赖方向：automigrate 只依赖 internal/model（叶子包），因此 internal/database
// 与 tests/testutil 都可以安全导入，不会形成测试期导入环。
func AllModels() []interface{} {
	return []interface{}{
		&model.User{},
		&model.Space{},
		&model.Membership{},
		&model.Channel{},
		&model.DMChannel{},
		&model.Message{},
		&model.MessageAttachment{}, // P1：漏加 = SQLite 部署下所有附件/图片消息 500（no such table: message_attachments）
		&model.Reaction{},
		&model.UserSession{},
		&model.AdminBootstrapToken{},
		&model.UserPresence{},
		&model.UserChannelRead{},
		&model.MessageThreadRead{}, // A9 二期：线程级已读位（DES-20261029-01 §五；漏加=SQLite 生产零建表）
		&model.Notification{},      // 1.9.4 task-118：通知中心落库（SQLite 开发环境建表；生产走 000047 迁移，表名必须=GORM 名 notifications）
		&model.ChannelRolePermission{},
		&model.OGCache{},
		&model.VoiceRoom{},
		&model.VoiceParticipant{},
		&model.BotPlayQueue{},
		&model.BotPlayerState{},
		&model.BotUploadAudio{},
		&model.BotTTSMessage{},
		&model.TTSConsent{},
		&model.Whiteboard{},
		&model.WhiteboardStroke{},
		&model.FileMetadata{},
		&model.SharedFolder{},
		&model.SharedFileEntry{},
		&model.SharedDocument{},
		&model.SharedDocumentVersion{},      // M25: 文档版本历史
		&model.SharedDocumentComment{},      // Phase 2: 文档评论
		&model.SharedDocumentCollaborator{}, // 1.9.4 A 方案：共享文档协作者（SQLite 开发环境建表；生产走 000045 迁移）
		&model.ScheduleEvent{},
		&model.ScheduleEventInvite{}, // A8-S1: 日程事件邀请 RSVP（SQLite 开发环境建表；生产走 000039 迁移）
		&model.MinigameSession{},
		&model.MinigameHistory{},     // §15.4: 游戏历史记录
		&model.MinigameLeaderboard{}, // §15.7.4: 单机游戏排行榜（此前漏迁移，导致提交/查询报 no such table）
		&model.VirtualNetSession{},
		&model.VirtualNetNode{},
		&model.AdminConfig{},
		&model.AuditLog{},
		&model.ModuleRuntimeStatus{},
		&model.ScreenShareSession{},
		&model.MessageAck{},
		&model.SecurityAuditLog{},
		&model.PasswordHistory{},
		&model.SecurityQuestion{},
		&model.PasswordResetToken{},
		&model.VoiceRecording{},
		&model.E2EEKey{},
		&model.NeteaseAuth{},
		&model.Bot{},
		&model.RemoteAssistSession{}, // T49: 远程协助会话
		&model.UserSound{},           // 出入频道音效库
		&model.UserSoundSettings{},   // 个人入场/出场音效绑定
		&model.UserSoundFavorite{},   // 音效收藏
		&model.CloudFileShare{},      // DES-2026-0912-05: 云文件分享链接（SQLite 开发环境建表；生产走 000035 迁移）
		&model.UserChannelMute{},     // T4/S-1: 频道级静音（SQLite 开发环境建表；生产走 000036 迁移）
		&model.AdminAlert{},          // DES-20261001-01 §12: 管理后台监控告警（SQLite 开发环境建表；生产走 000040 迁移）
		&model.ServerEmoji{},         // DES-20261002-01 §4.7: B7 自定义服务器表情（SQLite 开发环境建表；生产走 000041 迁移）
	}
}
