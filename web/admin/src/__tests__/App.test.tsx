import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, act, fireEvent } from '@testing-library/react'
import App from '../App'
import * as apiCore from '../../../shared/api-core'

// App.tsx 的所有兄弟面板都会 import 同一个 api-core，因此这里把整模块打成 mock，
// 只保留被测代码真正会调用的那几个函数。
vi.mock('../../../shared/api-core', () => ({
  setTokenKey: vi.fn(),
  getAdminBootstrapStatus: vi.fn(),
  adminLogout: vi.fn(),
  getMe: vi.fn(),
  getAdminConfig: vi.fn(),
  getAdminRuntime: vi.fn(),
  getAdminModules: vi.fn(),
  getAdminStorage: vi.fn(),
  getAdminSystemUsage: vi.fn(),
  getAdminUsers: vi.fn(),
  getAdminLogs: vi.fn(),
  getAdminNetwork: vi.fn(),
  toggleModule: vi.fn(),
  // 面板里 import 了但本文件不直接断言的函数
  apiPost: vi.fn(),
  updateAdminConfig: vi.fn(),
  updateAdminNetwork: vi.fn(),
  detectAdminNetwork: vi.fn(),
  verifyAdminNetwork: vi.fn(),
  getAdminPorts: vi.fn(),
  restartAdminService: vi.fn(),
  adminLogin: vi.fn(),
  adminBootstrapVerify: vi.fn(),
  adminBootstrapRegister: vi.fn(),
}))

// jsdom 环境下 WebSocket 不一定存在；App 在拿到 session 后会建立 admin WS 连接。
class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  onmessage: ((ev: { data: string }) => void) | null = null
  close = vi.fn()
  constructor(public url: string) {
    FakeWebSocket.instances.push(this)
  }
}

