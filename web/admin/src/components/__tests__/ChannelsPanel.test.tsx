import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import ChannelsPanel from '../ChannelsPanel'
import * as apiCore from '../../../../shared/api-core'

// P1-1: 管理后台频道管理必须走 admin 专用接口（/api/admin/channels...），
// 而不是成员级 /api/channels。这里把成员级函数也放进 mock，用来断言
// ChannelsPanel 不再调用它们——若有人改回成员级接口，getAdminChannels
// 断言会失败、getChannels 断言会命中。
vi.mock('../../../../shared/api-core', () => ({
  getAdminChannels: vi.fn(),
  createAdminChannel: vi.fn(),
  updateAdminChannel: vi.fn(),
  deleteAdminChannel: vi.fn(),
  getChannelPermissions: vi.fn(),
  updateChannelPermissions: vi.fn(),
  transferChannelOwnership: vi.fn(),
  // 成员级接口（不应再被 ChannelsPanel 使用）
  getChannels: vi.fn(),
  createChannel: vi.fn(),
  updateChannel: vi.fn(),
  deleteChannel: vi.fn(),
}))

const channels = [
  { id: 'c1', spaceId: 's1', name: '综合', type: 'TEXT', position: 0 },
  { id: 'c2', spaceId: 's1', name: '语音房', type: 'VOICE', position: 1, voiceQuality: 'standard' },
]

function renderPanel() {
  return render(<ChannelsPanel users={[]} />)
}

