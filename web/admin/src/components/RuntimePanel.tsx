import { useState, type CSSProperties } from 'react'
import { Cpu, HardDrive, BarChart3, Music, Download, RefreshCw, AlertTriangle, BellOff, ChevronDown, ChevronRight, History } from 'lucide-react'
import type { StorageCategory } from '../types/admin'
import type { RuntimeStatsSnapshot, AdminAlertInfo } from '../types/admin'
import { formatBytes, formatBytesFromBytes, formatPercent, formatUptime } from '../utils/format'
import { exportAdminAuditLogs } from '../../../shared/api-core'

interface RuntimePanelProps {
  performance: RuntimeStatsSnapshot | null
  systemDisk: { total: number; used: number; free: number; percent: number } | null
  storage: StorageCategory[]
  auditLogs: { time: string; level: string; message: string; moduleName?: string }[]
  refreshAuditLogsNow: () => void
  auditLogsRefreshing: boolean
  refreshInterval: '5' | '10' | '30' | 'off'
  setRefreshInterval: (value: '5' | '10' | '30' | 'off') => void
  dataLoading: boolean
  // A11（DES-20261001-01 §12.3）：监控告警横幅 + 历史列表
  adminAlerts: AdminAlertInfo[]
  resolvedAlerts: AdminAlertInfo[]
  onMuteAlert: (id: string) => void
  mutingAlertId: string | null
}

// 告警类型的中文展示名（横幅与历史列表共用）。
const ALERT_TYPE_LABELS: Record<string, string> = {
  disk: '磁盘',
  health: '服务健康',
  livekit: 'LiveKit',
  database: '数据库',
  tts_worker: 'TTS Worker',
}

// severity 配色（DES §12.3：alert 红 / warn 黄）。
function alertBannerStyle(severity: string): CSSProperties {
  return severity === 'alert'
    ? { background: 'var(--danger, #e74c3c)', color: '#fff' }
    : { background: 'var(--warning, #f39c12)', color: '#fff' }
}

function formatAlertTime(iso?: string | null): string {
  if (!iso) return '--'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '--' : d.toLocaleString('zh-CN', { hour12: false })
}

