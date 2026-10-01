import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  setApiBase, resetApiCoreForTests,
  getAdminChannels, createAdminChannel, updateAdminChannel, deleteAdminChannel,
  restartAdminService, getAdminPorts, verifyAdminNetwork, exportAdminAuditLogs,
} from '../../../shared/api-core'

// P1-1 / P1-6 的接口层回归测试。
// - P1-1：管理后台频道管理必须打到 /api/admin/channels（admin 专用接口），
//         而不是成员级 /api/channels。
// - P1-6：服务重启的提示文案来自信封的 data.message；信封顶层 message 恒为
//         "success"，不能被当成提示文案展示。

// api() 返回完整信封 {code, message, data, meta}
const envelope = (data: unknown, topLevelMessage = 'success') => ({
  code: 'OK',
  message: topLevelMessage,
  data,
  meta: {},
})

function okResponse(payload: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => payload,
  } as unknown as Response
}

const fetchMock = vi.fn()

function lastCall() {
  const call = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]
  const [url, init] = call as [string, RequestInit]
  return { url, init }
}

describe('api-core admin 接口', () => {
  beforeEach(() => {
    resetApiCoreForTests()
    setApiBase('http://api.test')
    fetchMock.mockReset()
    fetchMock.mockResolvedValue(okResponse(envelope(null)))
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('getAdminChannels 打到 /api/admin/channels', async () => {
    fetchMock.mockResolvedValue(
      okResponse(envelope([{ id: 'c1', name: '综合', type: 'text' }])),
    )

    const res = await getAdminChannels()

    expect(lastCall().url).toBe('http://api.test/api/admin/channels')
    expect(lastCall().init.method).toBe('GET')
    expect((res as any).data).toHaveLength(1)
  })

  it('createAdminChannel 以 POST 打到 /api/admin/channels', async () => {
    await createAdminChannel({ name: '新语音', type: 'voice', audioQuality: 'high' })

    const { url, init } = lastCall()
    expect(url).toBe('http://api.test/api/admin/channels')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({
      name: '新语音',
      type: 'voice',
      audioQuality: 'high',
    })
  })

  it('updateAdminChannel 用 channelId 路径参数打 PATCH', async () => {
    await updateAdminChannel('c2', { audioQuality: 'ultra' })

    const { url, init } = lastCall()
    expect(url).toBe('http://api.test/api/admin/channels/c2')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(String(init.body))).toEqual({ audioQuality: 'ultra' })
  })

  it('deleteAdminChannel 用 channelId 路径参数打 DELETE', async () => {
    await deleteAdminChannel('c2')

    const { url, init } = lastCall()
    expect(url).toBe('http://api.test/api/admin/channels/c2')
    expect(init.method).toBe('DELETE')
  })

  it('restartAdminService 返回 data.message，而不是信封顶层的 "success"', async () => {
    fetchMock.mockResolvedValue(
      okResponse(envelope({ success: true, message: 'restart scheduled' })),
    )

    const notice = await restartAdminService('ridgericetalk')

    expect(lastCall().url).toBe('http://api.test/api/admin/service/restart')
    expect(notice).toBe('restart scheduled')
    expect(notice).not.toBe('success')
  })

  it('restartAdminService 在 data.message 缺失时回落到中文文案', async () => {
    fetchMock.mockResolvedValue(okResponse(envelope({ success: true })))

    const notice = await restartAdminService('ridgericetalk')

    expect(notice).not.toBe('success')
    expect(notice).toContain('重启指令已发送')
  })

  // P0-1 回归：端口配置 tab 曾经恒空白，因为这里读的是信封顶层的 .ports
  // （恒 undefined），而不是 .data.ports。改回去 → 下面的 toHaveLength 立刻失败。
  it('getAdminPorts 解包信封的 data.ports，而不是信封顶层（P0-1）', async () => {
    fetchMock.mockResolvedValue(okResponse(envelope({
      ports: [
        { key: 'api', label: 'API / Web 端口', protocol: 'tcp', internalPort: 8080, externalPort: 443 },
        { key: 'lkUdp', label: 'LiveKit UDP 端口', protocol: 'udp', internalPort: 7882, externalPort: 7882 },
      ],
    })))

    const ports = await getAdminPorts()

    expect(lastCall().url).toBe('http://api.test/api/admin/ports')
    expect(Array.isArray(ports)).toBe(true)
    expect(ports).toHaveLength(2)
    expect(ports[0].internalPort).toBe(8080)
    expect(ports[1].key).toBe('lkUdp')
  })

  // P0-2 回归：后端 VerifyNetwork 返回顶层 {success, result}（没有信封），
  // 而调用方曾经按 res.data.result 取值 → 校验结果永不显示。
  it('verifyAdminNetwork 以 body.network 提交，调用方读顶层 result（P0-2）', async () => {
    fetchMock.mockResolvedValue(okResponse({
      success: true,
      result: { serverUrl: 'https://voice.example.com', liveKitUrl: 'wss://voice.example.com/livekit', notes: [] },
    }))

    const res = await verifyAdminNetwork({ externalHost: 'voice.example.com' })

    expect(lastCall().url).toBe('http://api.test/api/admin/network/verify')
    expect(lastCall().init.method).toBe('POST')
    expect(JSON.parse(String(lastCall().init.body))).toEqual({
      network: { externalHost: 'voice.example.com' },
    })
    expect(res.success).toBe(true)
    expect(res.result.serverUrl).toBe('https://voice.example.com')
    expect((res as any).data).toBeUndefined()
  })

  // P0-5 回归：审计日志 CSV 导出接口早已实现，前端按钮却曾是 disabled 占位。
  // 这里锁住接口层真的会带着鉴权去取文件流并触发浏览器下载。
  it('exportAdminAuditLogs 取文件流并触发下载（P0-5）', async () => {
    const blob = new Blob(['time,level\n'], { type: 'text/csv' })
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      headers: { get: () => null },
      blob: async () => blob,
    } as unknown as Response)

    const createObjectURL = vi.fn(() => 'blob:mock-url')
    const revokeObjectURL = vi.fn()
    window.URL.createObjectURL = createObjectURL as any
    window.URL.revokeObjectURL = revokeObjectURL as any

    const anchors: HTMLAnchorElement[] = []
    const origCreateElement = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreateElement(tag)
      if (String(tag).toLowerCase() === 'a') {
        ;(el as HTMLAnchorElement).click = vi.fn()
        anchors.push(el as HTMLAnchorElement)
      }
      return el
    })

    await exportAdminAuditLogs()

    expect(lastCall().url).toBe('http://api.test/api/admin/audit-logs/export')
    expect(lastCall().init.method).toBe('GET')
    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(anchors).toHaveLength(1)
    expect(anchors[0].download).toBe('audit-logs.csv')
    expect(anchors[0].href).toContain('blob:mock-url')
    expect(anchors[0].click).toHaveBeenCalledTimes(1)
  })

  it('exportAdminAuditLogs 在导出接口报错时抛出，不静默成功', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 500 } as unknown as Response)

    await expect(exportAdminAuditLogs()).rejects.toThrow('下载失败: HTTP 500')
  })
})
