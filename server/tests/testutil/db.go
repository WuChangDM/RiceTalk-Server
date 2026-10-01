package testutil

import (
	"fmt"
	"sync/atomic"

	"ridgericetalk/internal/logger"
	"ridgericetalk/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var testDBCounter int64

// SetupTestDB creates an isolated in-memory SQLite database with all tables migrated.
func SetupTestDB() (*gorm.DB, error) {
	id := atomic.AddInt64(&testDBCounter, 1)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:memdb%d?mode=memory", id)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open test db: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&model.User{},
		&model.Space{},
		&model.Membership{},
		&model.Channel{},
		&model.DMChannel{},
		&model.Message{},
		&model.Reaction{},
		&model.UserSession{},
		&model.AdminBootstrapToken{},
		&model.UserPresence{},
		&model.UserChannelRead{},
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
		&model.SharedDocumentVersion{}, // M25: 文档版本历史
		&model.SharedDocumentComment{}, // Phase 2: 文档评论
		&model.ScheduleEvent{},
		&model.ScheduleEventInvite{}, // A8-S1: 日程事件邀请 RSVP
		&model.MinigameSession{},
		&model.MinigameLeaderboard{},
		&model.VirtualNetSession{},
		&model.VirtualNetNode{},
		&model.AdminConfig{},
		&model.AuditLog{},
		&model.SecurityAuditLog{},
		&model.ModuleRuntimeStatus{},
		&model.ScreenShareSession{},
		&model.MessageAck{},
		&model.SecurityQuestion{},
		&model.PasswordResetToken{},
		&model.VoiceRecording{},
		&model.E2EEKey{},
		&model.NeteaseAuth{},
		&model.Bot{},
		&model.RemoteAssistSession{}, // T49: 远程协助会话
		&model.MessageAttachment{},
		&model.UserSound{},          // 出入频道音效库
		&model.UserSoundSettings{},  // 个人入场/出场音效绑定
		&model.UserSoundFavorite{},  // 音效收藏
		&model.CloudFileShare{},     // DES-2026-0912-05: 云文件分享链接
		&model.UserChannelMute{},    // T4/S-1: 频道级静音
		&model.AdminAlert{},         // DES-20261001-01 §12: 管理后台监控告警
	); err != nil {
		return nil, fmt.Errorf("migrate test db: %w", err)
	}

	return db, nil
}

// MustSetupTestDB panics if SetupTestDB fails.
func MustSetupTestDB() *gorm.DB {
	db, err := SetupTestDB()
	if err != nil {
		panic(err)
	}
	return db
}

// TestLogger returns a silent logger suitable for tests.
func TestLogger() *logger.Logger {
	log, _ := logger.New("fatal")
	return log
}