export default function RuntimePanel({
  performance,
  systemDisk,
  storage,
  auditLogs,
  refreshAuditLogsNow,
  auditLogsRefreshing,
  refreshInterval,
  setRefreshInterval,
  dataLoading,
  adminAlerts,
  resolvedAlerts,
  onMuteAlert,
  mutingAlertId,
}: RuntimePanelProps) {
  const totalStorage = storage.reduce((sum, c) => sum + c.size, 0)
  const [exportingLogs, setExportingLogs] = useState(false)
  const [showAlertHistory, setShowAlertHistory] = useState(false)

  // C-6: 导出审计日志 CSV（后端返回文件流，前端触发浏览器下载）。
  const handleExportLogs = async () => {
    setExportingLogs(true)
    try {
      await exportAdminAuditLogs()
    } catch (err: any) {
      alert('导出失败: ' + (err?.message || '未知错误'))
    } finally {
      setExportingLogs(false)
    }
  }

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
        <h2 style={{ fontSize: 20 }}>运行监控</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <label htmlFor="refresh-interval" style={{ fontSize: 13, color: 'var(--text-secondary)' }}>自动刷新</label>
          <select id="refresh-interval" className="select" style={{ minWidth: 90 }} value={refreshInterval} onChange={e => setRefreshInterval(e.target.value as typeof refreshInterval)}>
            <option value="5">5秒</option>
            <option value="10">10秒</option>
            <option value="30">30秒</option>
            <option value="off">关闭</option>
          </select>
        </div>
      </div>
      {/* A11 §12.3：活跃告警横幅（severity 配色 + 静默 24h 按钮） */}
      {adminAlerts.length > 0 && (
        <div role="alert" style={{ display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 20 }}>
          {adminAlerts.map(alert => (
            <div
              key={alert.id}
              data-testid={`admin-alert-banner-${alert.type}`}
              style={{
                ...alertBannerStyle(alert.severity),
                padding: '10px 16px', borderRadius: 6, display: 'flex', alignItems: 'center', gap: 12, fontSize: 13,
              }}
            >
              <AlertTriangle size={16} aria-hidden="true" />
              <span>
                <strong>{alert.severity === 'alert' ? '严重' : '警告'}</strong>
                {' · '}{ALERT_TYPE_LABELS[alert.type] || alert.type}：{alert.message}
              </span>
              {alert.mutedUntil && (
                <span style={{ opacity: 0.85, fontSize: 12 }}>（已静默至 {formatAlertTime(alert.mutedUntil)}）</span>
              )}
              <button
                className="track-btn"
                style={{ marginLeft: 'auto', background: 'rgba(255,255,255,0.2)', color: '#fff', border: 'none', padding: '4px 12px' }}
                disabled={mutingAlertId === alert.id || !!alert.mutedUntil}
                title={alert.mutedUntil ? '该类型已处于静默期' : '24 小时内不再推送此类型告警（表仍记录）'}
                onClick={() => onMuteAlert(alert.id)}
              >
                <BellOff size={12} aria-hidden="true" style={{ marginRight: 4, verticalAlign: 'middle' }} />
                {mutingAlertId === alert.id ? '静默中...' : '静默 24h'}
              </button>
            </div>
          ))}
        </div>
      )}
      {!performance ? (
        <div className="admin-card" style={{ textAlign: 'center', color: 'var(--text-secondary)', padding: 40 }}>
          {dataLoading ? '加载运行时数据中...' : '暂无运行时数据'}
        </div>
      ) : (
        <>
          <div className="admin-card">
            <h3>系统资源</h3>
            <div className="stat-grid" style={{ marginTop: 12 }}>
              <div className="stat-box">
                <div className="stat-label"><Cpu size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 4 }} />CPU</div>
                <div className="stat-value">{formatPercent(performance.cpu.value)}%{performance.cpu.simulated && <span style={{ fontSize: 12, color: 'var(--text-secondary)', marginLeft: 4 }}>(模拟)</span>}</div>
                <div style={{ marginTop: 8, height: 8, background: 'var(--bg-primary)', borderRadius: 4, overflow: 'hidden' }}>
                  <div style={{ width: `${formatPercent(performance.cpu.value)}%`, height: '100%', background: 'var(--accent)', transition: 'width 0.3s' }} />
                </div>
              </div>
              <div className="stat-box">
                <div className="stat-label"><HardDrive size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 4 }} />内存</div>
                <div className="stat-value">{formatPercent(performance.memory.percent)}%</div>
                <div style={{ marginTop: 8, height: 8, background: 'var(--bg-primary)', borderRadius: 4, overflow: 'hidden' }}>
                  <div style={{ width: `${formatPercent(performance.memory.percent)}%`, height: '100%', background: 'var(--accent)', transition: 'width 0.3s' }} />
                </div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>总计 {formatBytes(performance.memory.total)} / 已用 {formatBytes(performance.memory.used)} / 可用 {formatBytes(performance.memory.available)}</div>
              </div>
              <div className="stat-box">
                <div className="stat-label"><BarChart3 size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 4 }} />磁盘 I/O</div>
                <div className="stat-value">{formatPercent(performance.disk)}%</div>
                <div style={{ marginTop: 8, height: 8, background: 'var(--bg-primary)', borderRadius: 4, overflow: 'hidden' }}>
                  <div style={{ width: `${formatPercent(performance.disk)}%`, height: '100%', background: 'var(--accent)', transition: 'width 0.3s' }} />
                </div>
              </div>
            </div>
          </div>
          <div className="admin-card">
            <h3>磁盘使用</h3>
            <div className="stat-grid" style={{ gridTemplateColumns: '1fr 1fr', marginTop: 12 }}>
              <div className="stat-box">
                <div className="stat-label">系统盘</div>
                <div className="stat-value">{formatBytesFromBytes(systemDisk?.used || 0)}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>已用 / 总计 {formatBytesFromBytes(systemDisk?.total || 0)}</div>
              </div>
              <div className="stat-box">
                <div className="stat-label">RidgeRiceTalk 总占用</div>
                <div className="stat-value">{formatBytes(totalStorage)}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>应用数据 + 日志</div>
              </div>
            </div>
          </div>
          {/* Network rate (replaces hardcoded throughput charts) */}
          <div className="admin-card">
            <h3 style={{ fontSize: 14 }}>网络速率</h3>
            <div style={{ display: 'flex', gap: 24, marginTop: 12 }}>
              <div>
                <div style={{ fontSize: 11, color: 'var(--text-secondary)' }}>接收</div>
                <div style={{ fontSize: 18, fontWeight: 600, color: 'var(--accent)' }}>{formatBytesFromBytes(performance?.network?.rx || 0)}</div>
              </div>
              <div>
                <div style={{ fontSize: 11, color: 'var(--text-secondary)' }}>发送</div>
                <div style={{ fontSize: 18, fontWeight: 600, color: '#3b82f6' }}>{formatBytesFromBytes(performance?.network?.tx || 0)}</div>
              </div>
            </div>
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 8 }}>累计字节数（历史时序数据需后端支持）</div>
          </div>

          {/* Extra runtime stats */}
          <div className="admin-card">
            <h3>实时概况</h3>
            <div className="stat-grid" style={{ marginTop: 12, gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))' }}>
              <div className="stat-box">
                <div className="stat-label">在线用户</div>
                <div className="stat-value">{performance?.online_users ?? '-'}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>{performance?.online_users !== undefined ? '当前在线' : '暂无数据'}</div>
              </div>
              <div className="stat-box">
                <div className="stat-label">当前语音</div>
                <div className="stat-value">{performance?.active_voices ?? '-'}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>{performance?.active_voices !== undefined ? '活跃语音室' : '暂无数据'}</div>
              </div>
              <div className="stat-box">
                <div className="stat-label">消息速率</div>
                <div className="stat-value">{performance?.message_rate ?? '-'}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>{performance?.message_rate !== undefined ? '近 1 分钟条数' : '暂无数据'}</div>
              </div>
              <div className="stat-box">
                <div className="stat-label">WebSocket 连接</div>
                <div className="stat-value">{performance?.websocket_conns ?? '-'}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>{performance?.websocket_conns !== undefined ? '活跃连接' : '暂无数据'}</div>
              </div>
              <div className="stat-box">
                <div className="stat-label">网络接收</div>
                <div className="stat-value">{formatBytesFromBytes(performance?.network?.rx || 0)}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>累计字节</div>
              </div>
              <div className="stat-box">
                <div className="stat-label">运行时长</div>
                <div className="stat-value">{formatUptime(performance?.uptime || 0)}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>自上次重启</div>
              </div>
            </div>
          </div>
          <div className="admin-card">
            <h3>累计流量</h3>
            <div style={{ marginTop: 12, fontSize: 13, color: 'var(--text-secondary)' }}>
              接收总计: {formatBytesFromBytes(performance?.network?.rx || 0)}<br/>
              发送总计: {formatBytesFromBytes(performance?.network?.tx || 0)}<br/>
              <span style={{ fontSize: 12 }}>按端口细分统计需后端支持</span>
            </div>
          </div>
          <div className="admin-card">
            <h3>机器人状态</h3>
            <div style={{ marginTop: 12, fontSize: 13, color: 'var(--text-secondary)' }}>
              <Music size={18} color="var(--accent)" aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 8 }} />
              请前往机器人频道查看实时播放状态
            </div>
          </div>
          <div className="admin-card">
            <h3 style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span>最近日志</span>
              <button className="track-btn" style={{ fontSize: 12, padding: '4px 10px' }} disabled={auditLogsRefreshing} onClick={refreshAuditLogsNow}>
                <RefreshCw size={12} aria-hidden="true" style={{ marginRight: 4, verticalAlign: 'middle', animation: auditLogsRefreshing ? 'spin 1s linear infinite' : undefined }} />
                {auditLogsRefreshing ? '刷新中...' : '刷新审计日志'}
              </button>
            </h3>
            <div style={{ background: 'var(--bg-primary)', borderRadius: 6, padding: 12, fontFamily: 'monospace', fontSize: 12, lineHeight: 1.6, color: 'var(--text-secondary)', maxHeight: 200, overflowY: 'auto', marginTop: 12 }}>
              {auditLogs.length === 0 ? (
                <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>暂无审计日志</div>
              ) : auditLogs.slice(0, 10).map((log, i) => (
                <div key={i}>[{log.time}] [{log.level}] {log.message}</div>
              ))}
            </div>
            <div style={{ display: 'flex', gap: 8, marginTop: 12, justifyContent: 'flex-end' }}>
              <button className="track-btn" disabled title="暂未实现" style={{ opacity: 0.5, cursor: 'not-allowed' }}>查看更多日志</button>
              <button className="track-btn" disabled={exportingLogs} onClick={handleExportLogs}><Download size={14} aria-hidden="true" style={{ marginRight: 4 }} /> {exportingLogs ? '导出中...' : '导出日志'}</button>
            </div>
          </div>
          {/* A11 §12.3：历史告警折叠列表（时间/类型/消息/恢复时间） */}
          <div className="admin-card">
            <h3>
              <button
                data-testid="alert-history-toggle"
                style={{ background: 'none', border: 'none', color: 'inherit', font: 'inherit', padding: 0, cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6 }}
                onClick={() => setShowAlertHistory(v => !v)}
              >
                {showAlertHistory ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
                <History size={14} aria-hidden="true" />
                历史告警（{resolvedAlerts.length}）
              </button>
            </h3>
            {showAlertHistory && (
              resolvedAlerts.length === 0 ? (
                <div style={{ marginTop: 12, fontSize: 13, color: 'var(--text-secondary)', fontStyle: 'italic' }}>暂无历史告警</div>
              ) : (
                <div style={{ marginTop: 12, display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {resolvedAlerts.map(alert => (
                    <div
                      key={alert.id}
                      data-testid="alert-history-item"
                      style={{ display: 'flex', alignItems: 'center', gap: 12, fontSize: 12, color: 'var(--text-secondary)', padding: '6px 10px', background: 'var(--bg-primary)', borderRadius: 6 }}
                    >
                      <span style={{ minWidth: 130 }}>{formatAlertTime(alert.firstSeenAt)}</span>
                      <span style={{ ...alertBannerStyle(alert.severity), borderRadius: 4, padding: '1px 8px', fontSize: 11 }}>
                        {ALERT_TYPE_LABELS[alert.type] || alert.type}
                      </span>
                      <span style={{ flex: 1, color: 'var(--text-primary)' }}>{alert.message}</span>
                      <span>恢复于 {formatAlertTime(alert.resolvedAt)}</span>
                    </div>
                  ))}
                </div>
              )
            )}
          </div>
        </>
      )}
    </>
  )
}
