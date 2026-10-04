import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import UsersPanel from '../UsersPanel'
import * as apiCore from '../../../../shared/api-core'

vi.mock('../../../../shared/api-core', () => ({
  updateUserRole: vi.fn(),
  updateUserStatus: vi.fn(),
  deleteAdminUser: vi.fn(),
  resetUserPassword: vi.fn(),
}))

type Row = {
  id: string
  username: string
  email: string
  role: 'OWNER' | 'ADMIN' | 'MEMBER'
  online: boolean
  isActive: boolean
}

const users: Row[] = [
  { id: 'u1', username: 'alice', email: 'alice@example.com', role: 'MEMBER', online: true, isActive: true },
  { id: 'u2', username: 'bob', email: 'bob@example.com', role: 'ADMIN', online: false, isActive: false },
  { id: 'u3', username: 'boss', email: 'boss@example.com', role: 'OWNER', online: true, isActive: true },
]

const session = { id: 'me', username: 'owner', role: 'OWNER' } as any

// UsersPanel 是受控组件：用户列表来自父级 state。测试里用一个带 state 的
// 外壳渲染，才能验证 onUsersChange 的写回效果（而不是只看调用参数）。
function renderPanel(overrides: Partial<Parameters<typeof UsersPanel>[0]> = {}) {
  const onUsersChange = vi.fn()
  const onReload = vi.fn()
  const onPageChange = vi.fn()
  const onKeywordChange = vi.fn()
  const props = {
    session,
    users: users as any,
    userTotal: users.length,
    userSummary: { total: 3, online: 2, admins: 2 },
    userPage: 1,
    userKeyword: '',
    usersPerPage: 5,
    onPageChange,
    onKeywordChange,
    onUsersChange,
    onReload,
    ...overrides,
  }
  const utils = render(<UsersPanel {...props} />)
  return { ...utils, onUsersChange, onReload, onPageChange, onKeywordChange }
}

function rowOf(username: string): HTMLElement {
  return screen.getByText(username).closest('tr') as HTMLElement
}

