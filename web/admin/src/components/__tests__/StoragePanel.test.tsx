import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import StoragePanel from '../StoragePanel'
import * as apiCore from '../../../../shared/api-core'

vi.mock('../../../../shared/api-core', () => ({
  updateAdminConfig: vi.fn(),
  // E4-UI：全局分享子视图直接用共享请求原语调 /api/admin/cloudfs/shares。
  apiGet: vi.fn(),
  apiDelete: vi.fn(),
}))

describe('StoragePanel', () => {
  // formatBytes expects sizes in MB.
  const storage = [
    { id: 'logs', name: '日志', size: 100, files: 12, description: 'Application logs' },
    { id: 'tmp', name: '临时文件', size: 50, files: 5, description: 'Temporary files' },
  ]

  it('renders storage overview with correct totals', () => {
    render(<StoragePanel config={{ maxStorageGB: 10 } as any} storage={storage} />)
    expect(screen.getByText('存储概览')).toBeInTheDocument()
    expect(screen.getByText('日志')).toBeInTheDocument()
    expect(screen.getByText('临时文件')).toBeInTheDocument()
    // 100MB + 50MB = 150MB
    expect(screen.getAllByText('150.0 MB').length).toBeGreaterThanOrEqual(1)
  })

  it('updates max storage input and saves config', async () => {
    vi.mocked(apiCore.updateAdminConfig).mockResolvedValue({ code: 'OK' } as any)
    const { container } = render(<StoragePanel config={{ maxStorageGB: 10 } as any} storage={storage} />)

    const input = container.querySelector('#max-storage') as HTMLInputElement
    fireEvent.change(input, { target: { value: '20' } })
    expect(input.value).toBe('20')

    fireEvent.click(screen.getByRole('button', { name: /保存更改/i }))
    await waitFor(() => {
      expect(apiCore.updateAdminConfig).toHaveBeenCalledWith({ maxStorageGB: 20 })
    })
  })

  it('shows warning color when usage exceeds 50%', () => {
    // 6 GB in MB
    const largeStorage = [{ id: 'logs', name: '日志', size: 6 * 1024, files: 1, description: '' }]
    render(<StoragePanel config={{ maxStorageGB: 10 } as any} storage={largeStorage} />)
    expect(screen.getByText('60.0%')).toBeInTheDocument()
  })
})

// ── E4-UI：全局分享子视图 ──────────────────────────────────────────
import { formatShareTime } from '../../utils/format'

const shareItems = [
  {
    id: 'sh1', fileId: 'f1', fileName: '报告.pdf', userId: 'u1', spaceId: 'sp1',
    spaceName: '研发空间', downloadCount: 3, lastAccessAt: '2026-09-30T12:00:00Z',
    revoked: false, expired: false, requiresPassword: false,
    expiresAt: '2026-10-05T00:00:00Z', createdAt: '2026-09-28T08:00:00Z',
  },
  {
    id: 'sh2', fileId: 'f2', fileName: '归档.zip', userId: 'u2', spaceId: 'sp1',
    spaceName: '研发空间', downloadCount: 0, lastAccessAt: null,
    revoked: true, expired: false, requiresPassword: true,
    expiresAt: '2026-10-10T00:00:00Z', createdAt: '2026-09-29T09:30:00Z',
  },
]

function makeSharesPage(n: number, startId = 0) {
  return Array.from({ length: n }, (_, i) => ({
    id: `sh${startId + i}`, fileId: `f${i}`, fileName: `文件${i}.txt`, userId: 'u1',
    spaceId: 'sp1', spaceName: '空间', downloadCount: 0, lastAccessAt: null,
    revoked: false, expired: false, requiresPassword: false,
    expiresAt: '2026-10-10T00:00:00Z', createdAt: '2026-09-29T09:30:00Z',
  }))
}

