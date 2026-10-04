import { useState, useEffect, useRef } from 'react'
import { Settings, Plus, Trash2, Share2, Lock, Hash, Volume2, Pencil, Check, X } from 'lucide-react'
import type { Channel } from '../../../shared/types'
import type { AdminUser } from '../../../shared/types'
import {
  getAdminChannels, createAdminChannel, updateAdminChannel, deleteAdminChannel,
  getChannelPermissions, updateChannelPermissions, transferChannelOwnership,
} from '../../../shared/api-core'
import { useModalFocus } from '../../../shared/useModalFocus'

// 取值域必须与后端一致（channel/service.go 校验 fluent/standard/high/ultra），
// 否则 updateAdminChannel 会被后端拒绝或静默丢弃。label 保留中文码率描述。
const AUDIO_QUALITY_OPTIONS = [
  { label: '流畅 32k', value: 'fluent' },
  { label: '标准 64k', value: 'standard' },
  { label: '高清 128k', value: 'high' },
  { label: '极致 256k', value: 'ultra' },
]

// admin 频道接口（admin/handler.go 的 C-3 一组）使用 channelId 路径参数，
// 并自行生成 id/spaceId/position；创建体只需要 name/type/audioQuality。
type ChannelPayload = {
  name: string
  type: 'text' | 'voice'
  audioQuality?: string
}

interface ChannelsPanelProps {
  users: (AdminUser & { isActive?: boolean; lastLoginAt?: string })[]
}

