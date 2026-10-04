import { useState, useEffect, useRef } from 'react'
import {
  Server, LogOut,
  RefreshCw, Sun, Moon,
  AlertTriangle,
} from 'lucide-react'
import type { AdminSession, ServerConfigSnapshot, AdminUser } from '../../shared/types'
import {
  getAdminBootstrapStatus, adminLogout, setTokenKey,
  getAdminConfig, getAdminRuntime, getAdminModules,
  getAdminStorage, getAdminSystemUsage, getAdminUsers,
  getAdminLogs, getAdminNetwork,
  toggleModule, getMe, getAdminAlerts, muteAdminAlert,
} from '../../shared/api-core'
import type { Tab, NetworkConfig, ModuleInfo, StorageCategory, RuntimeStatsSnapshot, AdminAlertInfo } from './types/admin'
import { EMPTY_NETWORK_CONFIG } from './types/admin'
import LoginForm from './components/LoginForm'
import Sidebar from './components/Sidebar'
import ServerSettingsPanel from './components/ServerSettingsPanel'
import ChannelsPanel from './components/ChannelsPanel'
import UsersPanel from './components/UsersPanel'
import ModulesPanel from './components/ModulesPanel'
import StoragePanel from './components/StoragePanel'
import RuntimePanel from './components/RuntimePanel'

// Use separate token key from user client to avoid session conflicts
setTokenKey('rrt_admin_token')

