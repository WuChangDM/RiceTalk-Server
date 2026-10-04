export function formatBytes(mb: number) {
  if (mb >= 1024) return `${(mb / 1024).toFixed(2)} GB`
  return `${mb.toFixed(1)} MB`
}

// 格式化字节数（后端 network.rx/tx 返回字节）
export function formatBytesFromBytes(bytes: number) {
  if (!bytes || bytes < 0) return '0 B'
  if (bytes >= 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${bytes} B`
}

// 格式化百分比（保留 1 位小数）
export function formatPercent(v: number): string {
  return typeof v === 'number' && !isNaN(v) ? v.toFixed(1) : '0.0'
}

// 格式化运行时长（秒）
export function formatUptime(seconds: number): string {
  if (typeof seconds !== 'number' || seconds < 0) return '0s'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m ${s}s`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

// E4-UI：把服务端下发的 UTC RFC3339 时间串（如 2026-10-01T12:34:56Z）转成
// 可读的 "2026-10-01 12:34:56 UTC"。刻意不做本地时区换算（jsdom/测试环境
// 时区不稳定，且 UTC 标注对部署方排查日志更直接）；空值显示 "-"。
export function formatShareTime(iso?: string | null): string {
  if (!iso) return '-'
  return iso.replace('T', ' ').replace(/Z$/, ' UTC')
}