describe('StoragePanel 全局分享子视图（E4-UI）', () => {
  beforeEach(() => {
    vi.mocked(apiCore.apiGet).mockResolvedValue({ code: 'OK', data: { items: shareItems } } as any)
    vi.mocked(apiCore.apiDelete).mockResolvedValue({ code: 'OK', data: { id: 'sh1', revoked: true } } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  async function openSharesTab() {
    render(<StoragePanel config={{ maxStorageGB: 10 } as any} storage={[]} />)
    fireEvent.click(screen.getByRole('button', { name: '全局分享' }))
    await waitFor(() => expect(apiCore.apiGet).toHaveBeenCalled())
  }

  it('切到全局分享时拉取 /api/admin/cloudfs/shares?limit=50&offset=0 并渲染表格', async () => {
    await openSharesTab()

    expect(apiCore.apiGet).toHaveBeenCalledWith('/api/admin/cloudfs/shares?limit=50&offset=0')
    expect(screen.getByText('全局分享管理')).toBeInTheDocument()
    expect(screen.getByText('报告.pdf')).toBeInTheDocument()
    expect(screen.getByText('u1')).toBeInTheDocument()
    // 两行 share 同属一个空间 → 用 getAllByText 断言出现两次
    expect(screen.getAllByText('研发空间')).toHaveLength(2)
    // 下载次数、状态、UTC 时间展示（lastAccessAt 为 null 显示 "-"）
    expect(screen.getByText('2026-09-30 12:00:00 UTC')).toBeInTheDocument()
    expect(screen.getByText('有效')).toBeInTheDocument()
    expect(screen.getByText('已撤销')).toBeInTheDocument()
    expect(screen.getByText('2026-09-28 08:00:00 UTC')).toBeInTheDocument()
  })

  it('分页：首页禁用上一页，下一页以 offset=50 重新拉取', async () => {
    vi.mocked(apiCore.apiGet).mockImplementation(async (path: string) => {
      const offset = Number(new URL(path, 'http://x').searchParams.get('offset') || 0)
      const items = offset === 0 ? makeSharesPage(50) : makeSharesPage(1, 50)
      return { code: 'OK', data: { items } } as any
    })

    await openSharesTab()

    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled()
    expect(screen.getByText('第 1 页')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => {
      expect(apiCore.apiGet).toHaveBeenLastCalledWith('/api/admin/cloudfs/shares?limit=50&offset=50')
    })
    expect(screen.getByText('第 2 页')).toBeInTheDocument()
    // 末页（1 条 < 50）：下一页禁用、上一页可用
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled()
  })

  it('撤销：confirm 弹窗 → DELETE → 提示成功并刷新列表', async () => {
    await openSharesTab()

    const row = screen.getByText('报告.pdf').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('撤销分享'))

    const dialog = screen.getByRole('dialog', { name: '确认撤销分享' })
    expect(within(dialog).getByText(/报告\.pdf/)).toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: '撤销' }))

    await waitFor(() => {
      expect(apiCore.apiDelete).toHaveBeenCalledWith('/api/admin/cloudfs/shares/sh1')
    })
    expect(window.alert).toHaveBeenCalledWith('分享已撤销')
    // 撤销后刷新列表（第二次 GET）
    await waitFor(() => expect(apiCore.apiGet).toHaveBeenCalledTimes(2))
    // 弹窗关闭
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '确认撤销分享' })).not.toBeInTheDocument())
  })

  it('已撤销行不可再撤销；404（记录不存在）按幂等成功处理并刷新', async () => {
    vi.mocked(apiCore.apiDelete).mockRejectedValueOnce(
      Object.assign(new Error('share not found'), { code: 'SHARE_NOT_FOUND' }),
    )
    await openSharesTab()

    // sh2 已撤销 → 按钮禁用
    const revokedRow = screen.getByText('归档.zip').closest('tr') as HTMLElement
    expect(within(revokedRow).getByTitle('撤销分享')).toBeDisabled()

    // sh1 撤销返回 404 → 不报错、按成功处理
    const row = screen.getByText('报告.pdf').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('撤销分享'))
    fireEvent.click(within(screen.getByRole('dialog', { name: '确认撤销分享' })).getByRole('button', { name: '撤销' }))

    await waitFor(() => {
      expect(apiCore.apiDelete).toHaveBeenCalledWith('/api/admin/cloudfs/shares/sh1')
    })
    expect(window.alert).toHaveBeenCalledWith('分享已撤销')
    expect(window.alert).not.toHaveBeenCalledWith(expect.stringContaining('撤销失败'))
    await waitFor(() => expect(apiCore.apiGet).toHaveBeenCalledTimes(2))
  })

  it('空列表显示空态文案，请求失败显示错误与重试', async () => {
    vi.mocked(apiCore.apiGet).mockResolvedValueOnce({ code: 'OK', data: { items: [] } } as any)
    await openSharesTab()
    expect(screen.getByText('暂无全局分享')).toBeInTheDocument()

    // 失败 → 错误提示 + 重试按钮
    vi.mocked(apiCore.apiGet).mockRejectedValueOnce(new Error('网络错误'))
    fireEvent.click(screen.getByRole('button', { name: '概览' }))
    fireEvent.click(screen.getByRole('button', { name: '全局分享' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('网络错误')
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })

  it('formatShareTime：RFC3339 转 UTC 可读格式，空值显示 -', () => {
    expect(formatShareTime('2026-10-01T08:09:10Z')).toBe('2026-10-01 08:09:10 UTC')
    expect(formatShareTime(null)).toBe('-')
    expect(formatShareTime(undefined)).toBe('-')
  })
})
