// RidgeRiceTalk Music Bot Worker - 核心类型定义
// 阶段 2：定义 Bot 会话状态、曲目信息、HTTP 请求体等数据结构
// 阶段 4：新增队列/播放历史/shuffle/空闲管理相关类型

/** 播放模式（与 Go 端 model.BotPlayerState.PlayMode 保持一致） */
export type PlayMode = 'order' | 'repeat-all' | 'repeat-one' | 'random';

/** 曲目来源（与 Go 端 model.BotPlayQueue.Source 保持一致） */
export type TrackSource = 'netease' | 'upload' | 'tts';

/**
 * 曲目信息
 * 与 Go 端 model.BotPlayQueue 字段对齐，但仅保留 Worker 播放所需字段
 * （Position/Priority/Status/AddedBy/CreatedAt 等数据库字段由 Go 端管理）
 *
 * 阶段 4：新增 audioUrl 字段，由 Go 端在队列同步时附带
 * Worker 自主 playNext 时直接从 queue 中读取 audioUrl，无需回调 Go
 */
export interface Track {
  /** 队列项 ID（model.BotPlayQueue.ID） */
  id: string;
  /** 曲目 ID（网易云歌曲 ID 或 upload 记录 ID 或 TTS 文件 ID） */
  trackId: string;
  /** 标题 */
  title: string;
  /** 艺术家 */
  artist: string;
  /** 时长（秒） */
  duration: number;
  /** 封面 URL */
  cover: string;
  /** 专辑 */
  album: string;
  /** 来源 */
  source: TrackSource;
  /**
   * 音频文件路径或 URL（由 Go 端解析后附带）
   * - upload 类型：本地文件绝对路径
   * - netease 类型：网易云歌曲 URL（有时效性，Go 端在播放时重新解析）
   *
   * 阶段 4：Worker 自主 playNext 时从此字段读取 audioUrl
   * 若此字段为空（如 netease URL 过期），Worker 回调 Go 端解析（阶段 5 实现）
  */
  audioUrl?: string;
  /** 临时音源 URL 的失效时间（Unix 毫秒）；本地上传不设置。 */
  urlExpireAt?: number;
}

/**
 * Play 请求体（Go → Worker）
 * Go 服务端在 startPlayback 中解析 audioUrl 后封装此请求
 */
export interface PlayRequest {
  /** 曲目元数据 */
  track: Track;
  /** 实际音频文件路径或 URL（Go 端已解析） */
  audioUrl?: string;
  /** 目标 LiveKit 房间名（如 "rrt-room-<channelId>"） */
  roomName: string;
  /** 频道 ID（用于状态回调） */
  channelId: string;
  /** 当前音量（0-100，默认 80） */
  volume?: number;
  /** 当前播放模式（默认 'order'） */
  playMode?: PlayMode;
}

/** Stop 请求体（Go → Worker） */
export interface StopRequest {
  /** 是否立即断开 LiveKit 连接（默认 false，仅停止播放） */
  disconnect?: boolean;
}

/** Seek 请求体（Go → Worker） */
export interface SeekRequest {
  /** 目标位置（秒） */
  position: number;
}

/** SetVolume 请求体（Go → Worker） */
export interface SetVolumeRequest {
  /** 音量（0-100） */
  volume: number;
}

// ===== 阶段 4 新增请求体 =====

/** Skip 请求体（Go → Worker） */
export interface SkipRequest {
  /**
   * 是否为用户主动点击下一首（默认 true）。
   * - true：用户点击"下一首"，repeat-one 模式下也切换到下一首
   * - false：自然播放结束，repeat-one 模式下重复当前曲目
   */
  isUserSkip?: boolean;
}

/** Previous 请求体（Go → Worker） */
export interface PreviousRequest {
  /** 预留字段，目前无参数 */
}

/** SetPlayMode 请求体（Go → Worker） */
export interface SetPlayModeRequest {
  /** 播放模式 */
  mode: PlayMode;
  /**
   * 频道 ID（可选，Go 端附带）
   * 用于在 session 不存在时创建轻量 session，确保 setPlayMode 生效
   */
  channelId?: string;
  /**
   * LiveKit 房间名（可选，Go 端附带）
   * 格式：rrt-room-<channelId>
   */
  roomName?: string;
}

/** UpdateQueue 请求体（Go → Worker） */
export interface UpdateQueueRequest {
  /** 最新队列（按 position 升序，Go 端已排序） */
  queue: Track[];
  /**
   * 频道 ID（可选，Go 端附带）
   * 用于在 session 不存在时创建轻量 session，确保队列同步生效
   */
  channelId?: string;
  /**
   * LiveKit 房间名（可选，Go 端附带）
   * 格式：rrt-room-<channelId>
   */
  roomName?: string;
}

/** Disconnect 请求体（Go → Worker） */
export interface DisconnectRequest {
  /** 预留字段，目前无参数 */
}

/**
 * Bot 会话状态
 * Worker 内部维护，对应 Go 端 BotPlayer 的运行时状态
 */
