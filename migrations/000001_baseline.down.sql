-- ============================================================
-- RidgeRiceTalk - 数据库回滚脚本
-- 版本: 001_init (down)
-- 说明: 按反向顺序删除所有表，与 001_init.up.sql 对应
-- 数据库: PostgreSQL 14+
-- ============================================================

-- ════════════════════════════════════════════════════════════
-- 按创建顺序逆序删除所有表（共 44 张）
-- ════════════════════════════════════════════════════════════

-- H36 补全的 3 张表（最后创建，最先删除）
DROP TABLE IF EXISTS bots CASCADE;
DROP TABLE IF EXISTS password_history CASCADE;
DROP TABLE IF EXISTS shared_document_versions CASCADE;

-- 管理与审计
DROP TABLE IF EXISTS security_audit_logs CASCADE;
DROP TABLE IF EXISTS screen_share_sessions CASCADE;
DROP TABLE IF EXISTS module_runtime_statuses CASCADE;
DROP TABLE IF EXISTS audit_logs CASCADE;
DROP TABLE IF EXISTS admin_configs CASCADE;

-- 虚拟网络
DROP TABLE IF EXISTS virtual_net_nodes CASCADE;
DROP TABLE IF EXISTS virtual_net_sessions CASCADE;

-- 日程与小游戏
DROP TABLE IF EXISTS minigame_sessions CASCADE;
DROP TABLE IF EXISTS schedule_events CASCADE;

-- 文件与共享
DROP TABLE IF EXISTS shared_documents CASCADE;
DROP TABLE IF EXISTS shared_file_entries CASCADE;
DROP TABLE IF EXISTS shared_folders CASCADE;
DROP TABLE IF EXISTS file_metadata CASCADE;

-- 白板
DROP TABLE IF EXISTS whiteboard_strokes CASCADE;

-- 机器人相关
DROP TABLE IF EXISTS netease_auths CASCADE;
DROP TABLE IF EXISTS tts_consents CASCADE;
DROP TABLE IF EXISTS bot_tts_messages CASCADE;
DROP TABLE IF EXISTS bot_player_states CASCADE;
DROP TABLE IF EXISTS bot_upload_audios CASCADE;
DROP TABLE IF EXISTS bot_play_queues CASCADE;

-- 语音相关
DROP TABLE IF EXISTS voice_recordings CASCADE;
DROP TABLE IF EXISTS voice_participants CASCADE;
DROP TABLE IF EXISTS voice_rooms CASCADE;

-- OG 缓存
DROP TABLE IF EXISTS og_caches CASCADE;

-- 在线状态与已读跟踪
DROP TABLE IF EXISTS channel_role_permissions CASCADE;
DROP TABLE IF EXISTS message_acks CASCADE;
DROP TABLE IF EXISTS user_channel_reads CASCADE;
DROP TABLE IF EXISTS user_presences CASCADE;

-- 会话与认证
DROP TABLE IF EXISTS admin_bootstrap_tokens CASCADE;
DROP TABLE IF EXISTS e2ee_keys CASCADE;
DROP TABLE IF EXISTS password_reset_tokens CASCADE;
DROP TABLE IF EXISTS totp_recovery_codes CASCADE;
DROP TABLE IF EXISTS mfa_secrets CASCADE;
DROP TABLE IF EXISTS user_sessions CASCADE;

-- 频道与消息
DROP TABLE IF EXISTS reactions CASCADE;
DROP TABLE IF EXISTS messages CASCADE;
DROP TABLE IF EXISTS dm_channels CASCADE;
DROP TABLE IF EXISTS channels CASCADE;

-- 用户与空间
DROP TABLE IF EXISTS memberships CASCADE;
DROP TABLE IF EXISTS spaces CASCADE;
DROP TABLE IF EXISTS users CASCADE;

-- ════════════════════════════════════════════════════════════
-- 回滚完成
-- ════════════════════════════════════════════════════════════
