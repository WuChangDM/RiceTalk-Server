import { useState } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import ServerSettingsPanel from '../ServerSettingsPanel'
import * as apiCore from '../../../../shared/api-core'
import { EMPTY_NETWORK_CONFIG } from '../../types/admin'
import type { NetworkConfig } from '../../types/admin'
import type { ServerConfigSnapshot } from '../../../../shared/types'

vi.mock('../../../../shared/api-core', () => ({
  updateAdminConfig: vi.fn(),
  detectAdminNetwork: vi.fn(),
  verifyAdminNetwork: vi.fn(),
  updateAdminNetwork: vi.fn(),
  getAdminPorts: vi.fn(),
  restartAdminService: vi.fn(),
  getAdminConfig: vi.fn(),
}))

const baseConfig: ServerConfigSnapshot = {
  serverName: 'RidgeRiceTalk',
  allowRegister: true,
  apiPort: 8080,
  adminPort: 9090,
  livekitPort: 7880,
  vpnPort: 7788,
  publicAddress: 'https://voice.example.com',
  maxUsers: 100,
  deployMode: 'native',
  maxFileSize: 0,
  maxStorageGB: 10,
}

const ownerSession = { id: 'me', username: 'owner', role: 'OWNER' } as any

const ports = [
  {
    key: 'api', label: 'API / Web 端口', protocol: 'tcp' as const,
    internalPort: 8080, externalPort: 443, external: true,
    description: 'HTTP API 与网页端', natRequired: false,
  },
  {
    key: 'lkUdp', label: 'LiveKit UDP 端口', protocol: 'udp' as const,
    internalPort: 7882, externalPort: 7882, external: true,
    description: '语音媒体流', natRequired: true,
  },
]

// ServerSettingsPanel 是受控组件（config / networkConfig 都是 props），
// 必须用一个持有 state 的外壳渲染，才能验证「输入 → 保存」的真实链路。
function Harness({
  initialConfig = baseConfig,
  initialNetwork = EMPTY_NETWORK_CONFIG,
  session = ownerSession,
}: {
  initialConfig?: ServerConfigSnapshot | null
  initialNetwork?: NetworkConfig
  session?: any
}) {
  const [config, setConfig] = useState<ServerConfigSnapshot | null>(initialConfig)
  const [networkConfig, setNetworkConfig] = useState<NetworkConfig>(initialNetwork)
  const [networkConfigSaved, setNetworkConfigSaved] = useState<NetworkConfig>(initialNetwork)
  return (
    <ServerSettingsPanel
      config={config}
      setConfig={setConfig as any}
      networkConfig={networkConfig}
      setNetworkConfig={setNetworkConfig}
      networkConfigSaved={networkConfigSaved}
      setNetworkConfigSaved={setNetworkConfigSaved}
      dataLoading={false}
      session={session}
    />
  )
}

const tab = (name: string) => fireEvent.click(screen.getByRole('button', { name }))

