export interface User {
  id: string;
  username: string;
  email: string;
  avatar?: string;
  displayName?: string;
  customStatus?: string;
  theme?: string;
}

export interface Space {
  id: string;
  name: string;
  icon?: string;
}

export type ChannelType = 'TEXT' | 'VOICE';

/**
 * 语音频道音质档（C 档 U-语音音质，DES-20261002-01 §5）。
 * 服务端权威：POST /channels 落库 voice_quality，GET /channels 回传本字段；
 * 创建语音频道时默认 'standard'。客户端按档位映射 LiveKit 发布码率。
 */
export type VoiceQuality = 'fluent' | 'standard' | 'high' | 'ultra';

export interface Channel {
  id: string;
  spaceId: string;
  name: string;
  type: ChannelType;
  position: number;
  pinnedMessageId?: string;
  sortGroup?: string;
  /** 语音频道音质档（仅 VOICE 频道；老服务端/历史数据可能缺省） */
  voiceQuality?: VoiceQuality;
}

export interface Member {
  userId: string;
  username: string;
  displayName?: string;
  avatar?: string;
  role: 'OWNER' | 'ADMIN' | 'MEMBER';
  status: string;       // Web 端使用
  online?: boolean;     // Win 端使用，可选（CP-007 兼容）
  gameStatus?: string;
  customStatus?: string;
}

export interface MessageAttachment {
  id: string;
  messageId: string;
  type: 'image' | 'video' | 'file';
  fileName: string;
  fileSize: number;
  mimeType: string;
  fileUrl: string;
  thumbUrl?: string;
  width?: number;
  height?: number;
  cloudFileId: string;
  createdAt: string;
}

export interface SecurityQuestion {
  question: string;
  answer: string;
}

export interface SecurityQuestionPrompt {
  question: string;
}

export interface Message {
  id: string;
  channelId: string;
  userId: string;
  username: string;
  displayName?: string;
  avatar?: string;
  content: string;
  type?: string;
  createdAt: string;
  reactions?: { emoji: string; userId: string }[];
  parentId?: string;
  /**
   * A9 二期（DES-20261029-01 §7.7）：该消息线程的回复总数（服务端按当页主消息 id 批量 GROUP BY 下发）。
   * 主列表过滤线程回复后客户端无法再靠本地统计得出，必须以本字段为准；空线程为 0（实现恒有值）。
   */
  threadReplyCount?: number;
  clientMessageId?: string;
  pending?: boolean;
  failed?: boolean;
  attachments?: MessageAttachment[];
  replyTo?: {
    id: string;
    username: string;
    displayName?: string;
    content: string;
    createdAt: string;
  };
}

/**
 * B4/U18 全局消息搜索结果（GET /messages/search 归一化后）。
 * 服务端 wire 形状是 snake_case 的 GlobalSearchResult（server/internal/message/service.go），
 * api-core.searchAllMessages 已归一为 camelCase；author 是服务端聚合的显示名
 * （displayName → username 快照），不再单独给 avatar。
 */
export interface MessageSearchResult {
  id: string;
  channelId: string;
  channelName: string;
  authorId: string;
  author: string;
  content: string;
  createdAt: string;
  /**
   * A9 二期（DES-20261029-01 §7.6.3）：命中的是线程回复时服务端 GlobalSearchResult 补 parent_id（task-6 落地）。
   * 客户端据此改走「打开父消息线程抽屉 + 定位该回复」，无值则走 around 邻域页。
   */
  parentId?: string;
}

/**
 * A9 二期（DES-20261029-01 §7.1）：`GET /channels/{id}/unread` 新增的线程未读条目。
 * lastAt = 线程内最后一条未读回复时间（服务端按此降序、cap 20 返回）；无未读线程时返回空数组。
 */
export interface ThreadUnread {
  parentId: string;
  count: number;
  lastAt: string;
}

/**
 * B7 空间自定义表情（DES-20261002-01 §4.7 轻量版）。
 * 服务端 wire 形状 camelCase 直出（openapi components/schemas/ServerEmoji）。
 * url 为相对路径（/api/v1/emojis/:id/file），该端点需要 Authorization 鉴权——
 * `<img src>` 无法携带 header，客户端必须走「鉴权 fetch → Blob → objectURL」通道
 * （api-core.fetchEmojiBlob + emojiStore 缓存），不能直接把 url 塞进 img。
 */
