// M2 观测性：实时指标拉取（GET /api/admin/metrics/realtime）。
//
// 说明：admin 专用接口统一登记在 web/shared/api-core.ts，但本次按任务约束
// 「只改 ridgericetalk（Go）与 web/admin（TS）两处」，不动 shared —— 这里直接
// 复用 shared 导出的底层 apiGet（带 Bearer/CSRF/401 刷新逻辑），仅把路径与
// 响应类型收口在本文件内。后续如允许改 shared，可平移为 api-core 导出函数。
import { apiGet } from '../../../shared/api-core'
import type { RealtimeMetrics } from '../types/admin'

export const REALTIME_METRICS_POLL_MS = 5000

export function getAdminRealtimeMetrics(): Promise<{ code: string; data: RealtimeMetrics }> {
  return apiGet<{ code: string; data: RealtimeMetrics }>('/api/admin/metrics/realtime')
}
