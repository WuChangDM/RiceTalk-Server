import { useState, useRef } from 'react'
import { Search, ChevronLeft, ChevronRight, AlertTriangle, Lock, Trash2 } from 'lucide-react'
import type { AdminSession, AdminUser } from '../../../shared/types'
import {
  updateUserRole, updateUserStatus, deleteAdminUser, resetUserPassword,
} from '../../../shared/api-core'
import { useModalFocus } from '../../../shared/useModalFocus'

interface UsersPanelProps {
  session: AdminSession
  users: (AdminUser & { isActive?: boolean; lastLoginAt?: string })[]
  userTotal: number
  userSummary: Record<string, any>
  userPage: number
  userKeyword: string
  usersPerPage: number
  onPageChange: (page: number) => void
  onKeywordChange: (keyword: string) => void
  onUsersChange: (users: (AdminUser & { isActive?: boolean; lastLoginAt?: string })[]) => void
  onReload: () => void
}

export default function UsersPanel({
  session,
  users,
  userTotal,
  userSummary,
  userPage,
  userKeyword,
  usersPerPage,
  onPageChange,
  onKeywordChange,
  onUsersChange,
  onReload,
}: UsersPanelProps) {
  const [userSearch, setUserSearch] = useState(userKeyword)
  const [confirmRemoveUser, setConfirmRemoveUser] = useState<string | null>(null)
  const [resetPasswordUser, setResetPasswordUser] = useState<string | null>(null)
  const [resetPasswordNew, setResetPasswordNew] = useState('')
  const [resetPasswordConfirm, setResetPasswordConfirm] = useState('')
  const [resetPasswordError, setResetPasswordError] = useState('')
  const [resetPasswordLoading, setResetPasswordLoading] = useState(false)

  const confirmRemoveUserModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(!!confirmRemoveUser, () => setConfirmRemoveUser(null), confirmRemoveUserModalRef)
  const resetPasswordModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(!!resetPasswordUser, () => setResetPasswordUser(null), resetPasswordModalRef)

  const totalPages = Math.max(1, Math.ceil(userTotal / usersPerPage))

  const handleSearch = () => {
    onKeywordChange(userSearch)
    onPageChange(1)
  }

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
        <h2 style={{ fontSize: 20 }}>用户管理</h2>
        <div style={{ position: 'relative' }}>
          <Search size={14} aria-hidden="true" style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }} />
          <input
            className="auth-input"
            aria-label="搜索用户"
            style={{ margin: 0, paddingLeft: 32, width: 240 }}
            placeholder="搜索用户名或邮箱（回车搜索）..."
            value={userSearch}
            onChange={e => setUserSearch(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') handleSearch() }}
          />
        </div>
      </div>
      <div className="admin-card" style={{ marginBottom: 16 }}>
        <div className="stat-grid" style={{ gridTemplateColumns: 'repeat(3, 1fr)' }}>
          <div className="stat-box">
            <div className="stat-value">{userSummary.total ?? userTotal}</div>
            <div className="stat-label">总用户数</div>
          </div>
          <div className="stat-box">
            <div className="stat-value">{userSummary.online ?? 0}</div>
            <div className="stat-label">在线</div>
          </div>
          <div className="stat-box">
            <div className="stat-value">{userSummary.admins ?? 0}</div>
            <div className="stat-label">管理员</div>
          </div>
        </div>
      </div>
      <div className="admin-card" style={{ overflow: 'auto' }}>
        {/* P1-4: 原「IP」「延迟」两列是假列——model.User 根本没有这两个字段，
            服务端也未采集（补采集涉及隐私与合规），故移除而不是长期显示 "-"。 */}
        <table className="data-table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>角色</th>
              <th>状态</th>
              <th>在线</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {users.map(u => (
              <tr key={u.id}>
                <td><strong>{u.username}</strong></td>
                <td>
                  <select className="select" aria-label="用户角色" defaultValue={u.role} style={{ minWidth: 100, padding: '4px 8px' }} disabled={u.role === 'OWNER' && session.role !== 'OWNER'} onChange={async (e) => {
                    const newRole = e.target.value
                    if (newRole === u.role) return
                    if (u.id === session.id) {
                      alert('不能修改自己的角色')
                      e.target.value = u.role
                      return
                    }
                    try {
                      await updateUserRole(u.id, newRole)
                      onUsersChange(users.map(x => x.id === u.id ? { ...x, role: newRole as AdminUser['role'] } : x))
                    } catch (err: any) {
                      alert('修改角色失败: ' + (err.message || '未知错误'))
                      e.target.value = u.role
                    }
                  }}>
                    <option value="MEMBER">Member</option>
                    <option value="ADMIN">Admin</option>
                    <option value="OWNER" disabled={session.role !== 'OWNER'}>Owner</option>
                  </select>
                </td>
                <td>{u.isActive === false ? <span style={{ color: 'var(--danger)' }}>● 已禁用</span> : <span style={{ color: 'var(--accent)' }}>● 正常</span>}</td>
                <td>{u.online ? <span style={{ color: 'var(--accent)' }}>● 在线</span> : <span style={{ color: 'var(--text-secondary)' }}>○ 离线</span>}</td>
                <td>
                  {u.role !== 'OWNER' && (
                    <>
                      <button className="track-btn" style={{ marginRight: 4, padding: '4px 10px', fontSize: 12 }} title={u.isActive === false ? '启用账户' : '禁用账户'} onClick={async () => {
                        try {
                          await updateUserStatus(u.id, u.isActive === false)
                          await onReload()
                        } catch (err: any) {
                          alert((u.isActive === false ? '启用' : '禁用') + '失败: ' + (err.message || '未知错误'))
                        }
                      }}>{u.isActive === false ? '启用' : '禁用'}</button>
                      <button className="track-btn" style={{ marginRight: 4, padding: '4px 10px', fontSize: 12 }} title="重置密码" onClick={() => { setResetPasswordUser(u.id); setResetPasswordNew(''); setResetPasswordConfirm(''); setResetPasswordError('') }}><Lock size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 2 }} />重置密码</button>
                      <button className="icon-btn" title="移除成员" onClick={() => setConfirmRemoveUser(u.id)}><Trash2 size={14} aria-hidden="true" /></button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', gap: 8, marginTop: 12 }}>
          <button className="icon-btn" disabled={userPage <= 1} onClick={() => { const p = Math.max(1, userPage - 1); onPageChange(p) }}><ChevronLeft size={14} aria-hidden="true" /></button>
          {(() => {
            const pages: (number | string)[] = []
            if (totalPages <= 7) {
              for (let i = 1; i <= totalPages; i++) pages.push(i)
            } else {
              pages.push(1)
              const start = Math.max(2, userPage - 1)
              const end = Math.min(totalPages - 1, userPage + 1)
              if (start > 2) pages.push('...')
              for (let i = start; i <= end; i++) pages.push(i)
              if (end < totalPages - 1) pages.push('...')
              pages.push(totalPages)
            }
            return pages.map((p, idx) =>
              typeof p === 'string' ? (
                <span key={`gap-${idx}`} style={{ padding: '0 4px', color: 'var(--text-secondary)' }}>...</span>
              ) : (
                <button key={p} className={`icon-btn ${p === userPage ? 'active' : ''}`} style={{ minWidth: 28 }} onClick={() => onPageChange(p)}>{p}</button>
              )
            )
          })()}
          <button className="icon-btn" disabled={userPage >= totalPages} onClick={() => { const p = Math.min(totalPages, userPage + 1); onPageChange(p) }}><ChevronRight size={14} aria-hidden="true" /></button>
        </div>
      </div>

      {confirmRemoveUser && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setConfirmRemoveUser(null)}>
          <div ref={confirmRemoveUserModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="确认移除用户" style={{ width: 360, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8 }}><AlertTriangle size={18} color="var(--danger)" aria-hidden="true" /> 确认移除</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14, marginTop: 8 }}>确定要移除用户 <strong>{users.find(u => u.id === confirmRemoveUser)?.username}</strong> 吗？此操作不可撤销。</p>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={() => setConfirmRemoveUser(null)}>取消</button>
              <button className="track-btn" style={{ background: 'var(--danger)', color: '#fff' }} onClick={async () => {
                try {
                  await deleteAdminUser(confirmRemoveUser)
                  await onReload()
                } catch (err: any) {
                  alert('删除用户失败: ' + (err.message || '未知错误'))
                }
                setConfirmRemoveUser(null)
              }}>确认移除</button>
            </div>
          </div>
        </div>
      )}

      {resetPasswordUser && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setResetPasswordUser(null)}>
          <div ref={resetPasswordModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="重置密码" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Lock size={18} aria-hidden="true" /> 重置密码</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14, marginTop: 8 }}>为用户 <strong>{users.find(u => u.id === resetPasswordUser)?.username}</strong> 设置新密码，重置后该用户的所有登录会话将立即失效。</p>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <label className="stat-label" htmlFor="reset-new-password">新密码（至少 8 位）</label>
              <input id="reset-new-password" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="password" placeholder="输入新密码" value={resetPasswordNew} onChange={e => setResetPasswordNew(e.target.value)} />
            </div>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <label className="stat-label" htmlFor="reset-confirm-password">确认密码</label>
              <input id="reset-confirm-password" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="password" placeholder="再次输入新密码" value={resetPasswordConfirm} onChange={e => setResetPasswordConfirm(e.target.value)} />
            </div>
            {resetPasswordError && <div style={{ color: 'var(--danger)', fontSize: 13, marginTop: 8 }}>{resetPasswordError}</div>}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={() => setResetPasswordUser(null)}>取消</button>
              <button className="track-btn" disabled={resetPasswordLoading} onClick={async () => {
                if (resetPasswordNew.length < 8) { setResetPasswordError('密码至少需要 8 位'); return }
                if (resetPasswordNew !== resetPasswordConfirm) { setResetPasswordError('两次输入的密码不一致'); return }
                setResetPasswordLoading(true)
                setResetPasswordError('')
                try {
                  await resetUserPassword(resetPasswordUser, resetPasswordNew)
                  alert('密码已重置')
                  setResetPasswordUser(null)
                } catch (err: any) {
                  setResetPasswordError(err.message || '重置失败')
                } finally {
                  setResetPasswordLoading(false)
                }
              }}>{resetPasswordLoading ? '重置中...' : '确认重置'}</button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
