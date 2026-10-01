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

export interface Channel {
  id: string;
  spaceId: string;
  name: string;
  type: ChannelType;
  position: number;
  pinnedMessageId?: string;
  sortGroup?: string;
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