describe('ChannelsPanel（admin 接口）', () => {
  beforeEach(() => {
    vi.mocked(apiCore.getAdminChannels).mockResolvedValue({ code: 'OK', data: channels } as any)
    vi.mocked(apiCore.createAdminChannel).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.mocked(apiCore.updateAdminChannel).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.mocked(apiCore.deleteAdminChannel).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  it('通过 getAdminChannels 加载列表，且不使用成员级 getChannels', async () => {
    renderPanel()

    await waitFor(() => {
      expect(apiCore.getAdminChannels).toHaveBeenCalled()
    })
    expect(await screen.findByText('综合')).toBeInTheDocument()
    expect(screen.getByText('语音房')).toBeInTheDocument()
    expect(vi.mocked(apiCore.getChannels)).not.toHaveBeenCalled()
  })

  it('创建语音频道时按 admin 接口的形状提交 name/type/audioQuality', async () => {
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    fireEvent.click(screen.getByRole('button', { name: /新建频道/ }))
    const dialog = screen.getByRole('dialog', { name: '新建频道' })
    fireEvent.change(within(dialog).getByLabelText('频道名称'), { target: { value: '新语音' } })
    fireEvent.change(within(dialog).getByLabelText('类型'), { target: { value: 'VOICE' } })
    fireEvent.change(within(dialog).getByLabelText('音频质量'), { target: { value: 'high' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '创建' }))

    await waitFor(() => {
      expect(apiCore.createAdminChannel).toHaveBeenCalledWith({
        name: '新语音',
        type: 'voice',
        audioQuality: 'high',
      })
    })
    // 创建后重新拉取列表（服务端生成 id/position，不能靠本地伪造）
    expect(apiCore.getAdminChannels).toHaveBeenCalledTimes(2)
    expect(vi.mocked(apiCore.createChannel)).not.toHaveBeenCalled()
  })

  it('创建失败时不把伪造的频道塞进列表', async () => {
    vi.mocked(apiCore.createAdminChannel).mockRejectedValue(new Error('CHANNEL_ALREADY_EXISTS'))
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    fireEvent.click(screen.getByRole('button', { name: /新建频道/ }))
    fireEvent.change(screen.getByLabelText('频道名称'), { target: { value: '重复频道' } })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalled()
    })
    expect(screen.queryByText('重复频道')).not.toBeInTheDocument()
    // 失败路径不再重拉列表（列表本来就还是服务端那份）
    expect(apiCore.getAdminChannels).toHaveBeenCalledTimes(1)
  })

  it('修改音质走 updateAdminChannel', async () => {
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    fireEvent.change(screen.getByLabelText('音频质量'), { target: { value: 'ultra' } })

    await waitFor(() => {
      expect(apiCore.updateAdminChannel).toHaveBeenCalledWith('c2', { audioQuality: 'ultra' })
    })
    expect(vi.mocked(apiCore.updateChannel)).not.toHaveBeenCalled()
  })

  it('重命名频道走 updateAdminChannel 并重新拉取真实列表', async () => {
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('综合').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('编辑'))
    fireEvent.change(screen.getByLabelText('频道名称'), { target: { value: '综合闲聊' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => {
      expect(apiCore.updateAdminChannel).toHaveBeenCalledWith('c1', { name: '综合闲聊' })
    })
    expect(apiCore.getAdminChannels).toHaveBeenCalledTimes(2)
  })

  it('删除频道走 deleteAdminChannel，失败时列表保持服务端状态', async () => {
    vi.mocked(apiCore.deleteAdminChannel).mockRejectedValue(new Error('CHANNEL_NOT_FOUND'))
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('综合').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('删除'))
    const dialog = screen.getByRole('dialog', { name: '确认删除频道' })
    fireEvent.click(within(dialog).getByRole('button', { name: '删除' }))

    await waitFor(() => {
      expect(apiCore.deleteAdminChannel).toHaveBeenCalledWith('c1')
    })
    expect(vi.mocked(apiCore.deleteChannel)).not.toHaveBeenCalled()
    // 删除失败：列表重新拉取后该频道仍在（不谎报删除成功）
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalledTimes(2))
    expect(screen.getByText('综合')).toBeInTheDocument()
  })

  // P0-3 回归锁：音质选择器曾经提交 32k/64k/128k/256k，而后端只接受
  // fluent/standard/high/ultra —— 保存静默无效。取值域回归时下面的断言立刻失败。
  it('音质选择器的取值域与后端一致（fluent/standard/high/ultra）（P0-3）', async () => {
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const select = screen.getByLabelText('音频质量') as HTMLSelectElement
    expect(Array.from(select.options).map(o => o.value)).toEqual(['fluent', 'standard', 'high', 'ultra'])
    expect(select.value).toBe('standard')
  })

  it('四个音质档位都会原样提交给 updateAdminChannel', async () => {
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const select = screen.getByLabelText('音频质量')
    for (const quality of ['fluent', 'standard', 'high', 'ultra']) {
      fireEvent.change(select, { target: { value: quality } })
      await waitFor(() => {
        expect(apiCore.updateAdminChannel).toHaveBeenCalledWith('c2', { audioQuality: quality })
      })
    }
    expect(apiCore.updateAdminChannel).toHaveBeenCalledTimes(4)
  })

  it('频道权限读取与保存分别走 getChannelPermissions / updateChannelPermissions', async () => {
    const perms = {
      read_roles: ['MEMBER'],
      write_roles: ['ADMIN'],
      visible_roles: [],
      is_public: false,
    }
    vi.mocked(apiCore.getChannelPermissions).mockResolvedValue({ code: 'OK', data: perms } as any)
    vi.mocked(apiCore.updateChannelPermissions).mockResolvedValue({ code: 'OK', data: {} } as any)

    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('语音房').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('权限'))

    const dialog = await screen.findByRole('dialog', { name: /频道权限管理/ })
    await waitFor(() => {
      expect(apiCore.getChannelPermissions).toHaveBeenCalledWith('c2')
    })
    // 已有权限回显：空的 visible_roles 表达为「所有角色（不限制）」
    expect(within(dialog).getByText(/当前: ADMIN/)).toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() => {
      expect(apiCore.updateChannelPermissions).toHaveBeenCalledWith('c2', perms)
    })
  })

  it('转移所有权走 transferChannelOwnership 并刷新列表', async () => {
    vi.mocked(apiCore.transferChannelOwnership).mockResolvedValue({ code: 'OK', data: {} } as any)
    render(
      <ChannelsPanel
        users={[{ id: 'u1', username: 'alice', email: 'alice@example.com', role: 'MEMBER' } as any]}
      />,
    )
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('综合').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('转移所有权'))
    const dialog = screen.getByRole('dialog', { name: '转移频道所有权' })

    const confirm = within(dialog).getByRole('button', { name: '确认转移' })
    expect(confirm).toBeDisabled()

    fireEvent.change(within(dialog).getByLabelText('选择新 OWNER'), { target: { value: 'u1' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '确认转移' }))

    await waitFor(() => {
      expect(apiCore.transferChannelOwnership).toHaveBeenCalledWith('c1', 'u1')
    })
  })
})

