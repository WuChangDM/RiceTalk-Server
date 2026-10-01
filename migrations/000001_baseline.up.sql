-- ============================================================
-- RidgeRiceTalk - 数据库初始迁移脚本
-- 版本: 001_init
-- 说明: 与 server/internal/model/model.go 中的 GORM 模型完全对齐
-- 数据库: PostgreSQL 14+
-- 生成规则: 以 model.go 为准，GORM 默认命名约定
-- ============================================================

-- ════════════════════════════════════════════════════════════
-- 用户与空间
-- ════════════════════════════════════════════════════════════

-- ── 用户表 (User → users) ──
CREATE TABLE IF NOT EXISTS users (
    id              VARCHAR(64) PRIMARY KEY,
    username        VARCHAR(32) NOT NULL,
    email           VARCHAR(255) NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    avatar          VARCHAR(512),
    display_name    VARCHAR(64),
    custom_status   VARCHAR(128),
    role            VARCHAR(16) DEFAULT 'MEMBER',
    is_active       BOOLEAN DEFAULT TRUE,
    token_version   BIGINT DEFAULT 0,
    email_verified  BOOLEAN DEFAULT FALSE,
    mfa_enabled     BOOLEAN DEFAULT FALSE,
    tts_consent     BOOLEAN DEFAULT FALSE,
    theme           VARCHAR(16) DEFAULT 'dark',
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users (email);
CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users (deleted_at);

-- ── 空间表 (Space → spaces) ──
CREATE TABLE IF NOT EXISTS spaces (
    id          VARCHAR(64) PRIMARY KEY,
    name        VARCHAR(64) NOT NULL,
    icon        VARCHAR(512),
    owner_id    VARCHAR(64) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_spaces_owner_id ON spaces (owner_id);
CREATE INDEX IF NOT EXISTS idx_spaces_deleted_at ON spaces (deleted_at);

-- ── 成员关系表 (Membership → memberships) ──
CREATE TABLE IF NOT EXISTS memberships (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    space_id    VARCHAR(64) NOT NULL,
    role        VARCHAR(16) DEFAULT 'MEMBER',
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_membership_space_user ON memberships (user_id, space_id);

-- ════════════════════════════════════════════════════════════
-- 频道与消息
-- ════════════════════════════════════════════════════════════

-- ── 频道表 (Channel → channels) ──
CREATE TABLE IF NOT EXISTS channels (
    id                  VARCHAR(64) PRIMARY KEY,
    space_id            VARCHAR(64) NOT NULL,
    name                VARCHAR(64) NOT NULL,
    type                VARCHAR(16) NOT NULL,
    visibility          VARCHAR(20) DEFAULT 'public',
    voice_quality       VARCHAR(16) DEFAULT 'standard',
    pinned_message_id   VARCHAR(64),
    position            BIGINT DEFAULT 0,
    sort_group          VARCHAR(32) DEFAULT '',
    permissions         TEXT DEFAULT '',
    created_by          VARCHAR(64),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_channels_space_id ON channels (space_id);
CREATE INDEX IF NOT EXISTS idx_channel_space_sort ON channels (space_id, position);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_space_name_type ON channels (space_id, name, type);
CREATE INDEX IF NOT EXISTS idx_channels_deleted_at ON channels (deleted_at);

-- ── DM 频道表 (DMChannel → dm_channels) ──
CREATE TABLE IF NOT EXISTS dm_channels (
    id          VARCHAR(64) PRIMARY KEY,
    channel_id  VARCHAR(64) NOT NULL,
    user_aid    VARCHAR(64) NOT NULL,
    user_bid    VARCHAR(64) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_dm_channel ON dm_channels (channel_id, user_aid, user_bid);
CREATE INDEX IF NOT EXISTS idx_dm_user_a ON dm_channels (user_aid);
CREATE INDEX IF NOT EXISTS idx_dm_user_b ON dm_channels (user_bid);

-- ── 消息表 (Message → messages) ──
CREATE TABLE IF NOT EXISTS messages (
    id                    VARCHAR(64) PRIMARY KEY,
    channel_id            VARCHAR(64) NOT NULL,
    user_id               VARCHAR(64) NOT NULL,
    author_username       VARCHAR(32) DEFAULT '',
    author_display_name   VARCHAR(64) DEFAULT '',
    author_avatar_url     VARCHAR(512) DEFAULT '',
    author_role           VARCHAR(16) DEFAULT 'MEMBER',
    content               TEXT NOT NULL,
    type                  VARCHAR(20) DEFAULT 'text',
    parent_id             VARCHAR(64),
    client_message_id     VARCHAR(64),
    edited_at             TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at            TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_messages_channel_id ON messages (channel_id);
CREATE INDEX IF NOT EXISTS idx_messages_user_id ON messages (user_id);
CREATE INDEX IF NOT EXISTS idx_messages_parent_id ON messages (parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_msg_client_channel ON messages (channel_id, client_message_id);
CREATE INDEX IF NOT EXISTS idx_messages_deleted_at ON messages (deleted_at);

-- ── 表情回应表 (Reaction → reactions) ──
CREATE TABLE IF NOT EXISTS reactions (
    id          VARCHAR(64) PRIMARY KEY,
    message_id  VARCHAR(64) NOT NULL,
    user_id     VARCHAR(64) NOT NULL,
    emoji       VARCHAR(32) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_reaction_user_msg ON reactions (message_id, user_id, emoji);

-- ════════════════════════════════════════════════════════════
-- 会话与认证
-- ════════════════════════════════════════════════════════════

-- ── 用户会话表 (UserSession → user_sessions) ──
CREATE TABLE IF NOT EXISTS user_sessions (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    token_hash  VARCHAR(255) NOT NULL,
    is_admin_session BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_sessions_token_hash ON user_sessions (token_hash);

-- ── MFA 密钥表 (MFASecret → mfa_secrets) ──
CREATE TABLE IF NOT EXISTS mfa_secrets (
    id               VARCHAR(64) PRIMARY KEY,
    user_id          VARCHAR(64) NOT NULL,
    secret           VARCHAR(64) NOT NULL,
    enabled          BOOLEAN DEFAULT FALSE,
    login_challenge  VARCHAR(255),
    failed_attempts  BIGINT DEFAULT 0,
    locked_until     TIMESTAMPTZ,
    last_failed_at   TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mfa_secrets_user_id ON mfa_secrets (user_id);

-- ── TOTP 恢复码表 (TOTPRecoveryCode → totp_recovery_codes) ──
CREATE TABLE IF NOT EXISTS totp_recovery_codes (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    code_hash   VARCHAR(255) NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_totp_recovery_codes_user_id ON totp_recovery_codes (user_id);

-- ── 密码重置令牌表 (PasswordResetToken → password_reset_tokens) ──
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id                   VARCHAR(64) PRIMARY KEY,
    user_id              VARCHAR(64) NOT NULL,
    token                VARCHAR(255) NOT NULL,
    token_hash           VARCHAR(64) NOT NULL,
    email_code           VARCHAR(64),
    email_code_used      BOOLEAN DEFAULT FALSE,
    email_code_used_at   TIMESTAMPTZ,
    login_challenge      VARCHAR(255),
    expires_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used                 BOOLEAN DEFAULT FALSE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user_id ON password_reset_tokens (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_password_reset_tokens_token ON password_reset_tokens (token);
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_token_hash ON password_reset_tokens (token_hash);

-- ── E2EE 密钥表 (E2EEKey → e2ee_keys) ──
CREATE TABLE IF NOT EXISTS e2ee_keys (
    id          VARCHAR(64) PRIMARY KEY,
    channel_id  VARCHAR(64) NOT NULL,
    key_bytes   VARCHAR(512) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_e2ee_keys_channel_id ON e2ee_keys (channel_id);

-- ── 管理员引导令牌表 (AdminBootstrapToken → admin_bootstrap_tokens) ──
CREATE TABLE IF NOT EXISTS admin_bootstrap_tokens (
    id           VARCHAR(64) PRIMARY KEY,
    token_hash   VARCHAR(255) NOT NULL,
    expires_at   TIMESTAMPTZ,
    used         BOOLEAN DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at      TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_bootstrap_tokens_token_hash ON admin_bootstrap_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_admin_bootstrap_tokens_expires_at ON admin_bootstrap_tokens (expires_at);

-- ════════════════════════════════════════════════════════════
-- 在线状态与已读跟踪
-- ════════════════════════════════════════════════════════════

-- ── 用户在线状态表 (UserPresence → user_presences) ──
CREATE TABLE IF NOT EXISTS user_presences (
    id            VARCHAR(64) PRIMARY KEY,
    user_id       VARCHAR(64) NOT NULL,
    status        VARCHAR(16) DEFAULT 'offline',
    custom_status VARCHAR(128),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_presences_user_id ON user_presences (user_id);

-- ── 用户频道已读表 (UserChannelRead → user_channel_reads) ──
CREATE TABLE IF NOT EXISTS user_channel_reads (
    id                   VARCHAR(64) PRIMARY KEY,
    user_id              VARCHAR(64) NOT NULL,
    channel_id           VARCHAR(64) NOT NULL,
    message_id           VARCHAR(64),
    last_read_message_id VARCHAR(64),
    unread_count         BIGINT DEFAULT 0,
    mention_count        BIGINT DEFAULT 0,
    read_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_channel ON user_channel_reads (user_id, channel_id);

-- ── 消息已读确认表 (MessageAck → message_acks) ──
CREATE TABLE IF NOT EXISTS message_acks (
    id          VARCHAR(64) PRIMARY KEY,
    message_id  VARCHAR(64) NOT NULL,
    user_id     VARCHAR(64) NOT NULL,
    channel_id  VARCHAR(64) NOT NULL,
    acked_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_msg_ack_user_msg ON message_acks (message_id, user_id);

-- ── 频道角色权限表 (ChannelRolePermission → channel_role_permissions) ──
CREATE TABLE IF NOT EXISTS channel_role_permissions (
    id          VARCHAR(64) PRIMARY KEY,
    channel_id  VARCHAR(64) NOT NULL,
    role        VARCHAR(16) NOT NULL,
    can_view    BOOLEAN DEFAULT TRUE,
    can_write   BOOLEAN DEFAULT TRUE,
    can_manage  BOOLEAN DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_role_perm ON channel_role_permissions (channel_id, role);

-- ════════════════════════════════════════════════════════════
-- OG 缓存
-- ════════════════════════════════════════════════════════════

-- ── OG 缓存表 (OGCache → og_caches) ──
CREATE TABLE IF NOT EXISTS og_caches (
    id           VARCHAR(64) PRIMARY KEY,
    url          VARCHAR(1024) NOT NULL,
    title        VARCHAR(512),
    description  VARCHAR(2048),
    image        VARCHAR(2048),
    expires_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_og_caches_url ON og_caches (url);

-- ════════════════════════════════════════════════════════════
-- 语音相关
-- ════════════════════════════════════════════════════════════

-- ── 语音房间表 (VoiceRoom → voice_rooms) ──
CREATE TABLE IF NOT EXISTS voice_rooms (
    id            VARCHAR(64) PRIMARY KEY,
    channel_id    VARCHAR(64) NOT NULL,
    live_kit_room VARCHAR(128),
    e2ee_enabled  BOOLEAN DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_voice_rooms_channel_id ON voice_rooms (channel_id);

-- ── 语音参与者表 (VoiceParticipant → voice_participants) ──
CREATE TABLE IF NOT EXISTS voice_participants (
    id                VARCHAR(64) PRIMARY KEY,
    room_id           VARCHAR(64) NOT NULL,
    user_id           VARCHAR(64) NOT NULL,
    is_muted          BOOLEAN DEFAULT FALSE,
    is_speaking       BOOLEAN DEFAULT FALSE,
    is_screen_sharing BOOLEAN DEFAULT FALSE,
    speaker_muted     BOOLEAN DEFAULT FALSE,
    last_latency_ms   BIGINT DEFAULT 0,
    joined_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at           TIMESTAMPTZ,
    disconnect_reason VARCHAR(32)
);

CREATE INDEX IF NOT EXISTS idx_voice_participant_room_user ON voice_participants (room_id, user_id);

-- ── 语音录制表 (VoiceRecording → voice_recordings) ──
CREATE TABLE IF NOT EXISTS voice_recordings (
    id                   VARCHAR(64) PRIMARY KEY,
    channel_id           VARCHAR(64) NOT NULL,
    started_by           VARCHAR(64) NOT NULL,
    started_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stopped_at           TIMESTAMPTZ,
    file_url             VARCHAR(512),
    file_size_mb         DOUBLE PRECISION,
    duration_seconds     BIGINT,
    status               VARCHAR(16) DEFAULT 'recording',
    include_screen_share BOOLEAN DEFAULT FALSE,
    egress_id            VARCHAR(64),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_voice_recordings_channel_id ON voice_recordings (channel_id);

-- ════════════════════════════════════════════════════════════
-- 机器人相关
-- ════════════════════════════════════════════════════════════

-- ── 机器人播放队列表 (BotPlayQueue → bot_play_queues) ──
CREATE TABLE IF NOT EXISTS bot_play_queues (
    id          VARCHAR(64) PRIMARY KEY,
    channel_id  VARCHAR(64) NOT NULL,
    track_id    VARCHAR(128),
    title       VARCHAR(256),
    artist      VARCHAR(128),
    duration    BIGINT,
    cover       VARCHAR(512),
    album       VARCHAR(128),
    source      VARCHAR(16) DEFAULT 'netease',
    priority    BIGINT DEFAULT 0,
    position    BIGINT DEFAULT 0,
    volume      BIGINT DEFAULT 80,
    status      VARCHAR(16) DEFAULT 'queued',
    added_by    VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bot_play_queues_channel_id ON bot_play_queues (channel_id);

-- ── 机器人上传音频表 (BotUploadAudio → bot_upload_audios) ──
CREATE TABLE IF NOT EXISTS bot_upload_audios (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    title       VARCHAR(256),
    artist      VARCHAR(128),
    duration    BIGINT,
    file_path   VARCHAR(512),
    file_size   BIGINT,
    mime_type   VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_bot_upload_audios_deleted_at ON bot_upload_audios (deleted_at);

-- ── 机器人播放状态表 (BotPlayerState → bot_player_states) ──
CREATE TABLE IF NOT EXISTS bot_player_states (
    channel_id        VARCHAR(64) PRIMARY KEY,
    current_track_id  VARCHAR(64),
    playing           BOOLEAN DEFAULT FALSE,
    paused            BOOLEAN DEFAULT FALSE,
    "current_time"    BIGINT DEFAULT 0,
    volume            BIGINT DEFAULT 80,
    play_mode         VARCHAR(16) DEFAULT 'order',
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── 机器人 TTS 消息表 (BotTTSMessage → bot_tts_messages) ──
CREATE TABLE IF NOT EXISTS bot_tts_messages (
    id                   VARCHAR(64) PRIMARY KEY,
    user_id              VARCHAR(64) NOT NULL,
    channel_id           VARCHAR(64) NOT NULL,
    text                 TEXT NOT NULL,
    voice_name           VARCHAR(64),
    speed                DOUBLE PRECISION DEFAULT 1.0,
    pitch                DOUBLE PRECISION DEFAULT 0,
    volume               DOUBLE PRECISION DEFAULT 1.0,
    voice_data_path      VARCHAR(512),
    voice_data_sensitive BOOLEAN DEFAULT TRUE,
    played_at            TIMESTAMPTZ,
    expires_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── TTS 同意表 (TTSConsent → tts_consents) ──
CREATE TABLE IF NOT EXISTS tts_consents (
    id           VARCHAR(64) PRIMARY KEY,
    user_id      VARCHAR(64) NOT NULL,
    consented    BOOLEAN DEFAULT FALSE,
    consented_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tts_consents_user_id ON tts_consents (user_id);

-- ── 网易云认证表 (NeteaseAuth → netease_auths) ──
CREATE TABLE IF NOT EXISTS netease_auths (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    cookie      TEXT,
    expires_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_netease_auths_user_id ON netease_auths (user_id);

-- ════════════════════════════════════════════════════════════
-- 白板
-- ════════════════════════════════════════════════════════════

-- ── 白板笔迹表 (WhiteboardStroke → whiteboard_strokes) ──
-- L18: ID size 64→32; L19: 添加 tool 字段; L20: 添加 author_name 快照字段; type 改为可空（旧字段保留）
CREATE TABLE IF NOT EXISTS whiteboard_strokes (
    id                VARCHAR(32) PRIMARY KEY,
    channel_id        VARCHAR(64) NOT NULL,
    user_id           VARCHAR(64) NOT NULL,
    author_name       VARCHAR(64) NOT NULL,
    type              VARCHAR(16),
    tool              VARCHAR(16) NOT NULL,
    data              TEXT,
    color             VARCHAR(16),
    width             DOUBLE PRECISION,
    layer             BIGINT DEFAULT 0,
    encrypted         BOOLEAN DEFAULT FALSE,
    encryption_key_id VARCHAR(64),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_whiteboard_strokes_channel_id ON whiteboard_strokes (channel_id);

-- ════════════════════════════════════════════════════════════
-- 文件与共享
-- ════════════════════════════════════════════════════════════

-- ── 文件元数据表 (FileMetadata → file_metadata) ──
CREATE TABLE IF NOT EXISTS file_metadata (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    file_name   VARCHAR(256),
    file_path   VARCHAR(512),
    file_size   BIGINT,
    mime_type   VARCHAR(64),
    checksum    VARCHAR(64),
    channel_id  VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── 共享文件夹表 (SharedFolder → shared_folders) ──
CREATE TABLE IF NOT EXISTS shared_folders (
    id                VARCHAR(64) PRIMARY KEY,
    space_id          VARCHAR(64) NOT NULL,
    parent_id         VARCHAR(64),
    name              VARCHAR(128) NOT NULL,
    path              VARCHAR(512) NOT NULL,
    owner_id          VARCHAR(64) NOT NULL,
    permission_policy TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_shared_folders_space_id ON shared_folders (space_id);
CREATE INDEX IF NOT EXISTS idx_shared_folders_parent_id ON shared_folders (parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_parent_name ON shared_folders (parent_id, name);
CREATE INDEX IF NOT EXISTS idx_shared_folders_path ON shared_folders (path);
CREATE INDEX IF NOT EXISTS idx_shared_folders_deleted_at ON shared_folders (deleted_at);

-- ── 共享文件条目表 (SharedFileEntry → shared_file_entries) ──
-- L16: 添加 file_ext 和 version_no 字段
CREATE TABLE IF NOT EXISTS shared_file_entries (
    id            VARCHAR(64) PRIMARY KEY,
    space_id      VARCHAR(64) NOT NULL,
    folder_id     VARCHAR(64) NOT NULL,
    file_name     VARCHAR(256) NOT NULL,
    physical_name VARCHAR(512),
    file_path     VARCHAR(512),
    file_size     BIGINT,
    mime_type     VARCHAR(64),
    file_ext      VARCHAR(32),
    version_no    BIGINT DEFAULT 1,
    uploaded_by   VARCHAR(64),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_shared_file_entries_space_id ON shared_file_entries (space_id);
CREATE INDEX IF NOT EXISTS idx_shared_file_entries_folder_id ON shared_file_entries (folder_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_file_folder_name ON shared_file_entries (folder_id, file_name);
CREATE INDEX IF NOT EXISTS idx_shared_file_entries_file_path ON shared_file_entries (file_path);
CREATE INDEX IF NOT EXISTS idx_shared_file_entries_deleted_at ON shared_file_entries (deleted_at);

-- ── 共享文档表 (SharedDocument → shared_documents) ──
CREATE TABLE IF NOT EXISTS shared_documents (
    id                VARCHAR(64) PRIMARY KEY,
    space_id          VARCHAR(64) NOT NULL,
    channel_id        VARCHAR(64),
    title             VARCHAR(128) NOT NULL,
    content           TEXT,
    owner_id          VARCHAR(64) NOT NULL,
    permission_policy TEXT,
    version           BIGINT DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_shared_documents_space_id ON shared_documents (space_id);
CREATE INDEX IF NOT EXISTS idx_shared_documents_channel_id ON shared_documents (channel_id);
CREATE INDEX IF NOT EXISTS idx_shared_documents_deleted_at ON shared_documents (deleted_at);

-- ════════════════════════════════════════════════════════════
-- 日程与小游戏
-- ════════════════════════════════════════════════════════════

-- ── 日程事件表 (ScheduleEvent → schedule_events) ──
-- L25: 添加 event_date 字段；L26: 添加 reminder_minutes/reminder_sent；M24: 添加 scope 字段
CREATE TABLE IF NOT EXISTS schedule_events (
    id               VARCHAR(64) PRIMARY KEY,
    space_id         VARCHAR(64) NOT NULL,
    title            VARCHAR(128) NOT NULL,
    description      TEXT,
    location         VARCHAR(256),
    event_date       DATE NOT NULL,
    start_time       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    end_time         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    all_day          BOOLEAN DEFAULT FALSE,
    scope            VARCHAR(32) NOT NULL DEFAULT 'all',
    reminder_minutes BIGINT DEFAULT 0,
    reminder_sent    BOOLEAN DEFAULT FALSE,
    created_by       VARCHAR(64),
    channel_id       VARCHAR(64),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_schedule_space_date ON schedule_events (space_id, start_time);
CREATE INDEX IF NOT EXISTS idx_schedule_event_date ON schedule_events (event_date);
CREATE INDEX IF NOT EXISTS idx_schedule_events_deleted_at ON schedule_events (deleted_at);

-- ── 小游戏会话表 (MinigameSession → minigame_sessions) ──
CREATE TABLE IF NOT EXISTS minigame_sessions (
    id           VARCHAR(64) PRIMARY KEY,
    game_type    VARCHAR(32) NOT NULL,
    channel_id   VARCHAR(64) NOT NULL,
    host_id      VARCHAR(64) NOT NULL,
    state        TEXT,
    status       VARCHAR(16) DEFAULT 'waiting',
    players_json TEXT,
    is_active    BOOLEAN DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_minigame_channel ON minigame_sessions (game_type, channel_id, status);

-- ════════════════════════════════════════════════════════════
-- 虚拟网络
-- ════════════════════════════════════════════════════════════

-- ── 虚拟网络会话表 (VirtualNetSession → virtual_net_sessions) ──
-- L27: 添加 network_cidr 字段
CREATE TABLE IF NOT EXISTS virtual_net_sessions (
    id           VARCHAR(64) PRIMARY KEY,
    space_id     VARCHAR(64) NOT NULL,
    user_id      VARCHAR(64) NOT NULL,
    node_id      VARCHAR(64),
    status       VARCHAR(16) DEFAULT 'disconnected',
    ip           VARCHAR(45),
    network_cidr VARCHAR(64),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_virtual_net_sessions_space_id ON virtual_net_sessions (space_id);
CREATE INDEX IF NOT EXISTS idx_vnet_user_status ON virtual_net_sessions (user_id, status);

-- ── 虚拟网络节点表 (VirtualNetNode → virtual_net_nodes) ──
CREATE TABLE IF NOT EXISTS virtual_net_nodes (
    id           VARCHAR(64) PRIMARY KEY,
    space_id     VARCHAR(64) NOT NULL,
    session_id   VARCHAR(64) NOT NULL,
    name         VARCHAR(64),
    ip           VARCHAR(45),
    status       VARCHAR(16) DEFAULT 'active',
    latency_ms   BIGINT DEFAULT 0,
    last_seen_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_virtual_net_nodes_space_id ON virtual_net_nodes (space_id);
CREATE INDEX IF NOT EXISTS idx_virtual_net_nodes_session_id ON virtual_net_nodes (session_id);

-- ════════════════════════════════════════════════════════════
-- 管理与审计
-- ════════════════════════════════════════════════════════════

-- ── 管理员配置表 (AdminConfig → admin_configs) ──
CREATE TABLE IF NOT EXISTS admin_configs (
    id             VARCHAR(64) PRIMARY KEY,
    server_name    VARCHAR(64),
    allow_register BOOLEAN DEFAULT TRUE,
    api_port       BIGINT DEFAULT 8080,
    admin_port     BIGINT DEFAULT 8081,
    live_kit_port  BIGINT DEFAULT 7880,
    vpn_port       BIGINT DEFAULT 41641,
    public_address VARCHAR(255),
    max_users      BIGINT DEFAULT 1000,
    deploy_mode    VARCHAR(16) DEFAULT 'native',
    smtp_host      VARCHAR(255),
    smtp_port      BIGINT,
    smtp_user      VARCHAR(255),
    smtp_password  VARCHAR(255),
    smtp_from      VARCHAR(255),
    smtp_enable    BOOLEAN DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── 审计日志表 (AuditLog → audit_logs) ──
CREATE TABLE IF NOT EXISTS audit_logs (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64),
    action      VARCHAR(64) NOT NULL,
    resource    VARCHAR(64),
    details     TEXT,
    ip_address  VARCHAR(45),
    user_agent  VARCHAR(512),
    success     BOOLEAN DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs (user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs (action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs (created_at);

-- ── 模块运行状态表 (ModuleRuntimeStatus → module_runtime_statuses) ──
CREATE TABLE IF NOT EXISTS module_runtime_statuses (
    id                  VARCHAR(64) PRIMARY KEY,
    module_name         VARCHAR(32) NOT NULL,
    enabled             BOOLEAN DEFAULT TRUE,
    grace_period_ends_at TIMESTAMPTZ,
    updated_by          VARCHAR(64),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_module_runtime_statuses_module_name ON module_runtime_statuses (module_name);

-- ── 屏幕共享会话表 (ScreenShareSession → screen_share_sessions) ──
CREATE TABLE IF NOT EXISTS screen_share_sessions (
    id            VARCHAR(64) PRIMARY KEY,
    user_id       VARCHAR(64) NOT NULL,
    channel_id    VARCHAR(64) NOT NULL,
    share_type    VARCHAR(32) DEFAULT 'screen',
    status        VARCHAR(32) DEFAULT 'active',
    viewer_policy VARCHAR(32) DEFAULT 'all',
    active        BOOLEAN DEFAULT FALSE,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at      TIMESTAMPTZ,
    ended_reason  VARCHAR(32)
);

CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_user_id ON screen_share_sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_channel_id ON screen_share_sessions (channel_id);
CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_active ON screen_share_sessions (active);

-- ── 安全审计日志表 (SecurityAuditLog → security_audit_logs) ──
CREATE TABLE IF NOT EXISTS security_audit_logs (
    id            VARCHAR(64) PRIMARY KEY,
    user_id       VARCHAR(64),
    action        VARCHAR(64) NOT NULL,
    resource_type VARCHAR(64),
    resource_id   VARCHAR(64),
    ip_masked     VARCHAR(64),
    details_json  TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_security_audit_logs_user_id ON security_audit_logs (user_id);
CREATE INDEX IF NOT EXISTS idx_security_audit_logs_action ON security_audit_logs (action);
CREATE INDEX IF NOT EXISTS idx_security_audit_logs_created_at ON security_audit_logs (created_at);

-- ── 共享文档版本历史表 (SharedDocumentVersion → shared_document_versions) ──
-- H36: 此表在原 001_init 中缺失，现补全
CREATE TABLE IF NOT EXISTS shared_document_versions (
    id           VARCHAR(64) PRIMARY KEY,
    document_id  VARCHAR(64) NOT NULL,
    version_no   INTEGER NOT NULL,
    title        VARCHAR(128) NOT NULL,
    content      TEXT,
    edited_by    VARCHAR(64) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_doc_version_doc ON shared_document_versions (document_id, version_no);

-- ── 密码历史表 (PasswordHistory → password_history) ──
-- H36: 此表在原 001_init 中缺失，现补全（H10 密码历史检查依赖）
CREATE TABLE IF NOT EXISTS password_history (
    id            VARCHAR(64) PRIMARY KEY,
    user_id       VARCHAR(64) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_password_history_user_created ON password_history (user_id, created_at);

-- ── 机器人令牌表 (Bot → bots) ──
-- H36: 此表在原 001_init 中缺失，现补全（M22 机器人令牌管理依赖）
CREATE TABLE IF NOT EXISTS bots (
    id         VARCHAR(64) PRIMARY KEY,
    channel_id VARCHAR(64) NOT NULL,
    name       VARCHAR(64),
    token      VARCHAR(128) NOT NULL,
    created_by VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_bots_channel_id ON bots (channel_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_token ON bots (token);
CREATE INDEX IF NOT EXISTS idx_bots_deleted_at ON bots (deleted_at);

-- ════════════════════════════════════════════════════════════
-- 迁移完成
-- ════════════════════════════════════════════════════════════