export interface ServerEmoji {
  id: string;
  name: string;         // 空间内唯一，消息中以 :name: 引用（[a-z0-9_] 2-32）
  spaceId: string;
  creatorId?: string;
  url?: string;         // 相对路径；缺省时客户端按 /api/v1/emojis/:id/file 兜底拼接
  createdAt?: string;
}

export interface BotStatus {
  playing: boolean;
  current: Track | null;
  queue: Track[];
}

export interface Track {
  id: string;
  title: string;
  artist: string;
  duration: number;
  cover?: string;
  album?: string;
}

export interface ServerEntry {
  id: string;
  name: string;
  address: string;
  lastConnected: string;
}

export interface Whiteboard {
  id: string;
  spaceId: string;
  name: string;
  archived: boolean;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  thumbnailUrl?: string;
  strokeCount?: number;
  lastDrawnAt?: string;
}

export interface WhiteboardStroke {
  id: string;
  whiteboardId: string;
  userId: string;
  authorName: string;
  tool: string;
  type?: string;
  data: string;
  color: string;
  width: number;
  createdAt: string;
}

export interface AdminSession {
  id: string;
  username: string;
  role: 'OWNER' | 'ADMIN';
}

export interface AdminBootstrapStatus {
  initialized: boolean;
}

export interface ServerConfigSnapshot {
  serverName: string;
  allowRegister: boolean;
  apiPort: number;
  adminPort: number;
  livekitPort: number;
  vpnPort: number;
  publicAddress: string;
  maxUsers: number;
  deployMode: 'native' | 'docker';
  maxFileSize: number;
  maxStorageGB?: number;
}

export interface AdminPerformanceSnapshot {
  cpu: { value: number; simulated: boolean };
  memory: { total: number; used: number; available: number; percent: number };
  disk: number;
  network: { rx: number; tx: number };
  uptime: number;
}

export interface AdminUser {
  id: string;
  username: string;
  email: string;
  role: string;
  online: boolean;
  lastActive: string;
  ip?: string;
  latency?: number;
  userAgent?: string;
}

// D6: 类型化的服务器信息响应（合并 Web 与 Win 字段）
export interface ServerInfoResponse {
  code: string;
  data: {
    state: string;
    initialized: boolean;
    name: string;
    version: string;
    serverUrl: string;
    livekitUrl: string;
    mediaUdpPort?: number;        // Web 字段
    livekitPort?: number;         // Web 字段（U14：网络步初值推导用，服务端实际配置端口）
    webVoiceEnabled: boolean;
    clientAccessEnabled: boolean;
    useHttps: boolean;
    apiPort: number;
    adminPort: number;
    apiUrl?: string;              // Win 字段
    webUrl?: string;              // Win 字段
    adminUrl?: string;            // Win 字段
    features?: Record<string, boolean>;  // Win 字段
  };
}

// D6: 服务器网络配置（Win 端定义）
export interface ServerNetworkConfig {
  deploymentMode: string;
  apiPort: number;
  adminPort: number;
  livekitPort: number;
  publicAddress: string;
  useHttps: boolean;
  domain: string;
  detectedInternalIp: string;
  detectedPublicIp: string;
  upnpEnabled: boolean;
  upnpMapped: boolean;
  frpEnabled: boolean;
  tunnelProvider: string;
}

// D6: 类型化的服务器网络响应
export interface ServerNetworkResponse {
  code: string;
  data: {
    network: ServerNetworkConfig;
    suggestedSrvRecords: string[];
    srvTcpPredicate: string;
    srvUdpPredicate: string;
  };
}

// Task 7: 端口配置面板使用的端口信息类型（GET /api/admin/ports 返回的单条端口项）
export interface PortInfo {
  key: string;           // "api" | "admin" | "lkWs" | "lkTcp" | "lkUdp" | "vpn"
  label: string;         // "API / Web 端口"
  protocol: 'tcp' | 'udp';
  internalPort: number;
  externalPort: number;
  external: boolean;
  description: string;
  natRequired: boolean;  // NAT 环境下内外端口必须一致
}

export interface UserSound {
  id: string;
  userId: string;
  title: string;
  fileSize: number;
  mimeType: string;
  duration: number;
  isPreset: boolean;
  uploaderName: string;
  favorited: boolean;
  createdAt: string;
}

export interface UserSoundSettings {
  joinSoundId: string | null;
  leaveSoundId: string | null;
  joinVolume: number;
  leaveVolume: number;
  enabled: boolean;
}