describe('App（管理端外壳）', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('rrt_admin_token', 'admin-token')
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket as any)

    vi.mocked(apiCore.getAdminBootstrapStatus).mockResolvedValue({
      code: 'OK',
      data: { initialized: true, needsBootstrap: false },
    } as any)
    vi.mocked(apiCore.getMe).mockResolvedValue({
      code: 'OK',
      data: { user: { id: 'u1', username: 'owner', role: 'OWNER' } },
    } as any)
    vi.mocked(apiCore.getAdminConfig).mockResolvedValue({ code: 'OK', data: null } as any)
    vi.mocked(apiCore.getAdminRuntime).mockResolvedValue({ code: 'OK', data: null } as any)
    vi.mocked(apiCore.getAdminModules).mockResolvedValue({
      code: 'OK',
      data: { items: [{ name: 'bots', enabled: true, status: 'running', description: 'Bot 模块', cpu: 0, memory: 0 }] },
    } as any)
    vi.mocked(apiCore.getAdminStorage).mockResolvedValue({ code: 'OK', data: { items: [] } } as any)
    vi.mocked(apiCore.getAdminSystemUsage).mockResolvedValue({ code: 'OK', data: null } as any)
    vi.mocked(apiCore.getAdminUsers).mockResolvedValue({ code: 'OK', data: { items: [], total: 0, summary: {} } } as any)
    vi.mocked(apiCore.getAdminLogs).mockResolvedValue({ code: 'OK', data: { items: [] } } as any)
    vi.mocked(apiCore.getAdminNetwork).mockResolvedValue({ code: 'OK', data: { network: null } } as any)
    vi.mocked(apiCore.toggleModule).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  async function renderLoggedIn() {
    const utils = render(<App />)
    await screen.findByText('RidgeRiceTalk 管理端')
    return utils
  }

  async function showGracePeriodBanner() {
    // H44: 模块禁用宽限期横幅由 window 自定义事件或 admin WebSocket 消息驱动。
    await act(async () => {
      window.dispatchEvent(new CustomEvent('rrt:module_disabled', { detail: { module: 'bots' } }))
    })
    await screen.findByText(/将在/)
  }

  // 注意：登录页的 <h1> 同样含「RidgeRiceTalk 管理端」字样，所以登录态的断言
  // 必须挑「只有登录后才有」的元素（侧边栏导航 / 退出登录），不能只看品牌名。
  it('未登录时只显示登录表单，登录后才有的外壳元素不存在', async () => {
    localStorage.removeItem('rrt_admin_token')
    vi.mocked(apiCore.getMe).mockResolvedValue({ code: 'OK', data: {} } as any)
    render(<App />)

    expect(await screen.findByPlaceholderText(/管理员邮箱/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /退出登录/ })).not.toBeInTheDocument()
    expect(screen.queryByText('频道管理')).not.toBeInTheDocument()
    expect(screen.queryByText('运行监控')).not.toBeInTheDocument()
  })

  it('恢复会话后渲染管理端外壳（侧边栏与退出登录可见）', async () => {
    await renderLoggedIn()

    expect(apiCore.getMe).toHaveBeenCalled()
    expect(screen.getByRole('heading', { name: '服务器设置' })).toBeInTheDocument()
    expect(screen.getByText('频道管理')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /退出登录/ })).toBeInTheDocument()
    expect(screen.queryByPlaceholderText(/管理员邮箱/)).not.toBeInTheDocument()
  })

  // P1-2 回归锁：后端没有「立即禁用」语义 —— 任何 disable 动作都会重新种一个
  // 5 分钟宽限期。横幅按钮不能再写「立即禁用」骗运维。
  it('宽限期横幅的按钮文案是「开始禁用（5 分钟宽限期）」，不是「立即禁用」（P1-2）', async () => {
    await renderLoggedIn()
    await showGracePeriodBanner()

    expect(screen.getByText(/后完全禁用/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '开始禁用（5 分钟宽限期）' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /立即禁用/ })).not.toBeInTheDocument()
  })

  it('点击「开始禁用」按真实语义重启宽限期，不谎报已禁用（P1-2）', async () => {
    await renderLoggedIn()
    await showGracePeriodBanner()

    fireEvent.click(screen.getByRole('button', { name: '开始禁用（5 分钟宽限期）' }))

    await waitFor(() => {
      expect(apiCore.toggleModule).toHaveBeenCalledWith('bots', 'disable')
    })
    // 提示如实说明「宽限期结束后才完全禁用」，不出现「已立即禁用」
    const alertText = String(vi.mocked(window.alert).mock.calls[0]?.[0] ?? '')
    expect(alertText).toContain('5 分钟宽限期')
    expect(alertText).not.toContain('立即')
    // 横幅仍在（正在倒计时），说明前端没有假装模块已经停掉
    expect(screen.getByText(/后完全禁用/)).toBeInTheDocument()
  })

  it('「取消」调用 enable 并收起横幅（P1-2）', async () => {
    await renderLoggedIn()
    await showGracePeriodBanner()

    fireEvent.click(screen.getByRole('button', { name: '取消' }))

    await waitFor(() => {
      expect(apiCore.toggleModule).toHaveBeenCalledWith('bots', 'enable')
    })
    await waitFor(() => {
      expect(screen.queryByText(/后完全禁用/)).not.toBeInTheDocument()
    })
  })

  it('admin WebSocket 的 module_disabled 消息也能拉起横幅（NM-01）', async () => {
    await renderLoggedIn()

    await waitFor(() => {
      expect(FakeWebSocket.instances.length).toBeGreaterThan(0)
    })
    const ws = FakeWebSocket.instances[0]
    expect(ws.url).toContain('/ws?token=admin-token')

    await act(async () => {
      ws.onmessage?.({ data: JSON.stringify({ type: 'module_disabled', module: 'voice' }) })
    })

    expect(await screen.findByText(/voice/)).toBeInTheDocument()
    expect(screen.getByText(/后完全禁用/)).toBeInTheDocument()
  })

  it('无关的 WebSocket 消息不会拉起横幅，也不会因非 JSON 消息崩溃', async () => {
    await renderLoggedIn()
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0))
    const ws = FakeWebSocket.instances[0]

    await act(async () => {
      ws.onmessage?.({ data: JSON.stringify({ type: 'presence_update', user: 'x' }) })
      ws.onmessage?.({ data: 'not-json' })
    })

    expect(screen.queryByText(/后完全禁用/)).not.toBeInTheDocument()
  })

  it('禁用失败时提示错误且不假装进入宽限期', async () => {
    vi.mocked(apiCore.toggleModule).mockRejectedValue(new Error('MODULE_NOT_FOUND'))
    await renderLoggedIn()
    await showGracePeriodBanner()

    fireEvent.click(screen.getByRole('button', { name: '开始禁用（5 分钟宽限期）' }))

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('禁用失败'))
    })
    expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('MODULE_NOT_FOUND'))
  })
})
