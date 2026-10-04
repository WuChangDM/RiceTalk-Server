import { useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { toggleModule, apiPost, getAdminModules, getAdminLogs } from '../../../shared/api-core'
import type { ModuleInfo } from '../types/admin'

interface ModulesPanelProps {
  modules: ModuleInfo[]
  setModules: (modules: ModuleInfo[]) => void
  auditLogs: { time: string; level: string; message: string; moduleName?: string }[]
  setAuditLogs: (logs: { time: string; level: string; message: string; moduleName?: string }[]) => void
  refreshAuditLogsNow: () => void
  auditLogsRefreshing: boolean
  moduleGracePeriod: { moduleName: string; remainingSeconds: number } | null
  setModuleGracePeriod: (value: { moduleName: string; remainingSeconds: number } | null) => void
}

export default function ModulesPanel({
  modules,
  setModules,
  auditLogs,
  setAuditLogs,
  refreshAuditLogsNow,
  auditLogsRefreshing,
  moduleGracePeriod,
  setModuleGracePeriod,
}: ModulesPanelProps) {
  const [selectedModuleLog, setSelectedModuleLog] = useState('all')

  return (
    <>
      <h2 style={{ marginBottom: 20, fontSize: 20 }}>模块管理</h2>
      <div className="admin-card" style={{ overflow: 'auto' }}>
        <table className="data-table">
          <thead>
            <tr>
              <th>模块</th>
              <th>状态</th>
              <th>说明</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {modules.map(m => {
              const isRunning = m.enabled === true || m.status === 'running' || m.status === 'active'
              return (
                <tr key={m.name}>
                  <td><strong>{m.name}</strong></td>
                  <td>
                    {/* P1-5: 原此处显示 "CPU x% / MEM y MB"，但后端建默认模块时
                        CPU/Memory 恒为 0 且没有任何采样任务回填（admin/service.go
                        GetModules），属于长期恒 0 的假数据，故移除。 */}
                    <span style={{ color: isRunning ? 'var(--accent)' : 'var(--text-secondary)' }}>● {isRunning ? '运行中' : '已禁用'}</span>
                  </td>
                  <td>{m.description}</td>
                  <td>
                    <button className="track-btn" onClick={async () => {
                      const action = isRunning ? 'disable' : 'enable'
                      try {
                        await toggleModule(m.name, action)
                        setModules(modules.map(x => x.name === m.name ? { ...x, status: action === 'enable' ? 'running' : 'disabled', enabled: action === 'enable' } : x))
                        // H44: 禁用模块时启动 5 分钟宽限期倒计时
                        if (action === 'disable') {
                          setModuleGracePeriod({ moduleName: m.name, remainingSeconds: 300 })
                        }
                        // 操作完成后刷新整个模块列表以同步后端状态
                        try {
                          const res = await getAdminModules()
                          if (res?.data) setModules(res.data.items || res.data || [])
                        } catch { /* ignore refetch error */ }
                      } catch (err: any) {
                        alert('模块操作失败: ' + (err.message || '未知错误'))
                      }
                    }}>
                      {isRunning ? '禁用' : '启用'}
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <div className="admin-card">
        <h3 style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span>统一日志</span>
          <button className="track-btn" style={{ fontSize: 12, padding: '4px 10px' }} disabled={auditLogsRefreshing} onClick={refreshAuditLogsNow}>
            <RefreshCw size={12} aria-hidden="true" style={{ marginRight: 4, verticalAlign: 'middle', animation: auditLogsRefreshing ? 'spin 1s linear infinite' : undefined }} />
            {auditLogsRefreshing ? '刷新中...' : '刷新审计日志'}
          </button>
        </h3>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <label htmlFor="module-log-filter" style={{ fontSize: 13, color: 'var(--text-secondary)' }}>模块:</label>
          <select id="module-log-filter" className="select" style={{ minWidth: 140 }} value={selectedModuleLog} onChange={e => setSelectedModuleLog(e.target.value)}>
            <option value="all">全部</option>
            {modules.map(m => <option key={m.name} value={m.name}>{m.name}</option>)}
          </select>
        </div>
        <div style={{ background: 'var(--bg-primary)', borderRadius: 6, padding: 12, fontFamily: 'monospace', fontSize: 12, lineHeight: 1.6, color: 'var(--text-secondary)', maxHeight: 200, overflowY: 'auto' }}>
          {(() => {
            const filtered = selectedModuleLog === 'all' ? auditLogs : auditLogs.filter(log => log.moduleName === selectedModuleLog)
            return filtered.length === 0 ? (
              <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>暂无审计日志</div>
            ) : filtered.map((log, i) => (
              <div key={i}>[{log.time}] [{log.level}] {log.message}</div>
            ))
          })()}
        </div>
        <div style={{ marginTop: 12, textAlign: 'right' }}>
          <button className="track-btn" onClick={async () => {
            try {
              const res = await getAdminLogs(1, 100)
              if (res?.data?.items) {
                const mapped = res.data.items.map((l: any) => ({
                  time: l.createdAt ? new Date(l.createdAt).toLocaleTimeString('zh-CN', { hour12: false }) : '--:--:--',
                  level: (l.level || 'INFO').toUpperCase(),
                  message: l.action || l.message || l.summary || JSON.stringify(l),
                  moduleName: l.resource || l.module || '',
                }));
                setAuditLogs(mapped)
              }
            } catch (err: any) {
              alert('加载日志失败: ' + (err.message || '未知错误'))
            }
          }}>查看更多日志</button>
        </div>
      </div>
    </>
  )
}
