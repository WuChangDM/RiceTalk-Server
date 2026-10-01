import type { AdminPerformanceSnapshot } from '../../../shared/types'

export type Tab = 'server' | 'channels' | 'users' | 'modules' | 'storage' | 'runtime'
export type ServerSettingsTab = 'basic' | 'webvoice' | 'ports' | 'network' | 'srv'

// W18: 后端 /api/admin/runtime 在 AdminPerformanceSnapshot 基础字段外额外返回
// 4 个业务统计字段（snake_case，未经转换）。本地扩展类型以安全访问这些字段，
// 避免修改共享类型文件。
export type RuntimeStatsSnapshot = AdminPerformanceSnapshot & {
  online_users?: number
  active_voices?: number
  message_rate?: number
  websocket_conns?: number
}

export interface NetworkConfig {
  externalHost: string
  externalHttpPort: number
  externalLiveKitWsPort: number
  externalMediaUdpPort: number
  externalAdminPort: number
  externalLiveKitTcpPort: number
  useHttps: boolean
  upnpEnabled: boolean
  turnTcpFallbackEnabled: boolean
  webVoiceEnabled: boolean
  clientAccessEnabled: boolean
}

export const EMPTY_NETWORK_CONFIG: NetworkConfig = {
  externalHost: '',
  externalHttpPort: 443,
  externalLiveKitWsPort: 443,
  externalMediaUdpPort: 7882,
  externalAdminPort: 9090,
  externalLiveKitTcpPort: 7881,
  useHttps: true,
  upnpEnabled: true,
  turnTcpFallbackEnabled: true,
  webVoiceEnabled: false,
  clientAccessEnabled: true,
}

export interface ModuleInfo {
  id?: string
  name: string
  description: string
  status: 'running' | 'disabled' | 'active'
  enabled?: boolean
  cpu: number
  memory: number
}

export interface StorageCategory {
  id: string
  name: string
  size: number
  files: number
  description: string
}

// E4-UI：admin 全局分享条目（GET /api/admin/cloudfs/shares 的 items 元素）。
// 字段以服务端 handler_cloudfs_shares.go 为准；lastAccessAt 可能缺失（null/缺省）。
export interface AdminCloudShareItem {
  id: string
  fileId: string
  fileName?: string
  userId: string
  spaceId: string
  spaceName?: string
  downloadCount: number
  lastAccessAt?: string | null
  revoked: boolean
  expired: boolean
  requiresPassword: boolean
  expiresAt: string
  createdAt: string
}

// A11（DES-20261001-01 §12）：管理后台监控告警。
// 字段以服务端 internal/admin/alerts.go AlertEventPayload / model.AdminAlert 为准
//（WS admin_alert 事件与 REST GET /api/admin/alerts 共用同一契约）。
export interface AdminAlertInfo {
  id: string
  type: 'disk' | 'health' | 'livekit' | 'database' | 'tts_worker'
  severity: 'warn' | 'alert'
  message: string
  firstSeenAt: string
  lastSeenAt: string
  resolvedAt?: string | null
  mutedUntil?: string | null
  /** WS 事件附带：open | update | resolved | snapshot | muted */
  action?: string
}