export default function ChannelsPanel({ users }: ChannelsPanelProps) {
  const [channels, setChannels] = useState<Channel[]>([])
  const [audioQuality, setAudioQuality] = useState<Record<string, string>>({})

  const [showNewChannelModal, setShowNewChannelModal] = useState(false)
  const [newChannelName, setNewChannelName] = useState('')
  const [newChannelType, setNewChannelType] = useState<'TEXT' | 'VOICE'>('TEXT')
  const [newChannelQuality, setNewChannelQuality] = useState('standard')
  const [editChannelId, setEditChannelId] = useState<string | null>(null)
  const [editChannelName, setEditChannelName] = useState('')
  const [deleteChannelConfirm, setDeleteChannelConfirm] = useState<string | null>(null)
  const [transferChannelId, setTransferChannelId] = useState<string | null>(null)
  const [transferNewOwnerId, setTransferNewOwnerId] = useState('')
  const [transferLoading, setTransferLoading] = useState(false)

  // A1-S4：分组列内联编辑态。groupEditing 记录正在编辑的频道与输入值；
  // groupError 保存服务端校验失败的提示（如分组名超过 32 rune 的 400），
  // 显示在输入框下方，编辑态保持打开以便用户改后重试。
  const [groupEditing, setGroupEditing] = useState<{ id: string; value: string } | null>(null)
  const [groupSaving, setGroupSaving] = useState(false)
  const [groupError, setGroupError] = useState<string | null>(null)

  const [channelPermissionsChannel, setChannelPermissionsChannel] = useState<Channel | null>(null)
  const [channelPermissionsData, setChannelPermissionsData] = useState<{
    read_roles: string[]
    write_roles: string[]
    visible_roles: string[]
    is_public: boolean
  } | null>(null)
  const [channelPermissionsLoading, setChannelPermissionsLoading] = useState(false)
  const [channelPermissionsSaving, setChannelPermissionsSaving] = useState(false)

  const loadChannels = async () => {
    try {
      const res = await getAdminChannels()
      if (res?.data) setChannels(res.data)
    } catch {
      // ignore
    }
  }

  useEffect(() => {
    loadChannels()
  }, [])

  const newChannelModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(showNewChannelModal, () => setShowNewChannelModal(false), newChannelModalRef)
  const editChannelModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(!!editChannelId, () => setEditChannelId(null), editChannelModalRef)
  const deleteChannelConfirmModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(!!deleteChannelConfirm, () => setDeleteChannelConfirm(null), deleteChannelConfirmModalRef)
  const transferChannelModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(!!transferChannelId, () => setTransferChannelId(null), transferChannelModalRef)
  const channelPermissionsModalRef = useRef<HTMLDivElement>(null)
  useModalFocus(!!channelPermissionsChannel, () => setChannelPermissionsChannel(null), channelPermissionsModalRef)

  const handleUpdateChannelName = async (id: string, name: string) => {
    try {
      await updateAdminChannel(id, { name })
    } catch (err: any) {
      alert('保存频道名称失败: ' + (err?.message || '未知错误'))
    }
    // 无论成功与否都以服务端为准，不做本地乐观改名（避免界面显示未落库的名称）。
    await loadChannels()
  }

  const handleDeleteChannel = async (id: string) => {
    try {
      await deleteAdminChannel(id)
    } catch (err: any) {
      alert('删除频道失败: ' + (err?.message || '未知错误'))
    }
    // 删除失败时刷新列表会保留该频道，界面不会谎报删除成功。
    await loadChannels()
    setEditChannelId(null)
  }

  const handleCreateChannel = async () => {
    if (!newChannelName.trim()) return
    const type = newChannelType.toLowerCase() as 'text' | 'voice'
    const payload: ChannelPayload = { name: newChannelName.trim(), type }
    // 语音频道创建时需把音质带给后端（CreateChannel body 的 audioQuality 字段）。
    if (type === 'voice') payload.audioQuality = newChannelQuality
    try {
      await createAdminChannel(payload)
    } catch (err: any) {
      // 创建失败不能伪造一个本地频道塞进列表。
      alert('创建频道失败: ' + (err?.message || '未知错误'))
      return
    }
    await loadChannels()
    setNewChannelName('')
    setNewChannelType('TEXT')
    setNewChannelQuality('standard')
    setShowNewChannelModal(false)
  }

  const openChannelPermissions = async (channel: Channel) => {
    setChannelPermissionsChannel(channel)
    setChannelPermissionsData(null)
    setChannelPermissionsLoading(true)
    try {
      const res = await getChannelPermissions(channel.id)
      if (res?.data) {
        setChannelPermissionsData({
          read_roles: res.data.read_roles || [],
          write_roles: res.data.write_roles || [],
          visible_roles: res.data.visible_roles || [],
          is_public: !!res.data.is_public,
        })
      }
    } catch (err: any) {
      alert('加载频道权限失败: ' + (err?.message || '未知错误'))
      setChannelPermissionsChannel(null)
    } finally {
      setChannelPermissionsLoading(false)
    }
  }

  const saveChannelPermissions = async () => {
    if (!channelPermissionsChannel || !channelPermissionsData) return
    setChannelPermissionsSaving(true)
    try {
      await updateChannelPermissions(channelPermissionsChannel.id, channelPermissionsData)
      try {
        const res = await getChannelPermissions(channelPermissionsChannel.id)
        if (res?.data) {
          setChannelPermissionsData({
            read_roles: res.data.read_roles || [],
            write_roles: res.data.write_roles || [],
            visible_roles: res.data.visible_roles || [],
            is_public: !!res.data.is_public,
          })
        }
      } catch {
        // Verification fetch failed — the save itself succeeded; not fatal.
      }
      alert('权限已保存')
    } catch (err: any) {
      alert('保存权限失败: ' + (err?.message || '未知错误'))
    } finally {
      setChannelPermissionsSaving(false)
    }
  }

  const toggleRoleInList = (list: string[], role: string): string[] => {
    return list.includes(role) ? list.filter(r => r !== role) : [...list, role]
  }

  // A1-S4：列表按 (sort_group, position) 排序——未分组（sortGroup 为空）排在最前，
  // 其后按组名的 Unicode 码点序（用 < 直接比较而非 localeCompare，与服务端
  // "sort_group ASC" 的字典序对齐），同组内按 position 升序。
  const compareBySortGroup = (a: Channel, b: Channel): number => {
    const ag = a.sortGroup || ''
    const bg = b.sortGroup || ''
    if (!ag && bg) return -1
    if (ag && !bg) return 1
    if (ag !== bg) return ag < bg ? -1 : 1
    return (a.position ?? 0) - (b.position ?? 0)
  }

  const startEditGroup = (c: Channel) => {
    setGroupEditing({ id: c.id, value: c.sortGroup || '' })
    setGroupError(null)
  }

  const cancelEditGroup = () => {
    setGroupEditing(null)
    setGroupError(null)
  }

  // 保存分组：PATCH {"sortGroup": value}；空串 = 移出分组。服务端 400（超长等）
  // 时不关闭编辑态，把错误文案显示在输入框下方。
  const saveSortGroup = async () => {
    if (!groupEditing) return
    setGroupSaving(true)
    setGroupError(null)
    try {
      await updateAdminChannel(groupEditing.id, { sortGroup: groupEditing.value })
      setGroupEditing(null)
      // 与改名一致：不做本地乐观更新，保存后以服务端列表为准。
      await loadChannels()
    } catch (err: any) {
      setGroupError(err?.message || '保存分组失败')
    } finally {
      setGroupSaving(false)
    }
  }

  // 分组单元格：非编辑态显示组名（未分组显示占位）+ 编辑按钮；
  // 编辑态显示输入框 + 保存/取消按钮 + 行内错误提示。
  const renderGroupCell = (c: Channel) => {
    const editing = groupEditing && groupEditing.id === c.id ? groupEditing : null
    if (editing) {
      return (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            <input
              aria-label="分组名"
              className="auth-input"
              style={{ width: 120, padding: '4px 8px', margin: 0 }}
              value={editing.value}
              disabled={groupSaving}
              autoFocus
              onChange={e => setGroupEditing({ ...editing, value: e.target.value })}
              onKeyDown={e => {
                if (e.key === 'Enter') saveSortGroup()
                if (e.key === 'Escape') cancelEditGroup()
              }}
            />
            <button className="icon-btn" title="保存分组" disabled={groupSaving} onClick={saveSortGroup}><Check size={14} aria-hidden="true" /></button>
            <button className="icon-btn" title="取消编辑分组" disabled={groupSaving} onClick={cancelEditGroup}><X size={14} aria-hidden="true" /></button>
          </div>
          {groupError && (
            <div role="alert" style={{ color: 'var(--danger)', fontSize: 12, marginTop: 4 }}>{groupError}</div>
          )}
        </div>
      )
    }
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <span>{c.sortGroup || '未分组'}</span>
        <button className="icon-btn" title="编辑分组" onClick={() => startEditGroup(c)}><Pencil size={14} aria-hidden="true" /></button>
      </div>
    )
  }

  const sortedTextChannels = channels.filter(c => c.type === 'TEXT').sort(compareBySortGroup)
  const sortedVoiceChannels = channels.filter(c => c.type === 'VOICE').sort(compareBySortGroup)

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
        <h2 style={{ fontSize: 20 }}>频道管理</h2>
        <button className="track-btn" onClick={() => setShowNewChannelModal(true)}><Plus size={14} aria-hidden="true" style={{ marginRight: 4 }} /> 新建频道</button>
      </div>
      <div className="admin-card">
        <h3>─── 文字频道 ───</h3>
        <table className="data-table" style={{ marginTop: 12 }}>
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>分组</th>
              <th>消息数</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {sortedTextChannels.map(c => (
              <tr key={c.id}>
                <td><strong>{c.name}</strong></td>
                <td>文字</td>
                <td>{renderGroupCell(c)}</td>
                <td>-</td>
                <td>
                  <button className="icon-btn" title="编辑" onClick={() => { setEditChannelId(c.id); setEditChannelName(c.name) }}><Settings size={14} aria-hidden="true" /></button>
                  <button className="icon-btn" title="转移所有权" onClick={() => { setTransferChannelId(c.id); setTransferNewOwnerId('') }}><Share2 size={14} aria-hidden="true" /></button>
                  <button className="icon-btn" title="权限" onClick={() => openChannelPermissions(c)}><Lock size={14} aria-hidden="true" /></button>
                  <button className="icon-btn" title="删除" onClick={() => setDeleteChannelConfirm(c.id)}><Trash2 size={14} aria-hidden="true" /></button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="admin-card">
        <h3>─── 语音频道 ───</h3>
        <table className="data-table" style={{ marginTop: 12 }}>
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>分组</th>
              <th>音质</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {sortedVoiceChannels.map(c => (
              <tr key={c.id}>
                <td><strong>{c.name}</strong></td>
                <td>语音</td>
                <td>{renderGroupCell(c)}</td>
                <td>
                  <select className="select" aria-label="音频质量" style={{ minWidth: 110, padding: '4px 8px' }} value={audioQuality[c.id] || (c as any).voiceQuality || 'standard'} onChange={async e => {
                    const newQuality = e.target.value
                    setAudioQuality({ ...audioQuality, [c.id]: newQuality })
                    try {
                      await updateAdminChannel(c.id, { audioQuality: newQuality })
                    } catch (err: any) {
                      alert('音频质量保存失败: ' + (err?.message || '未知错误'))
                    }
                  }}>
                    {AUDIO_QUALITY_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                  </select>
                </td>
                <td>
                  <button className="icon-btn" title="编辑" onClick={() => { setEditChannelId(c.id); setEditChannelName(c.name) }}><Settings size={14} aria-hidden="true" /></button>
                  <button className="icon-btn" title="转移所有权" onClick={() => { setTransferChannelId(c.id); setTransferNewOwnerId('') }}><Share2 size={14} aria-hidden="true" /></button>
                  <button className="icon-btn" title="权限" onClick={() => openChannelPermissions(c)}><Lock size={14} aria-hidden="true" /></button>
                  <button className="icon-btn" title="删除" onClick={() => setDeleteChannelConfirm(c.id)}><Trash2 size={14} aria-hidden="true" /></button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {showNewChannelModal && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setShowNewChannelModal(false)}>
          <div ref={newChannelModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="新建频道" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3>新建频道</h3>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <label className="stat-label" htmlFor="new-channel-name">频道名称</label>
              <input id="new-channel-name" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} value={newChannelName} onChange={e => setNewChannelName(e.target.value)} />
            </div>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <label className="stat-label" htmlFor="new-channel-type">类型</label>
              <select id="new-channel-type" className="select" style={{ marginTop: 8 }} value={newChannelType} onChange={e => setNewChannelType(e.target.value as 'TEXT' | 'VOICE')}>
                <option value="TEXT">文字频道</option>
                <option value="VOICE">语音频道</option>
              </select>
            </div>
            {newChannelType === 'VOICE' && (
              <div className="stat-box" style={{ marginTop: 12 }}>
                <label className="stat-label" htmlFor="new-channel-quality">音频质量</label>
                <select id="new-channel-quality" className="select" style={{ marginTop: 8 }} value={newChannelQuality} onChange={e => setNewChannelQuality(e.target.value)}>
                  {AUDIO_QUALITY_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                </select>
              </div>
            )}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={() => setShowNewChannelModal(false)}>取消</button>
              <button className="track-btn" onClick={handleCreateChannel}>创建</button>
            </div>
          </div>
        </div>
      )}

      {editChannelId && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setEditChannelId(null)}>
          <div ref={editChannelModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="编辑频道" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3>编辑频道</h3>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <label className="stat-label" htmlFor="edit-channel-name">频道名称</label>
              <input id="edit-channel-name" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} value={editChannelName} onChange={e => setEditChannelName(e.target.value)} />
            </div>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <div className="stat-label">类型</div>
              <div style={{ marginTop: 8, padding: '8px 12px', background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', color: 'var(--text-secondary)', fontSize: 14 }}>
                {channels.find(c => c.id === editChannelId)?.type === 'TEXT' ? '文字频道' : '语音频道'}
              </div>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 16 }}>
              <button className="track-btn" style={{ background: 'var(--danger)', color: '#fff' }} onClick={() => setDeleteChannelConfirm(editChannelId)}><Trash2 size={14} aria-hidden="true" style={{ marginRight: 4 }} /> 删除频道</button>
              <div style={{ display: 'flex', gap: 8 }}>
                <button className="track-btn" onClick={() => setEditChannelId(null)}>取消</button>
                <button className="track-btn" onClick={() => { handleUpdateChannelName(editChannelId, editChannelName); setEditChannelId(null) }}>保存</button>
              </div>
            </div>
          </div>
        </div>
      )}

      {deleteChannelConfirm && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setDeleteChannelConfirm(null)}>
          <div ref={deleteChannelConfirmModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="确认删除频道" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3>确认删除频道</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14, marginTop: 8 }}>确定要删除此频道吗？此操作不可撤销。</p>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
              <button className="track-btn" onClick={() => setDeleteChannelConfirm(null)}>取消</button>
              <button className="track-btn" style={{ background: 'var(--danger)', color: '#fff' }} onClick={() => { handleDeleteChannel(deleteChannelConfirm); setDeleteChannelConfirm(null) }}>删除</button>
            </div>
          </div>
        </div>
      )}

      {transferChannelId && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setTransferChannelId(null)}>
          <div ref={transferChannelModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="转移频道所有权" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Share2 size={18} aria-hidden="true" /> 转移所有权</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14, marginTop: 8 }}>将频道 <strong>{channels.find(c => c.id === transferChannelId)?.name}</strong> 的所有权转移给其他用户。转移后新 OWNER 将获得该频道的完整管理权限。</p>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <label className="stat-label" htmlFor="transfer-new-owner">选择新 OWNER</label>
              <select id="transfer-new-owner" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} value={transferNewOwnerId} onChange={e => setTransferNewOwnerId(e.target.value)}>
                <option value="">请选择用户...</option>
                {users.map(u => (
                  <option key={u.id} value={u.id}>{u.username} ({u.email})</option>
                ))}
              </select>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={() => setTransferChannelId(null)}>取消</button>
              <button className="track-btn" disabled={transferLoading || !transferNewOwnerId} onClick={async () => {
                if (!transferChannelId || !transferNewOwnerId) return
                setTransferLoading(true)
                try {
                  await transferChannelOwnership(transferChannelId, transferNewOwnerId)
                  await loadChannels()
                  alert('所有权已转移')
                  setTransferChannelId(null)
                  setTransferNewOwnerId('')
                } catch (err: any) {
                  alert('转移失败: ' + (err.message || '未知错误'))
                } finally {
                  setTransferLoading(false)
                }
              }}>{transferLoading ? '转移中...' : '确认转移'}</button>
            </div>
          </div>
        </div>
      )}

      {channelPermissionsChannel && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setChannelPermissionsChannel(null)}>
          <div ref={channelPermissionsModalRef} className="admin-card" role="dialog" aria-modal="true" aria-label="频道权限管理" style={{ width: 480, maxWidth: '90vw', maxHeight: '85vh', overflowY: 'auto' }} onClick={e => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Lock size={18} aria-hidden="true" /> 频道权限 - {channelPermissionsChannel.name}</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 13, marginTop: 8 }}>勾选角色以限制读写/可见性。空列表表示"所有角色"（不限制）。公开频道会覆盖可读角色列表。</p>
            {channelPermissionsLoading ? (
              <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-secondary)', fontSize: 14 }}>加载中...</div>
            ) : channelPermissionsData ? (
              <>
                <div className="stat-box" style={{ marginTop: 12 }}>
                  <label className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                    <input
                      type="checkbox"
                      checked={channelPermissionsData.is_public}
                      onChange={e => setChannelPermissionsData({ ...channelPermissionsData, is_public: e.target.checked })}
                    />
                    公开频道（所有角色可读）
                  </label>
                </div>
                {(['MEMBER', 'ADMIN', 'OWNER'] as const).map(field => {
                  const list = field === 'MEMBER' ? channelPermissionsData.read_roles
                    : field === 'ADMIN' ? channelPermissionsData.write_roles
                    : channelPermissionsData.visible_roles
                  const setList = (newList: string[]) => {
                    if (field === 'MEMBER') setChannelPermissionsData({ ...channelPermissionsData, read_roles: newList })
                    else if (field === 'ADMIN') setChannelPermissionsData({ ...channelPermissionsData, write_roles: newList })
                    else setChannelPermissionsData({ ...channelPermissionsData, visible_roles: newList })
                  }
                  const title = field === 'MEMBER' ? '可读角色（Read）'
                    : field === 'ADMIN' ? '可写角色（Write）'
                    : '可见角色（Visible）'
                  return (
                    <div className="stat-box" key={field} style={{ marginTop: 12 }}>
                      <div className="stat-label">{title}</div>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12, marginTop: 8 }}>
                        {['OWNER', 'ADMIN', 'MEMBER'].map(role => (
                          <label key={role} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)', cursor: 'pointer' }}>
                            <input
                              type="checkbox"
                              checked={list.includes(role)}
                              onChange={() => setList(toggleRoleInList(list, role))}
                            />
                            {role}
                          </label>
                        ))}
                      </div>
                      <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
                        当前: {list.length === 0 ? '所有角色（不限制）' : list.join(', ')}
                      </div>
                    </div>
                  )
                })}
              </>
            ) : (
              <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-secondary)', fontSize: 14 }}>暂无权限数据</div>
            )}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={() => setChannelPermissionsChannel(null)}>取消</button>
              <button
                className="track-btn"
                disabled={channelPermissionsLoading || channelPermissionsSaving || !channelPermissionsData}
                onClick={saveChannelPermissions}
              >{channelPermissionsSaving ? '保存中...' : '保存'}</button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
