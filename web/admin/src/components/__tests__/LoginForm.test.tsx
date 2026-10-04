import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import LoginForm from '../LoginForm'
import * as apiCore from '../../../../shared/api-core'

vi.mock('../../../../shared/api-core', () => ({
  adminLogin: vi.fn(),
  adminBootstrapVerify: vi.fn(),
  adminBootstrapRegister: vi.fn(),
  detectAdminNetwork: vi.fn(),
  updateAdminNetwork: vi.fn(),
  getServerInfo: vi.fn(),
}))

describe('LoginForm', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders login form when bootstrap is initialized', () => {
    render(<LoginForm bootstrapInitialized={true} onSessionChange={vi.fn()} />)
    expect(screen.getByPlaceholderText(/邮箱/i)).toBeInTheDocument()
    expect(screen.getByPlaceholderText(/密码/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /登录/i })).toBeInTheDocument()
  })

  it('renders bootstrap flow when bootstrap is not initialized', () => {
    render(<LoginForm bootstrapInitialized={false} onSessionChange={vi.fn()} />)
    expect(screen.getByLabelText(/一次性设置令牌/i)).toBeInTheDocument()
  })

  it('calls adminLogin and stores token on submit', async () => {
    const mockAdminLogin = vi.mocked(apiCore.adminLogin).mockResolvedValue({
      code: 'OK',
      data: {
        accessToken: 'test-token-123',
        token: 'test-token-123',
        user: { id: 'u1', username: 'admin', role: 'admin' },
      },
    } as any)

    const onSessionChange = vi.fn()
    render(<LoginForm bootstrapInitialized={true} onSessionChange={onSessionChange} />)

    fireEvent.change(screen.getByPlaceholderText(/邮箱/i), { target: { value: 'admin@example.com' } })
    fireEvent.change(screen.getByPlaceholderText(/密码/i), { target: { value: 'password' } })
    fireEvent.click(screen.getByRole('button', { name: /登录/i }))

    await waitFor(() => {
      expect(mockAdminLogin).toHaveBeenCalledWith('admin@example.com', 'password')
      expect(localStorage.getItem('rrt_admin_token')).toBe('test-token-123')
      expect(onSessionChange).toHaveBeenCalledWith({ id: 'u1', username: 'admin', role: 'admin' })
    })
  })

  it('displays login error when adminLogin fails', async () => {
    vi.mocked(apiCore.adminLogin).mockRejectedValue(new Error('Invalid credentials'))

    render(<LoginForm bootstrapInitialized={true} onSessionChange={vi.fn()} />)

    fireEvent.change(screen.getByPlaceholderText(/邮箱/i), { target: { value: 'bad@example.com' } })
    fireEvent.change(screen.getByPlaceholderText(/密码/i), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByRole('button', { name: /登录/i }))

    await waitFor(() => {
      expect(screen.getByText(/Invalid credentials/i)).toBeInTheDocument()
    })
  })

  it('U14: bootstrap 网络步初值从 /server/info 推导而非 443+HTTPS 假值', async () => {
    vi.mocked(apiCore.adminBootstrapVerify).mockResolvedValue({
      code: 'OK',
      data: { valid: true },
    } as any)
    vi.mocked(apiCore.adminBootstrapRegister).mockResolvedValue({
      code: 'OK',
      data: { accessToken: 'tk-123', user: { id: 'u1', username: 'owner', role: 'OWNER' } },
    } as any)
    vi.mocked(apiCore.getServerInfo).mockResolvedValue({
      // 真实契约：/api/server/info 返回裸字段对象（无 {code,data} 信封）
      state: 'uninitialized', initialized: false, name: 'RidgeRiceTalk', version: '0.2.2',
      serverUrl: '', livekitUrl: 'ws://192.168.31.187:7880/livekit', livekitPort: 17880, mediaUdpPort: 17882,
      webVoiceEnabled: false, clientAccessEnabled: true, useHttps: false,
      apiPort: 18080, adminPort: 19090,
    } as any)

    render(<LoginForm bootstrapInitialized={false} onSessionChange={vi.fn()} />)

    // step1：令牌验证
    fireEvent.change(screen.getByLabelText(/一次性设置令牌/i), { target: { value: 'tok-123' } })
    fireEvent.click(screen.getByRole('button', { name: /验证/i }))
    await waitFor(() => expect(screen.getByLabelText(/用户名/i)).toBeInTheDocument())

    // step2：创建 Owner
    fireEvent.change(screen.getByLabelText(/用户名/i), { target: { value: 'owner' } })
    fireEvent.change(screen.getByLabelText(/邮箱/i), { target: { value: 'owner@example.com' } })
    fireEvent.change(screen.getByLabelText(/密码/i), { target: { value: 'password123' } })
    fireEvent.click(screen.getByRole('button', { name: /下一步：配置网络/i }))

    // step3：网络步初值应为 /server/info 的实际值，而非 443/HTTPS
    await waitFor(() => {
      expect(screen.getByLabelText(/HTTP端口/i)).toHaveValue(18080)
      expect(screen.getByLabelText(/LiveKit WS端口/i)).toHaveValue(17880)
      expect(screen.getByLabelText(/媒体UDP端口/i)).toHaveValue(17882)
      expect(screen.getByLabelText(/使用 HTTPS/i)).not.toBeChecked()
    })
    // /server/info 被调用过一次
    expect(apiCore.getServerInfo).toHaveBeenCalledTimes(1)
  })
})