describe('UsersPanel', () => {
  beforeEach(() => {
    vi.mocked(apiCore.updateUserRole).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.mocked(apiCore.updateUserStatus).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.mocked(apiCore.deleteAdminUser).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.mocked(apiCore.resetUserPassword).mockResolvedValue({ code: 'OK', data: {} } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
  })

  // P1-4 回归锁：model.User 没有 IP / 延迟字段，服务端也不采集。这两列必须
  // 保持移除状态——一旦有人把它们加回表格（又变成长期显示 "-" 的假列），
  // 下面的表头断言立刻失败。
  it('用户表不再渲染 IP / 延迟两列（P1-4）', () => {
    renderPanel()

    const headers = screen.getAllByRole('columnheader').map(th => th.textContent?.trim())
    expect(headers).toEqual(['用户名', '角色', '状态', '在线', '操作'])
    expect(screen.queryByText('IP')).not.toBeInTheDocument()
    expect(screen.queryByText('延迟')).not.toBeInTheDocument()
  })

  it('角色变更调用 updateUserRole 并把新角色写回列表', async () => {
    const { onUsersChange } = renderPanel()

    fireEvent.change(within(rowOf('alice')).getByLabelText('用户角色'), { target: { value: 'ADMIN' } })

    await waitFor(() => {
      expect(apiCore.updateUserRole).toHaveBeenCalledWith('u1', 'ADMIN')
    })
    const written = onUsersChange.mock.calls[0][0] as Row[]
    expect(written.find(u => u.id === 'u1')?.role).toBe('ADMIN')
    // 其他用户不受影响
    expect(written.find(u => u.id === 'u2')?.role).toBe('ADMIN')
    expect(vi.mocked(apiCore.updateUserStatus)).not.toHaveBeenCalled()
  })

  it('角色变更失败时提示错误且不写回本地列表', async () => {
    vi.mocked(apiCore.updateUserRole).mockRejectedValue(new Error('FORBIDDEN'))
    const { onUsersChange } = renderPanel()

    fireEvent.change(within(rowOf('alice')).getByLabelText('用户角色'), { target: { value: 'ADMIN' } })

    await waitFor(() => {
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining('FORBIDDEN'))
    })
    expect(onUsersChange).not.toHaveBeenCalled()
  })

  it('禁用正常用户调用 updateUserStatus(id, false) 并重新拉取列表', async () => {
    const { onReload } = renderPanel()

    fireEvent.click(within(rowOf('alice')).getByRole('button', { name: '禁用' }))

    await waitFor(() => {
      expect(apiCore.updateUserStatus).toHaveBeenCalledWith('u1', false)
      expect(onReload).toHaveBeenCalled()
    })
  })

  it('启用已禁用用户调用 updateUserStatus(id, true)', async () => {
    renderPanel()
    expect(within(rowOf('bob')).getByText(/已禁用/)).toBeInTheDocument()

    fireEvent.click(within(rowOf('bob')).getByRole('button', { name: '启用' }))

    await waitFor(() => {
      expect(apiCore.updateUserStatus).toHaveBeenCalledWith('u2', true)
    })
  })

  it('OWNER 行不提供禁用 / 重置 / 移除操作', () => {
    renderPanel()
    const row = rowOf('boss')
    expect(within(row).queryByRole('button', { name: '禁用' })).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: /重置密码/ })).not.toBeInTheDocument()
    expect(within(row).queryByTitle('移除成员')).not.toBeInTheDocument()
  })

  it('重置口令调用 resetUserPassword，成功后关闭弹窗', async () => {
    renderPanel()

    fireEvent.click(within(rowOf('alice')).getByRole('button', { name: /重置密码/ }))
    const dialog = screen.getByRole('dialog', { name: '重置密码' })
    fireEvent.change(within(dialog).getByLabelText(/新密码/), { target: { value: 'newpass123' } })
    fireEvent.change(within(dialog).getByLabelText('确认密码'), { target: { value: 'newpass123' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '确认重置' }))

    await waitFor(() => {
      expect(apiCore.resetUserPassword).toHaveBeenCalledWith('u1', 'newpass123')
    })
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: '重置密码' })).not.toBeInTheDocument()
    })
  })

  it('口令不足 8 位或两次不一致时不发送请求', async () => {
    renderPanel()

    fireEvent.click(within(rowOf('alice')).getByRole('button', { name: /重置密码/ }))
    const dialog = screen.getByRole('dialog', { name: '重置密码' })

    fireEvent.change(within(dialog).getByLabelText(/新密码/), { target: { value: 'short' } })
    fireEvent.change(within(dialog).getByLabelText('确认密码'), { target: { value: 'short' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '确认重置' }))
    expect(await within(dialog).findByText('密码至少需要 8 位')).toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText(/新密码/), { target: { value: 'newpass123' } })
    fireEvent.change(within(dialog).getByLabelText('确认密码'), { target: { value: 'newpass124' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '确认重置' }))
    expect(await within(dialog).findByText('两次输入的密码不一致')).toBeInTheDocument()

    expect(apiCore.resetUserPassword).not.toHaveBeenCalled()
  })

  it('删除用户调用 deleteAdminUser(id) 并重新拉取列表', async () => {
    const { onReload } = renderPanel()

    fireEvent.click(within(rowOf('alice')).getByTitle('移除成员'))
    const dialog = screen.getByRole('dialog', { name: '确认移除用户' })
    fireEvent.click(within(dialog).getByRole('button', { name: '确认移除' }))

    await waitFor(() => {
      expect(apiCore.deleteAdminUser).toHaveBeenCalledWith('u1')
      expect(onReload).toHaveBeenCalled()
    })
    expect(apiCore.deleteAdminUser).toHaveBeenCalledTimes(1)
  })

  it('回车搜索把关键词与页码写回父级', () => {
    const { onKeywordChange, onPageChange } = renderPanel({ userPage: 3 })

    const input = screen.getByLabelText('搜索用户')
    fireEvent.change(input, { target: { value: 'alice' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(onKeywordChange).toHaveBeenCalledWith('alice')
    expect(onPageChange).toHaveBeenCalledWith(1)
  })
})
