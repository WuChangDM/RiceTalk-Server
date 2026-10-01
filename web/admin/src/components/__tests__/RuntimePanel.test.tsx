import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RuntimePanel from '../RuntimePanel'
import * as apiCore from '../../../../shared/api-core'

vi.mock('../../../../shared/api-core', () => ({
  exportAdminAuditLogs: vi.fn(),
}))

const performance = {
  cpu: { value: 12.5, simulated: false },
  memory: { percent: 40.25, total: 16384, used: 6594, available: 9790 },
  disk: 3.5,
  network: { rx: 2048, tx: 4096 },
  uptime: 93784,
  online_users: 7,
  active_voices: 2,
  message_rate: 41,
  websocket_conns: 9,
} as any

const auditLogs = [
  { time: '10:00:00', level: 'INFO', message: 'admin_login' },
  { time: '10:01:00', level: 'WARN', message: 'admin_update_config' },
]

function renderPanel(overrides: Partial<Parameters<typeof RuntimePanel>[0]> = {}) {
  const refreshAuditLogsNow = vi.fn()
  const setRefreshInterval = vi.fn()
  const onMuteAlert = vi.fn()
  return render(
    <RuntimePanel
      performance={performance}
      systemDisk={{ total: 100 * 1024, used: 40 * 1024, free: 60 * 1024, percent: 40 }}
      storage={[{ id: 'logs', name: '日志', size: 100, files: 5, description: '' }]}
      auditLogs={auditLogs}
      refreshAuditLogsNow={refreshAuditLogsNow}
      auditLogsRefreshing={false}
      refreshInterval="5"
      setRefreshInterval={setRefreshInterval}
      dataLoading={false}
      adminAlerts={[]}
      resolvedAlerts={[]}
      onMuteAlert={onMuteAlert}
      mutingAlertId={null}
      {...overrides}
    />,
  )
}

// A11：活跃/历史告警样例（字段契约见 server internal/admin/alerts.go AlertEventPayload）
const activeDiskAlert = {
  id: 'alert_d1',
  type: 'disk' as const,
  severity: 'alert' as const,
  message: '磁盘用量 91.3%（阈值 85%），路径 /',
  firstSeenAt: '2026-10-01T04:00:00Z',
  lastSeenAt: '2026-10-01T04:05:00Z',
  resolvedAt: null,
  mutedUntil: null,
}
const activeLiveKitWarn = {
  id: 'alert_l1',
  type: 'livekit' as const,
  severity: 'warn' as const,
  message: 'LiveKit 信令端口 127.0.0.1:7880 探测失败',
  firstSeenAt: '2026-10-01T03:00:00Z',
  lastSeenAt: '2026-10-01T03:30:00Z',
  resolvedAt: null,
  mutedUntil: null,
}
const resolvedHealthAlert = {
  id: 'alert_h1',
  type: 'health' as const,
  severity: 'alert' as const,
  message: '本机健康检查失败（API 无响应）',
  firstSeenAt: '2026-10-01T01:00:00Z',
  lastSeenAt: '2026-10-01T01:05:00Z',
  resolvedAt: '2026-10-01T01:06:00Z',
  mutedUntil: null,
}