describe('ServerSettingsPanel', () => {
  beforeEach(() => {
    vi.mocked(apiCore.getAdminPorts).mockResolvedValue(ports as any)
    vi.mocked(apiCore.updateAdminNetwork).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.mocked(apiCore.updateAdminConfig).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  // P0-1 回归锁：getAdminPorts 解包错（读信封的 .ports 而不是 .data.ports）时，
  // 端口列表恒为空，整个「端口配置」tab 显示「暂无端口数据」且编辑弹窗永远打不开。
  // 这里断言端口表格真的渲染出数据 —— 解包回归时这些文本都不存在。
  it('端口配置 tab 渲染真实的端口列表，而不是「暂无端口数据」（P0-1）', async () => {
    render(<Harness />)

    tab('端口配置')

    await waitFor(() => {
      expect(apiCore.getAdminPorts).toHaveBeenCalled()
    })
    expect(await screen.findByText('API / Web 端口')).toBeInTheDocument()
    expect(screen.getByText('LiveKit UDP 端口')).toBeInTheDocument()
    // 内部端口与外部端口两列都要真实渲染（外部端口列回归成空/「-」时数量会变）
    expect(screen.getAllByText('7882')).toHaveLength(2)
    expect(screen.queryByText('暂无端口数据')).not.toBeInTheDocument()
    // natRequired 的端口触发 NAT 提示
    expect(screen.getByText(/检测到 NAT 网络/)).toBeInTheDocument()
  })

  it('端口列表加载失败时明确报错，不静默失败', async () => {
    vi.mocked(apiCore.getAdminPorts).mockRejectedValue(new Error('HTTP 500'))
    render(<Harness />)

    tab('端口配置')

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('加载端口列表失败'))
    })
    // 加载失败不能被当成「端口就是 0 个」，重启入口仍应可达
    expect(screen.getByRole('button', { name: /重启服务/ })).toBeInTheDocument()
  })

  it('端口列表为空时显示空状态', async () => {
    vi.mocked(apiCore.getAdminPorts).mockResolvedValue([] as any)
    render(<Harness />)

    tab('端口配置')

    expect(await screen.findByText('暂无端口数据')).toBeInTheDocument()
  })

  it('编辑外部端口按 key 精确映射到 NetworkConfig 字段并保存', async () => {
    render(<Harness />)
    tab('端口配置')
    await screen.findByText('API / Web 端口')

    const row = screen.getByText('API / Web 端口').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: '编辑' }))

    const dialog = screen.getByRole('dialog', { name: /编辑端口/ })
    fireEvent.change(within(dialog).getByLabelText('外部端口'), { target: { value: '8443' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() => {
      expect(apiCore.updateAdminNetwork).toHaveBeenCalledTimes(1)
    })
    const sent = vi.mocked(apiCore.updateAdminNetwork).mock.calls[0][0] as NetworkConfig
    expect(sent.externalHttpPort).toBe(8443)
    // 只改 api 对应的字段，其他端口不受牵连
    expect(sent.externalMediaUdpPort).toBe(EMPTY_NETWORK_CONFIG.externalMediaUdpPort)
    expect(sent.externalAdminPort).toBe(EMPTY_NETWORK_CONFIG.externalAdminPort)
  })

  it('NAT 环境下外部端口与内部端口不一致时拒绝保存', async () => {
    render(<Harness />)
    tab('端口配置')
    await screen.findByText('LiveKit UDP 端口')

    const row = screen.getByText('LiveKit UDP 端口').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: '编辑' }))
    const dialog = screen.getByRole('dialog', { name: /编辑端口/ })
    fireEvent.change(within(dialog).getByLabelText('外部端口'), { target: { value: '8443' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('NAT 环境下外部端口必须与内部端口一致'))
    })
    expect(apiCore.updateAdminNetwork).not.toHaveBeenCalled()
  })

  // P0-2 回归锁：后端 VerifyNetwork 返回顶层 {success, result}（无信封），
  // 前端曾读 res.data?.result → 结果区永不显示。解包写回去，下面的文本即消失。
  it('「校验配置」把顶层 result 渲染出来（P0-2）', async () => {
    vi.mocked(apiCore.verifyAdminNetwork).mockResolvedValue({
      success: true,
      result: {
        serverUrl: 'https://voice.example.com',
        liveKitUrl: 'wss://voice.example.com/livekit',
        notes: ['UDP 7882 未检测到开放'],
      },
    } as any)
    render(<Harness />)

    tab('网络配置')
    fireEvent.click(screen.getByRole('button', { name: '校验配置' }))

    expect(await screen.findByText(/Server URL: https:\/\/voice\.example\.com/)).toBeInTheDocument()
    expect(screen.getByText(/LiveKit URL: wss:\/\/voice\.example\.com\/livekit/)).toBeInTheDocument()
    expect(screen.getByText('UDP 7882 未检测到开放')).toBeInTheDocument()
  })

  it('「校验配置」的失败提示走 alert，不伪装成校验通过', async () => {
    vi.mocked(apiCore.verifyAdminNetwork).mockRejectedValue(new Error('network error'))
    render(<Harness />)

    tab('网络配置')
    fireEvent.click(screen.getByRole('button', { name: '校验配置' }))

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('校验失败'))
    })
    expect(screen.queryByText(/Server URL:/)).not.toBeInTheDocument()
  })

  it('自动探测网络把公网 IP 写入外部地址并展示探测结果', async () => {
    vi.mocked(apiCore.detectAdminNetwork).mockResolvedValue({
      success: true,
      result: { publicIpv4: '203.0.113.9', localIpv4s: ['192.168.1.20'], upnpSupported: true },
      recommendations: [{ method: '端口映射', priority: 1, reason: '路由器已支持 UPnP' }],
    } as any)
    render(<Harness />)

    tab('网络配置')
    fireEvent.click(screen.getByRole('button', { name: '自动探测网络' }))

    await waitFor(() => {
      expect(screen.getByText(/公网 IPv4: 203\.0\.113\.9/)).toBeInTheDocument()
    })
    expect(screen.getByText(/本地 IPv4: 192\.168\.1\.20/)).toBeInTheDocument()
    expect((screen.getByLabelText('外部域名 / 公网 IP') as HTMLInputElement).value).toBe('203.0.113.9')
    expect(screen.getByText('端口映射')).toBeInTheDocument()
  })

  // P0-4 回归锁：三个端口输入框曾经「可编辑可保存但后端不接收」。
  // 前端这一侧的表现是保存时 payload 里必须带上 apiPort/livekitPort/vpnPort，
  // 且是用户在输入框里填的那个值（改了输入框不生效 = 回归）。
  it('基本配置保存时带上三个端口字段（P0-4 前端侧）', async () => {
    render(<Harness />)

    fireEvent.change(screen.getByLabelText('服务端口'), { target: { value: '9091' } })
    fireEvent.change(screen.getByLabelText('语音端口 (LiveKit)'), { target: { value: '7881' } })
    fireEvent.change(screen.getByLabelText('虚拟局域网端口'), { target: { value: '7789' } })
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))

    await waitFor(() => {
      expect(apiCore.updateAdminConfig).toHaveBeenCalledTimes(1)
    })
    const sent = vi.mocked(apiCore.updateAdminConfig).mock.calls[0][0] as ServerConfigSnapshot
    expect(sent).toEqual(expect.objectContaining({
      apiPort: 9091,
      livekitPort: 7881,
      vpnPort: 7789,
    }))
    expect(screen.getByLabelText('服务端口')).toHaveValue(9091)
  })

  it('基本配置保存失败时明确报错', async () => {
    vi.mocked(apiCore.updateAdminConfig).mockRejectedValue(new Error('MAX_USERS_INVALID'))
    render(<Harness />)

    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('MAX_USERS_INVALID'))
    })
  })

  it('重启服务按钮只对 Owner 显示（Break-Glass 保护的高危操作）', async () => {
    const { unmount } = render(<Harness />)
    tab('端口配置')
    await waitFor(() => expect(apiCore.getAdminPorts).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: /重启服务/ })).toBeInTheDocument()
    unmount()

    render(<Harness session={{ id: 'a1', username: 'admin', role: 'ADMIN' }} />)
    tab('端口配置')
    await waitFor(() => expect(apiCore.getAdminPorts).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: /重启服务/ })).not.toBeInTheDocument()
  })

  it('配置数据缺失时基本配置 tab 给出空状态', () => {
    render(<Harness initialConfig={null} />)
    expect(screen.getByText('暂无配置数据')).toBeInTheDocument()
  })

  it('networkConfig 有未保存改动时保存按钮才可用，保存后交给 updateAdminNetwork', async () => {
    render(<Harness />)
    tab('网络配置')

    const saveBtn = screen.getByRole('button', { name: '保存更改' })
    expect(saveBtn).toBeDisabled()

    fireEvent.change(screen.getByLabelText('外部 HTTP 端口'), { target: { value: '8443' } })
    await waitFor(() => {
      expect(screen.getByRole('button', { name: '保存更改' })).toBeEnabled()
    })

    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => {
      expect(apiCore.updateAdminNetwork).toHaveBeenCalledTimes(1)
    })
    const sent = vi.mocked(apiCore.updateAdminNetwork).mock.calls[0][0] as NetworkConfig
    expect(sent.externalHttpPort).toBe(8443)
    // 保存成功后不再算「有未保存改动」
    await waitFor(() => {
      expect(screen.getByRole('button', { name: '保存更改' })).toBeDisabled()
    })
  })
})