export default function App() {
  const [loading, setLoading] = useState(true)
  const [bootstrapInitialized, setBootstrapInitialized] = useState<boolean | null>(null)

  useEffect(() => {
    getAdminBootstrapStatus().then((res: any) => {
      if (res?.data?.initialized === false && res?.data?.needsBootstrap === true) {
        setBootstrapInitialized(false)
      } else {
        setBootstrapInitialized(true)
      }
      setLoading(false)
    }).catch(() => {
      setBootstrapInitialized(true)
      setLoading(false)
    })
  }, [])

  const [session, setSession] = useState<AdminSession | null>(null)
  const [tab, setTab] = useState<Tab>('server')
  const [theme, setTheme] = useState<'dark' | 'light'>('dark')

  // ISSUE-030: restore admin session from localStorage token on startup
  useEffect(() => {
    const token = localStorage.getItem('rrt_admin_token')
    if (!token) return
    getMe()
      .then((res: any) => {
        const user = res?.data?.user
        if (user) {
          setSession({ id: user.id, username: user.username, role: user.role })
        }
      })
      .catch(() => {
        // Token is invalid or expired — clear it so the user can re-login
        localStorage.removeItem('rrt_admin_token')
      })
  }, [])

  const [config, setConfig] = useState<ServerConfigSnapshot | null>(null)
  const [networkConfig, setNetworkConfig] = useState<NetworkConfig>(EMPTY_NETWORK_CONFIG)
  const [networkConfigSaved, setNetworkConfigSaved] = useState<NetworkConfig>(EMPTY_NETWORK_CONFIG)
  const networkConfigSavedRef = useRef(networkConfigSaved)
  networkConfigSavedRef.current = networkConfigSaved
  const networkConfigRef = useRef(networkConfig)
  networkConfigRef.current = networkConfig

  const [dataLoading, setDataLoading] = useState(false)
  const [dataError, setDataError] = useState<string | null>(null)

  // Users
  const [userKeyword, setUserKeyword] = useState('')
  const [userPage, setUserPage] = useState(1)
  const [userTotal, setUserTotal] = useState(0)
  const [users, setUsers] = useState<(AdminUser & { isActive?: boolean; lastLoginAt?: string })[]>([])
  const [userSummary, setUserSummary] = useState<Record<string, any>>({})
  const usersPerPage = 5

  // Modules & logs
  const [modules, setModules] = useState<ModuleInfo[]>([])
  const [auditLogs, setAuditLogs] = useState<{ time: string; level: string; message: string; moduleName?: string }[]>([])
  const [moduleGracePeriod, setModuleGracePeriod] = useState<{ moduleName: string; remainingSeconds: number } | null>(null)

  // Storage
  const [storage, setStorage] = useState<StorageCategory[]>([])

  // A11（DES-20261001-01 §12.3）：监控告警。active 经 WS 实时维护，
  // 初始态与历史经 REST 拉取。
  const [adminAlerts, setAdminAlerts] = useState<AdminAlertInfo[]>([])
  const [resolvedAlerts, setResolvedAlerts] = useState<AdminAlertInfo[]>([])
  const [mutingAlertId, setMutingAlertId] = useState<string | null>(null)

  // Runtime
  const [refreshInterval, setRefreshInterval] = useState<'5' | '10' | '30' | 'off'>('5')
  const [performance, setPerformance] = useState<RuntimeStatsSnapshot | null>(null)
  const [systemDisk, setSystemDisk] = useState<{ total: number; used: number; free: number; percent: number } | null>(null)

  // 更新检查（P1-3）
  // 服务端 /api/admin/update/check 是未实现的占位接口（没有发布渠道/更新源），
  // 之前前端会把它伪装的"已是最新版本"展示给 Owner。UI 已移除该卡片，
  // 这里也不再轮询该接口。
  const [auditLogsRefreshing, setAuditLogsRefreshing] = useState(false)

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
  }, [theme])

  // Refs mirror userKeyword/userPage so loadAdminData (called from interval)
  // always reads the latest values without stale-closure issues.
  const userKeywordRef = useRef('')
  const userPageRef = useRef(1)
  userKeywordRef.current = userKeyword
  userPageRef.current = userPage

  const loadUsers = async (page?: number, keyword?: string) => {
    const p = page ?? userPageRef.current
    const k = keyword ?? userKeywordRef.current
    try {
      const res = await getAdminUsers(p, usersPerPage, k)
      if (res?.data) {
        setUsers(res.data.items || [])
        setUserTotal(res.data.total || 0)
        setUserSummary(res.data.summary || {})
      }
    } catch {
      // ignore — full error is surfaced by loadAdminData
    }
  }

  const loadAdminData = async () => {
    setDataLoading(true)
    setDataError(null)
    try {
      const [cfg, net, runtime, modulesRes, storageRes, logs, sysUsage, alertsRes] = await Promise.all([
        getAdminConfig().catch(() => null),
        getAdminNetwork().catch(() => null),
        getAdminRuntime().catch(() => null),
        getAdminModules().catch(() => null),
        getAdminStorage().catch(() => null),
        getAdminLogs(1, 50).catch(() => null),
        getAdminSystemUsage().catch(() => null),
        getAdminAlerts().catch(() => null),
      ])
      if (cfg?.data) {
        setConfig(cfg.data)
      }
      if (net?.data?.network) {
        const savedNet = net.data.network
        const hasUnsavedChanges = JSON.stringify(networkConfigRef.current) !== JSON.stringify(networkConfigSavedRef.current)
        setNetworkConfigSaved(savedNet)
        networkConfigSavedRef.current = savedNet
        // 仅在没有未保存改动时同步后端配置，避免覆盖用户正在编辑的内容
        if (!hasUnsavedChanges) {
          setNetworkConfig(savedNet)
        }
      }
      if (runtime?.data) setPerformance(runtime.data)
      if (modulesRes?.data) setModules(modulesRes.data.items || modulesRes.data || [])
      if (storageRes?.data?.items) setStorage(storageRes.data.items)
      if (sysUsage?.data) setSystemDisk(sysUsage.data)
      // A11: 初始告警态（活跃 + 历史）
      if (alertsRes?.data) {
        setAdminAlerts(alertsRes.data.active || [])
        setResolvedAlerts(alertsRes.data.resolved || [])
      }
      if (logs?.data?.items) {
        const mapped = logs.data.items.map((l: any) => ({
          time: l.createdAt ? new Date(l.createdAt).toLocaleTimeString('zh-CN', { hour12: false }) : '--:--:--',
          level: (l.level || 'INFO').toUpperCase(),
          message: l.action || l.message || l.summary || JSON.stringify(l),
          moduleName: l.resource || l.module || '',
        }));
        if (mapped.length) setAuditLogs(mapped)
      }
      // Load users with current keyword/page (uses refs to avoid stale closure)
      await loadUsers()
    } catch (err: any) {
      setDataError(err.message || '加载数据失败')
    } finally {
      setDataLoading(false)
    }
  }

  useEffect(() => {
    if (!session) return
    loadAdminData()
  }, [session])

  // ───────────────────────────────────────────────────────────────
  // §8 P2 NEW-ISSUE-W17 Admin 轮询分级
  // 拆分为三档定时器（替代原单一 5s 全量轮询）：
  //   高频（5s，随 refreshInterval 配置）：runtime + system/usage
  //   中频（30s 固定）：config + network + modules + storage + users
  //   低频（5min 固定）：audit-logs
  // channels 不再全局轮询，仅在频道 tab/频道变更时按需触发。
  // ───────────────────────────────────────────────────────────────

  // 高频：runtime + system/usage（数据变化快、与服务端压力小）
  const loadHighFreqData = async () => {
    try {
      const [runtime, sysUsage] = await Promise.all([
        getAdminRuntime().catch(() => null),
        getAdminSystemUsage().catch(() => null),
      ])
      if (runtime?.data) setPerformance(runtime.data)
      if (sysUsage?.data) setSystemDisk(sysUsage.data)
    } catch {
      // 单一来源错误不抛给 dataError（由 loadAdminData 统一管理）
    }
  }

  // 中频：config + network + modules + storage + users
  const loadMidFreqData = async () => {
    try {
      const [cfg, net, mods, st] = await Promise.all([
        getAdminConfig().catch(() => null),
        getAdminNetwork().catch(() => null),
        getAdminModules().catch(() => null),
        getAdminStorage().catch(() => null),
      ])
      if (cfg?.data) {
        setConfig(cfg.data)
      }
      if (net?.data?.network) {
        const savedNet = net.data.network
        const hasUnsavedChanges = JSON.stringify(networkConfigRef.current) !== JSON.stringify(networkConfigSavedRef.current)
        setNetworkConfigSaved(savedNet)
        networkConfigSavedRef.current = savedNet
        if (!hasUnsavedChanges) {
          setNetworkConfig(savedNet)
        }
      }
      if (mods?.data) setModules(mods.data.items || mods.data || [])
      if (st?.data?.items) setStorage(st.data.items)
      // users 单独走 loadUsers（依赖 userPageRef/userKeywordRef 避免闭包陈旧）
      await loadUsers()
    } catch {
      // ignore — surfaced by loadAdminData
    }
  }

  // 低频：audit-logs（每 5s 拉取是浪费的）
  const loadLowFreqData = async () => {
    try {
      const logs = await getAdminLogs(1, 50).catch(() => null)
      if (logs?.data?.items) {
        const mapped = logs.data.items.map((l: any) => ({
          time: l.createdAt ? new Date(l.createdAt).toLocaleTimeString('zh-CN', { hour12: false }) : '--:--:--',
          level: (l.level || 'INFO').toUpperCase(),
          message: l.action || l.message || l.summary || JSON.stringify(l),
          moduleName: l.resource || l.module || '',
        }));
        if (mapped.length) setAuditLogs(mapped)
      }
    } catch {
      // ignore
    }
  }

  // 手动：刷新审计日志（audit-logs）
  const refreshAuditLogsNow = async () => {
    setAuditLogsRefreshing(true)
    try {
      const logs = await getAdminLogs(1, 50).catch(() => null)
      if (logs?.data?.items) {
        const mapped = logs.data.items.map((l: any) => ({
          time: l.createdAt ? new Date(l.createdAt).toLocaleTimeString('zh-CN', { hour12: false }) : '--:--:--',
          level: (l.level || 'INFO').toUpperCase(),
          message: l.action || l.message || l.summary || JSON.stringify(l),
          moduleName: l.resource || l.module || '',
        }));
        setAuditLogs(mapped)
      }
    } finally {
      setAuditLogsRefreshing(false)
    }
  }

  // 高频定时器（受 refreshInterval 控制：5/10/30s 或关闭；默认 5s）
  useEffect(() => {
    if (!session || refreshInterval === 'off') return
    const ms = Number(refreshInterval) * 1000
    const timer = setInterval(() => loadHighFreqData(), ms)
    return () => clearInterval(timer)
  }, [session, refreshInterval])

  // 中频定时器（固定 30s，与用户偏好无关）
  useEffect(() => {
    if (!session) return
    const timer = setInterval(() => loadMidFreqData(), 30 * 1000)
    return () => clearInterval(timer)
  }, [session])

  // 低频定时器（固定 5min）
  useEffect(() => {
    if (!session) return
    const timer = setInterval(() => loadLowFreqData(), 5 * 60 * 1000)
    return () => clearInterval(timer)
  }, [session])

  // 页面切换时立即触发当前 tab 所需数据的 fetch（确保切换后数据新鲜）
  useEffect(() => {
    if (!session) return
    switch (tab) {
      case 'runtime':
        loadHighFreqData()
        break
      case 'users':
        loadUsers()
        break
      case 'modules':
      case 'storage':
      case 'server':
        loadMidFreqData()
        break
    }
    // tab/session 变化时触发；忽略 tier 函数闭包变化
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, session])

  // H44: 模块禁用宽限期倒计时（5分钟 = 300秒）
  useEffect(() => {
    if (!moduleGracePeriod) return
    const timer = setInterval(() => {
      setModuleGracePeriod(prev => {
        if (!prev) return null
        const next = prev.remainingSeconds - 1
        if (next <= 0) return null
        return { ...prev, remainingSeconds: next }
      })
    }, 1000)
    return () => clearInterval(timer)
  }, [moduleGracePeriod?.moduleName])

  // H44: 监听 WebSocket module_disabled 事件
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent).detail
      if (detail?.module) {
        setModuleGracePeriod({ moduleName: detail.module, remainingSeconds: 300 })
      }
    }
    window.addEventListener('rrt:module_disabled', handler)
    return () => window.removeEventListener('rrt:module_disabled', handler)
  }, [])

  // NM-01: Admin WebSocket 连接，实时接收模块禁用等事件。
  // A11（DES-20261001-01 §12.3）：同时消费 admin_alert_snapshot（连接建立时
  // 的活跃告警快照）与 admin_alert（open/update/resolved 增量事件）；
  // 页面 hidden 且浏览器通知已授权时，open 事件弹 Notification（不重复请求授权）。
  useEffect(() => {
    if (!session) return
    const token = localStorage.getItem('rrt_admin_token')
    if (!token) return
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${protocol}//${window.location.host}/ws?token=${encodeURIComponent(token)}`)
    ws.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data)
        if (data.type === 'module_disabled' && data.module) {
          setModuleGracePeriod({ moduleName: data.module, remainingSeconds: 300 })
          return
        }
        if (data.type === 'admin_alert_snapshot') {
          const alerts: AdminAlertInfo[] = data.payload?.alerts || []
          setAdminAlerts(alerts)
          return
        }
        if (data.type === 'admin_alert') {
          const alert: AdminAlertInfo = data.payload
          if (!alert?.id || !alert?.type) return
          if (alert.action === 'resolved') {
            // 恢复：从活跃列表移除，插入历史头部。
            setAdminAlerts(prev => prev.filter(a => a.id !== alert.id))
            setResolvedAlerts(prev => [{ ...alert }, ...prev].slice(0, 200))
            return
          }
          // open / update：活跃列表按 id 去重更新。
          setAdminAlerts(prev => {
            const idx = prev.findIndex(a => a.id === alert.id)
            if (idx >= 0) {
              const next = [...prev]
              next[idx] = alert
              return next
            }
            return [alert, ...prev]
          })
          // 桌面通知仅对新 open 且页面不可见时弹（update 刷屏无意义）。
          if (alert.action === 'open' && document.hidden &&
              typeof Notification !== 'undefined' && Notification.permission === 'granted') {
            try {
              const sevText = alert.severity === 'alert' ? '严重' : '警告'
              new Notification(`RidgeRiceTalk ${sevText}告警：${alert.type}`, { body: alert.message })
            } catch {
              // 通知构造失败不影响告警横幅
            }
          }
        }
      } catch {
        // Ignore non-JSON messages
      }
    }
    return () => ws.close()
  }, [session])

  // A11: 静默按钮（24h）。成功后本地把 mutedUntil 标到对应活跃告警上。
  const handleMuteAlert = async (id: string) => {
    setMutingAlertId(id)
    try {
      await muteAdminAlert(id, 24)
      const mutedUntil = new Date(Date.now() + 24 * 3600 * 1000).toISOString()
      setAdminAlerts(prev => prev.map(a => (a.id === id ? { ...a, mutedUntil } : a)))
    } catch (err: any) {
      alert('静默失败：' + (err?.message || '未知错误'))
    } finally {
      setMutingAlertId(null)
    }
  }

  const handleAdminLogout = async () => {
    try {
      await adminLogout()
    } catch (e) {
      // 即使 API 调用失败也清前端 state，避免用户卡住
      console.error('Logout API failed:', e)
    } finally {
      setSession(null)
    }
  }

  if (loading || bootstrapInitialized === null) {
    return (
      <div className="loading-screen">
        <div className="spinner" />
        <div style={{ color: 'var(--text-secondary)', fontSize: 14 }}>正在加载管理后台...</div>
      </div>
    )
  }

  if (!session) {
    return <LoginForm bootstrapInitialized={bootstrapInitialized} onSessionChange={setSession} />
  }

  return (
    <div className="admin-layout">
      {/* ISSUE-013: aria-live region for status announcements */}
      <div className="sr-only" role="status" aria-live="polite" aria-atomic="true">
        {dataError ? `数据加载异常：${dataError}` : ''}
      </div>
      <div className="admin-header">
        <div className="admin-header-title"><Server size={18} aria-hidden="true" /> RidgeRiceTalk 管理端</div>
        <div className="admin-header-actions">
          {dataError && <span role="alert" aria-live="assertive" style={{ color: 'var(--danger)', fontSize: 12, marginRight: 8 }} title={dataError}>数据加载异常</span>}
          <button className="icon-btn" title="刷新数据" onClick={loadAdminData} disabled={dataLoading}>
            <RefreshCw size={16} aria-hidden="true" style={dataLoading ? { animation: 'spin 1s linear infinite' } : undefined} />
          </button>
          <button className="icon-btn" title="切换主题" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
            {theme === 'dark' ? <Sun size={16} aria-hidden="true" /> : <Moon size={16} aria-hidden="true" />}
          </button>
          <button className="auth-btn" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', padding: '6px 12px', fontSize: 13 }} onClick={handleAdminLogout}>
            <LogOut size={14} aria-hidden="true" style={{ marginRight: 4, verticalAlign: 'middle' }} /> 退出登录
          </button>
        </div>
      </div>
      {/* H44: 模块禁用宽限期横幅 */}
      {moduleGracePeriod && (
        <div style={{ background: 'var(--warning, #f39c12)', color: '#fff', padding: '8px 16px', display: 'flex', alignItems: 'center', gap: 12, fontSize: 13 }}>
          <AlertTriangle size={16} aria-hidden="true" />
          <span>模块 <strong>{moduleGracePeriod.moduleName}</strong> 将在 <strong>{Math.floor(moduleGracePeriod.remainingSeconds / 60)}分{moduleGracePeriod.remainingSeconds % 60}秒</strong> 后完全禁用</span>
          <button className="track-btn" style={{ marginLeft: 'auto', background: 'rgba(255,255,255,0.2)', color: '#fff', border: 'none', padding: '4px 12px' }} onClick={async () => {
            // P1-2: 后端没有"立即禁用"语义——任何非 "enable" 的 action 都会走
            // ToggleModule(false)，而它会**重新种一个 5 分钟宽限期**
            // （admin/service.go ToggleModule）。所以这里既不谎称"已立即禁用"，
            // 也不把模块标成 disabled，只是按真实行为重启宽限期并如实告知。
            try {
              await toggleModule(moduleGracePeriod.moduleName, 'disable')
              setModuleGracePeriod({ moduleName: moduleGracePeriod.moduleName, remainingSeconds: 300 })
              alert('已开始禁用流程：模块将在 5 分钟宽限期结束后完全禁用')
            } catch (err: any) {
              alert('禁用失败：' + (err.message || '未知错误'))
            }
          }}>开始禁用（5 分钟宽限期）</button>
          <button className="track-btn" style={{ background: 'rgba(255,255,255,0.2)', color: '#fff', border: 'none', padding: '4px 12px' }} onClick={async () => {
            try {
              await toggleModule(moduleGracePeriod.moduleName, 'enable')
              setModules(modules.map(x => x.name === moduleGracePeriod.moduleName ? { ...x, status: 'running' } : x))
              setModuleGracePeriod(null)
            } catch (err: any) {
              alert('取消禁用失败: ' + (err.message || '未知错误'))
            }
          }}>取消</button>
        </div>
      )}
      <div className="admin-body">
        <Sidebar tab={tab} onTabChange={setTab} />
        <div className="admin-content">
          {tab === 'server' && (
            <ServerSettingsPanel
              config={config}
              setConfig={setConfig}
              networkConfig={networkConfig}
              setNetworkConfig={setNetworkConfig}
              networkConfigSaved={networkConfigSaved}
              setNetworkConfigSaved={setNetworkConfigSaved}
              dataLoading={dataLoading}
              session={session}
            />
          )}
          {tab === 'channels' && <ChannelsPanel users={users} />}
          {tab === 'users' && (
            <UsersPanel
              session={session}
              users={users}
              userTotal={userTotal}
              userSummary={userSummary}
              userPage={userPage}
              userKeyword={userKeyword}
              usersPerPage={usersPerPage}
              onPageChange={(p) => { setUserPage(p); loadUsers(p, userKeyword) }}
              onKeywordChange={(k) => { setUserKeyword(k) }}
              onUsersChange={setUsers}
              onReload={() => loadUsers(userPage, userKeyword)}
            />
          )}
          {tab === 'modules' && (
            <ModulesPanel
              modules={modules}
              setModules={setModules}
              auditLogs={auditLogs}
              setAuditLogs={setAuditLogs}
              refreshAuditLogsNow={refreshAuditLogsNow}
              auditLogsRefreshing={auditLogsRefreshing}
              moduleGracePeriod={moduleGracePeriod}
              setModuleGracePeriod={setModuleGracePeriod}
            />
          )}
          {tab === 'storage' && <StoragePanel config={config} storage={storage} />}
          {tab === 'runtime' && (
            <RuntimePanel
              performance={performance}
              systemDisk={systemDisk}
              storage={storage}
              auditLogs={auditLogs}
              refreshAuditLogsNow={refreshAuditLogsNow}
              auditLogsRefreshing={auditLogsRefreshing}
              refreshInterval={refreshInterval}
              setRefreshInterval={setRefreshInterval}
              dataLoading={dataLoading}
              adminAlerts={adminAlerts}
              resolvedAlerts={resolvedAlerts}
              onMuteAlert={handleMuteAlert}
              mutingAlertId={mutingAlertId}
            />
          )}
        </div>
      </div>
    </div>
  )
}