describe('RuntimePanel', () => {
  beforeEach(() => {
    vi.mocked(apiCore.exportAdminAuditLogs).mockResolvedValue(undefined)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  // P0-5 回归锁：后端 GET /api/admin/audit-logs/export 早已实现，前端按钮曾经是
  // `disabled` 的占位（接口白做）。现在按钮必须可用，点击必须真的触发导出。
  // 若有人把 disabled 加回去，第一条断言失败；若有人把 onClick 摘掉，第二条失败。
  it('导出日志按钮可用，点击后触发审计日志导出（P0-5）', async () => {
    renderPanel()

    const exportBtn = screen.getByRole('button', { name: /导出日志/ })
    expect(exportBtn).toBeEnabled()

    fireEvent.click(exportBtn)

    await waitFor(() => {
      expect(apiCore.exportAdminAuditLogs).toHaveBeenCalledTimes(1)
    })
    expect(exportBtn).toBeEnabled()
  })

  it('导出进行中按钮显示「导出中...」并禁用，避免重复导出', async () => {
    let resolveExport: () => void = () => {}
    vi.mocked(apiCore.exportAdminAuditLogs).mockImplementation(
      () => new Promise<void>(resolve => { resolveExport = resolve }),
    )
    renderPanel()

    fireEvent.click(screen.getByRole('button', { name: /导出日志/ }))

    const busy = await screen.findByRole('button', { name: /导出中/ })
    expect(busy).toBeDisabled()

    resolveExport()
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /导出日志/ })).toBeEnabled()
    })
  })

  it('导出失败时明确报错，不静默吞掉', async () => {
    vi.mocked(apiCore.exportAdminAuditLogs).mockRejectedValue(new Error('HTTP 500'))
    renderPanel()

    fireEvent.click(screen.getByRole('button', { name: /导出日志/ }))

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('导出失败'))
    })
    expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('HTTP 500'))
  })

  it('「查看更多日志」仍是明确的未实现占位（与可用的导出按钮区分开）', () => {
    renderPanel()

    const placeholder = screen.getByRole('button', { name: '查看更多日志' })
    expect(placeholder).toBeDisabled()
    expect(placeholder).toHaveAttribute('title', '暂未实现')
  })

  it('渲染审计日志与运行时统计', () => {
    renderPanel()

    expect(screen.getByText(/admin_login/)).toBeInTheDocument()
    expect(screen.getByText(/admin_update_config/)).toBeInTheDocument()
    expect(screen.getByText('12.5%')).toBeInTheDocument()
    expect(screen.getByText('40.3%')).toBeInTheDocument()
    expect(screen.getByText('7')).toBeInTheDocument()
    expect(screen.getByText('1d 2h 3m')).toBeInTheDocument()
  })

  it('无审计日志时给出空状态而不是空白区域', () => {
    renderPanel({ auditLogs: [] })
    expect(screen.getByText('暂无审计日志')).toBeInTheDocument()
  })

  it('加载中与无数据两种空状态可区分', () => {
    const { unmount } = renderPanel({ performance: null, dataLoading: true })
    expect(screen.getByText('加载运行时数据中...')).toBeInTheDocument()
    unmount()

    renderPanel({ performance: null, dataLoading: false })
    expect(screen.getByText('暂无运行时数据')).toBeInTheDocument()
  })

  it('刷新审计日志按钮把请求交给父级，并在刷新中禁用', () => {
    const refreshAuditLogsNow = vi.fn()
    const { rerender } = renderPanel({ refreshAuditLogsNow })

    fireEvent.click(screen.getByRole('button', { name: /刷新审计日志/ }))
    expect(refreshAuditLogsNow).toHaveBeenCalledTimes(1)
    // 刷新走父级的 audit-logs 拉取，不是 CSV 导出
    expect(apiCore.exportAdminAuditLogs).not.toHaveBeenCalled()

    rerender(
      <RuntimePanel
        performance={performance}
        systemDisk={null}
        storage={[]}
        auditLogs={auditLogs}
        refreshAuditLogsNow={refreshAuditLogsNow}
        auditLogsRefreshing={true}
        refreshInterval="5"
        setRefreshInterval={vi.fn()}
        dataLoading={false}
        adminAlerts={[]}
        resolvedAlerts={[]}
        onMuteAlert={vi.fn()}
        mutingAlertId={null}
      />,
    )
    expect(screen.getByRole('button', { name: /刷新中/ })).toBeDisabled()
  })

  it('自动刷新间隔选择把新值交回父级', () => {
    const setRefreshInterval = vi.fn()
    renderPanel({ setRefreshInterval })

    fireEvent.change(screen.getByLabelText('自动刷新'), { target: { value: 'off' } })

    expect(setRefreshInterval).toHaveBeenCalledWith('off')
  })

  // ── A11（DES-20261001-01 §12.3）：告警横幅 / 静默按钮 / 历史列表 ──

  it('A11：无活跃告警时不渲染横幅（role=alert 区域不存在）', () => {
    renderPanel()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('A11：活跃告警渲染横幅，severity=alert 用红色系、warn 用黄色系（取色注释性断言 + 文案）', () => {
    renderPanel({ adminAlerts: [activeDiskAlert, activeLiveKitWarn] })

    const banner = screen.getByTestId('admin-alert-banner-disk')
    expect(banner).toBeInTheDocument()
    // severity=alert 红色（--danger 回退 #e74c3c；jsdom 不展开 CSS 变量，
    // inline style 保留原始字符串，直接断言回退色）
    expect(banner.style.background).toContain('#e74c3c')
    // severity=warn 黄色
    const warn = screen.getByTestId('admin-alert-banner-livekit')
    expect(warn.style.background).toContain('#f39c12')
    // 文案含 severity（strong 节点）与「类型：消息」（span 的 textContent）
    expect(screen.getByText('严重')).toBeInTheDocument()
    expect(screen.getByText('警告')).toBeInTheDocument()
    expect(screen.getByText(/· 磁盘：磁盘用量 91.3%/)).toBeInTheDocument()
    expect(screen.getByText(/· LiveKit：LiveKit 信令端口/)).toBeInTheDocument()
  })

  it('A11：静默按钮把告警 id 交给父级，静默中禁用防重复点击', () => {
    const onMuteAlert = vi.fn()
    const { rerender } = renderPanel({ adminAlerts: [activeDiskAlert], onMuteAlert })

    const btn = screen.getByRole('button', { name: /静默 24h/ })
    fireEvent.click(btn)
    expect(onMuteAlert).toHaveBeenCalledWith('alert_d1')

    // 父级把 mutingAlertId 置为该 id → 按钮进入「静默中...」并禁用
    rerender(
      <RuntimePanel
        performance={performance}
        systemDisk={{ total: 100 * 1024, used: 40 * 1024, free: 60 * 1024, percent: 40 }}
        storage={[]}
        auditLogs={auditLogs}
        refreshAuditLogsNow={vi.fn()}
        auditLogsRefreshing={false}
        refreshInterval="5"
        setRefreshInterval={vi.fn()}
        dataLoading={false}
        adminAlerts={[activeDiskAlert]}
        resolvedAlerts={[]}
        onMuteAlert={onMuteAlert}
        mutingAlertId="alert_d1"
      />,
    )
    const busy = screen.getByRole('button', { name: /静默中/ })
    expect(busy).toBeDisabled()
  })

  it('A11：已静默的告警按钮禁用并显示静默截止时间', () => {
    renderPanel({
      adminAlerts: [{ ...activeDiskAlert, mutedUntil: '2026-10-02T04:00:00Z' }],
    })

    const btn = screen.getByRole('button', { name: /静默 24h/ })
    expect(btn).toBeDisabled()
    expect(screen.getByText(/已静默至/)).toBeInTheDocument()
  })

  it('A11：历史告警默认折叠，展开后列出时间/类型/消息/恢复时间', () => {
    renderPanel({ resolvedAlerts: [resolvedHealthAlert] })

    // 折叠态：条目不可见
    expect(screen.queryByTestId('alert-history-item')).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId('alert-history-toggle'))

    const item = screen.getByTestId('alert-history-item')
    expect(item).toBeInTheDocument()
    expect(screen.getByText(/服务健康/)).toBeInTheDocument()
    expect(screen.getByText(/本机健康检查失败/)).toBeInTheDocument()
    expect(screen.getByText(/恢复于/)).toBeInTheDocument()
  })

  it('A11：历史告警为空时展开给出空态而不是空白区域', () => {
    renderPanel()
    fireEvent.click(screen.getByTestId('alert-history-toggle'))
    expect(screen.getByText('暂无历史告警')).toBeInTheDocument()
  })
})