// ── A1-S4：频道分组列（内联编辑 + 排序）────────────────────────────
describe('ChannelsPanel 频道分组（A1-S4）', () => {
  beforeEach(() => {
    vi.mocked(apiCore.getAdminChannels).mockResolvedValue({ code: 'OK', data: channels } as any)
    vi.mocked(apiCore.updateAdminChannel).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  it('分组列按 未分组在前→组名码点序→position 排序渲染', async () => {
    // 故意打乱传入顺序，验证前端排序：未分组最前，组内按 position。
    const mixed = [
      { id: 'g1', spaceId: 's1', name: '乙组频道', type: 'TEXT', position: 2, sortGroup: 'work' },
      { id: 'g2', spaceId: 's1', name: '甲未分组', type: 'TEXT', position: 5 },
      { id: 'g3', spaceId: 's1', name: '丙阿尔法', type: 'TEXT', position: 1, sortGroup: 'alpha' },
      { id: 'g4', spaceId: 's1', name: '丁同组', type: 'TEXT', position: 0, sortGroup: 'alpha' },
    ]
    vi.mocked(apiCore.getAdminChannels).mockResolvedValue({ code: 'OK', data: mixed } as any)
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const tbody = screen.getByText('甲未分组').closest('tbody') as HTMLElement
    const names = Array.from(tbody.querySelectorAll('tr td:first-child')).map(td => td.textContent)
    // 未分组(甲) → alpha 组按 position（丁0, 丙1）→ work 组（乙）
    expect(names).toEqual(['甲未分组', '丁同组', '丙阿尔法', '乙组频道'])
  })

  it('内联编辑分组保存时按 PATCH {"sortGroup": value} 提交并刷新列表', async () => {
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('综合').closest('tr') as HTMLElement
    // 未编辑前显示「未分组」占位
    expect(row.textContent).toContain('未分组')
    fireEvent.click(within(row).getByTitle('编辑分组'))

    const input = screen.getByLabelText('分组名') as HTMLInputElement
    expect(input.value).toBe('')
    fireEvent.change(input, { target: { value: '重要' } })
    fireEvent.click(screen.getByTitle('保存分组'))

    await waitFor(() => {
      expect(apiCore.updateAdminChannel).toHaveBeenCalledWith('c1', { sortGroup: '重要' })
    })
    // 保存后以服务端列表为准（重拉一次）
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalledTimes(2))
    // 编辑态关闭
    await waitFor(() => expect(screen.queryByLabelText('分组名')).not.toBeInTheDocument())
  })

  it('清空分组名保存 = 移出分组（payload 为空串）', async () => {
    const grouped = [
      { id: 'c9', spaceId: 's1', name: '已分组频道', type: 'TEXT', position: 0, sortGroup: 'alpha' },
    ]
    vi.mocked(apiCore.getAdminChannels).mockResolvedValue({ code: 'OK', data: grouped } as any)
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('已分组频道').closest('tr') as HTMLElement
    expect(row.textContent).toContain('alpha')
    fireEvent.click(within(row).getByTitle('编辑分组'))
    fireEvent.change(screen.getByLabelText('分组名'), { target: { value: '' } })
    fireEvent.click(screen.getByTitle('保存分组'))

    await waitFor(() => {
      expect(apiCore.updateAdminChannel).toHaveBeenCalledWith('c9', { sortGroup: '' })
    })
  })

  it('服务端 400（分组名超长）时行内提示错误并保持编辑态', async () => {
    vi.mocked(apiCore.updateAdminChannel).mockRejectedValueOnce(new Error('分组名过长，最多 32 个字符'))
    renderPanel()
    await waitFor(() => expect(apiCore.getAdminChannels).toHaveBeenCalled())

    const row = screen.getByText('综合').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByTitle('编辑分组'))
    fireEvent.change(screen.getByLabelText('分组名'), { target: { value: 'x'.repeat(33) } })
    fireEvent.click(screen.getByTitle('保存分组'))

    // 行内错误提示（role=alert），且编辑态不关闭，用户可直接改后重试
    expect(await screen.findByRole('alert')).toHaveTextContent('分组名过长')
    expect(screen.getByLabelText('分组名')).toBeInTheDocument()
    // 失败路径不重拉列表
    expect(apiCore.getAdminChannels).toHaveBeenCalledTimes(1)
  })
})