export interface BotSession {
  botId: string;
  /** 频道 ID（用于回调） */
  channelId: string;
  /** LiveKit 房间名 */
  roomName: string;

  /** LiveKit Room 实例（连接后非空） */
  room?: import('@livekit/rtc-node').Room;
  /** 音频源（推流端） */
  audioSource?: import('@livekit/rtc-node').AudioSource;
  /** 本地音频轨道 */
  track?: import('@livekit/rtc-node').LocalAudioTrack;
  /** 已发布的 track publication */
  publication?: import('@livekit/rtc-node').LocalTrackPublication;

  /** 当前播放曲目 */
  currentTrack?: Track;
  /** 实际播放的音频 URL */
  currentAudioUrl?: string;
  /** 是否正在播放 */
  playing: boolean;
  /** 是否暂停（阶段 2 始终为 false，阶段 3 实现） */
  paused: boolean;
  /** 当前播放时间（秒） */
  currentTime: number;
  /** 音量（0-100） */
  volume: number;
  /** 播放模式 */
  playMode: PlayMode;

  /** 取消信号（用于中断 audioFramesFromFile 流） */
  abortController?: AbortController;
  /** 播放循环是否正在运行（防止并发 playLoop） */
  playLoopRunning: boolean;
  /** 播放会话代次；切歌/停止/seek 时递增，隔离旧 FFmpeg 回调。 */
  playbackSessionId: number;
  /** 连续启动/死流失败次数；健康播放 50 帧后重置。 */
  consecutiveFailures: number;
  /** 当前播放会话已成功推送的健康帧数。 */
  healthyFrames: number;
  /** 最后活动时间戳（ms） */
  lastActivity: number;

  /** 临时闪避增益包络（不改写用户 volume）。 */
  duckingGain: import('../audio/voiceDucking.js').DuckingGainEnvelope;
  /** LiveKit 活跃说话者控制器。 */
  voiceDucking: import('../audio/voiceDucking.js').VoiceDuckingController;
  /** LiveKit 事件监听器引用，用于断开时精确卸载。 */
  activeSpeakersListener?: (speakers: import('@livekit/rtc-node').Participant[]) => void;
  participantDisconnectedListener?: (participant: import('@livekit/rtc-node').RemoteParticipant) => void;

  // ===== 阶段 3 新增字段 =====

  /** 暂停唤醒回调：playLoop 检测 paused=true 时阻塞在此 Promise 上 */
  pauseResolver?: () => void;
  /** playLoop 是否正在阻塞等待 resume */
  pauseBlocking: boolean;
  /** seek 请求的起始时间（秒），playLoop 启动时使用 */
  seekOffset: number;

  // ===== 阶段 4 新增字段：队列与播放模式 =====

  /** 当前队列（按 position 升序，由 Go 推送同步） */
  queue: Track[];
  /** 播放历史（最近播放过的曲目，上限 50 条，最新追加到尾部） */
  playHistory: Track[];
  /** 历史回溯索引：-1 表示非回溯态，>=0 表示正在回溯到 history[index] */
  historyIndex: number;
  /** 随机播放顺序：存放 track.id，Fisher-Yates 洗牌后顺序 */
  shuffleOrder: string[];
  /** 当前 shuffle 索引：shuffleOrder[shuffleIndex] 为下一首要播放的曲目 */
  shuffleIndex: number;
  /** 空闲定时器：队列空 1 分钟后断开 LiveKit 连接 */
  idleTimer?: NodeJS.Timeout;
}

/**
 * Status 响应体（Worker → Go）
 * 字段与 Go 端 BotPlayer.Status() 对齐
 */
export interface StatusResponse {
  playing: boolean;
  paused: boolean;
  currentTime: number;
  volume: number;
  playMode: PlayMode;
  currentTrack: Track | null;
}

/** 空状态响应（Bot 会话不存在时返回） */
export const EMPTY_STATUS: StatusResponse = {
  playing: false,
  paused: false,
  currentTime: 0,
  volume: 80,
  playMode: 'order',
  currentTrack: null,
};

/**
 * Pause 操作结果（Worker → Go）
 *
 * 参考 Jellyfin SyncPlay PausedGroupState 的 prevState.Equals(Type) 模式：
 * 当 prevState == 当前状态时，操作为 no-op，但 Worker 仍回送 currentState 快照
 * 供 Go 端校正本地缓存（注释 "Client got lost, sending current state."）。
 *
 * Go 端处理约定：
 * - noop=true 时不调用 broadcastWorkerState 推送给所有 WS 客户端
 * - noop=false 时正常调用 broadcastWorkerState 广播状态变更
 * - currentState 字段在所有分支都返回，让 Go 端可以校正本地缓存（参考 Navidrome
 *   out-of-order 守卫的备份模式）
 */
export interface PauseResult {
  /** 是否为 no-op（重复暂停/未在播放/会话不存在） */
  noop: boolean;
  /** no-op 原因：'session_not_found' | 'not_playing' | 'already_paused' | 'paused' */
  reason: string;
  /** 当前状态快照（session_not_found 时为 null） */
  currentState: StatusResponse | null;
}
