import { useState, useEffect, type CSSProperties } from 'react'
import type { ServerConfigSnapshot } from '../../../shared/types'
import { updateAdminConfig, apiGet, apiDelete } from '../../../shared/api-core'
import type { StorageCategory, AdminCloudShareItem } from '../types/admin'
import { formatBytes, formatShareTime } from '../utils/format'

interface StoragePanelProps {
  config: ServerConfigSnapshot | null
  storage: StorageCategory[]
}

// E4-UI：存储面板子视图。默认停留在「存储概览」（既有行为不变），
// 「全局分享」子视图按需加载（激活时才请求 /api/admin/cloudfs/shares）。
type StorageSubTab = 'overview' | 'shares'

// 全局分享列表分页大小，与服务端 adminSharesDefaultLimit 一致。
const SHARES_PAGE_SIZE = 50

// 服务端对"分享记录彻底不存在"返回 404（SHARE_NOT_FOUND）；已撤销则是幂等 200。
// 撤销操作对这两种结果都按成功处理（任务口径：404/已撤销幂等按成功并刷新）。
function isShareNotFound(err: any): boolean {
  return err?.code === 'SHARE_NOT_FOUND' || /404|not found|不存在/i.test(String(err?.message || ''))
}

// 全局分享子视图：跨空间分享表格 + 分页 + 撤销。
function GlobalSharesSection() {
  const [shares, setShares] = useState<AdminCloudShareItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loaded, setLoaded] = useState(false)
  const [offset, setOffset] = useState(0)
  const [revokeTarget, setRevokeTarget] = useState<AdminCloudShareItem | null>(null)
  const [revoking, setRevoking] = useState(false)

  const loadShares = async (nextOffset: number) => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiGet<{ code: string; data: { items: AdminCloudShareItem[] } }>(
        `/api/admin/cloudfs/shares?limit=${SHARES_PAGE_SIZE}&offset=${nextOffset}`,
      )
      setShares(res?.data?.items || [])
      setOffset(nextOffset)
      setLoaded(true)
    } catch (err: any) {
      setError(err?.message || '加载全局分享失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadShares(0)
  }, [])

  // 撤销确认后执行 DELETE；无论成败都关闭弹窗并刷新列表，
  // 让界面始终回到服务端真实状态（与 ChannelsPanel 删除频道的模式一致）。
  const doRevoke = async () => {
    if (!revokeTarget) return
    setRevoking(true)
    try {
      await apiDelete(`/api/admin/cloudfs/shares/${encodeURIComponent(revokeTarget.id)}`)
      alert('分享已撤销')
    } catch (err: any) {
      if (isShareNotFound(err)) {
        // 404（记录不存在）按幂等成功处理，不向用户报错。
        alert('分享已撤销')
      } else {
        alert('撤销失败: ' + (err?.message || '未知错误'))
      }
    } finally {
      setRevoking(false)
      setRevokeTarget(null)
      await loadShares(offset)
    }
  }

  const hasPrev = offset > 0
  // 服务端按 created_at DESC 返回；本页条数不足一页说明已到末页。
  const hasNext = shares.length >= SHARES_PAGE_SIZE
  const page = Math.floor(offset / SHARES_PAGE_SIZE) + 1

  const shareStatus = (s: AdminCloudShareItem) => {
    if (s.revoked) return { text: '已撤销', color: 'var(--danger)' }
    if (s.expired) return { text: '已过期', color: 'var(--warning, #f59e0b)' }
    return { text: '有效', color: 'var(--accent)' }
  }

  return (
    <div className="admin-card" style={{ overflow: 'auto' }}>
      <h3>全局分享管理</h3>
      <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 4 }}>
        全部空间内的云文件分享链接。撤销后链接立即失效；列表按创建时间倒序分页展示。
      </div>
      {loading ? (
        <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-secondary)', fontSize: 14 }}>加载中...</div>
      ) : error ? (
        <div style={{ padding: 24, textAlign: 'center', fontSize: 14 }}>
          <div role="alert" style={{ color: 'var(--danger)' }}>{error}</div>
          <button className="track-btn" style={{ marginTop: 12 }} onClick={() => loadShares(offset)}>重试</button>
        </div>
      ) : shares.length === 0 ? (
        <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-secondary)', fontSize: 14 }}>
          {loaded ? '暂无全局分享' : '加载中...'}
        </div>
      ) : (
        <>
          <table className="data-table" style={{ marginTop: 12 }}>
            <thead>
              <tr>
                <th>文件名</th>
                <th>属主</th>
                <th>空间名</th>
                <th>下载次数</th>
                <th>最后访问</th>
                <th>状态</th>
                <th>过期时间</th>
                <th>创建时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {shares.map(s => {
                const status = shareStatus(s)
                return (
                  <tr key={s.id}>
                    <td><strong>{s.fileName || '-'}</strong></td>
                    <td>{s.userId}</td>
                    <td>{s.spaceName || '-'}</td>
                    <td>{s.downloadCount}</td>
                    <td>{formatShareTime(s.lastAccessAt)}</td>
                    <td style={{ color: status.color }}>{status.text}</td>
                    <td>{formatShareTime(s.expiresAt)}</td>
                    <td>{formatShareTime(s.createdAt)}</td>
                    <td>
                      <button
                        className="icon-btn"
                        title="撤销分享"
                        aria-label={`撤销分享 ${s.fileName || s.id}`}
                        disabled={s.revoked || revoking}
                        style={s.revoked ? { opacity: 0.4, cursor: 'not-allowed' } : undefined}
                        onClick={() => setRevokeTarget(s)}
                      >撤销</button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 16 }}>
            <button className="track-btn" disabled={!hasPrev} onClick={() => loadShares(Math.max(0, offset - SHARES_PAGE_SIZE))}>上一页</button>
            <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>第 {page} 页</span>
            <button className="track-btn" disabled={!hasNext} onClick={() => loadShares(offset + SHARES_PAGE_SIZE)}>下一页</button>
          </div>
        </>
      )}

      {revokeTarget && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => !revoking && setRevokeTarget(null)}>
          <div className="admin-card" role="dialog" aria-modal="true" aria-label="确认撤销分享" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3>确认撤销分享</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14, marginTop: 8 }}>
              确定要撤销 <strong>{revokeTarget.fileName || revokeTarget.id}</strong> 的分享吗？撤销后链接立即失效，此操作不可恢复。
            </p>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
              <button className="track-btn" disabled={revoking} onClick={() => setRevokeTarget(null)}>取消</button>
              <button className="track-btn" style={{ background: 'var(--danger)', color: '#fff' }} disabled={revoking} onClick={doRevoke}>{revoking ? '撤销中...' : '撤销'}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

export default function StoragePanel({ config, storage }: StoragePanelProps) {
  const [subTab, setSubTab] = useState<StorageSubTab>('overview')
  const [maxStorage, setMaxStorage] = useState(config?.maxStorageGB ?? 10)

  useEffect(() => {
    if (config?.maxStorageGB && config.maxStorageGB > 0) {
      setMaxStorage(config.maxStorageGB)
    }
  }, [config?.maxStorageGB])

  const totalStorage = storage.reduce((sum, c) => sum + c.size, 0)
  const storagePercent = Math.min(100, (totalStorage / (maxStorage * 1024)) * 100)
  const storageColor = storagePercent > 80 ? 'var(--danger)' : storagePercent > 50 ? 'var(--warning, #f59e0b)' : 'var(--accent)'

  const tabButtonStyle = (active: boolean): CSSProperties =>
    active ? { background: 'var(--accent)', color: '#fff' } : {}

  return (
    <>
      {/* E4-UI：子视图切换。按钮用「概览/全局分享」短文案，避免与下方 h2 标题文本重复。 */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
        <button
          className="track-btn"
          aria-pressed={subTab === 'overview'}
          style={tabButtonStyle(subTab === 'overview')}
          onClick={() => setSubTab('overview')}
        >概览</button>
        <button
          className="track-btn"
          aria-pressed={subTab === 'shares'}
          style={tabButtonStyle(subTab === 'shares')}
          onClick={() => setSubTab('shares')}
        >全局分享</button>
      </div>
      {subTab === 'overview' && (
        <>
          <h2 style={{ marginBottom: 20, fontSize: 20 }}>存储概览</h2>
          <div className="admin-card">
            <h3>软件最大磁盘占用</h3>
            <div className="stat-grid" style={{ gridTemplateColumns: '1fr 1fr', marginTop: 12 }}>
              <div className="stat-box">
                <label className="stat-label" htmlFor="max-storage">设定最大占用</label>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 8 }}>
                  <input id="max-storage" className="auth-input" style={{ margin: 0, width: 120 }} type="number" value={maxStorage} onChange={e => setMaxStorage(Number(e.target.value))} />
                  <span style={{ fontSize: 14, color: 'var(--text-secondary)' }}>GB</span>
                </div>
              </div>
              <div className="stat-box">
                <div className="stat-label">当前占用</div>
                <div style={{ marginTop: 8, fontSize: 14 }}>
                  <strong>{formatBytes(totalStorage)}</strong>
                  <span style={{ color: 'var(--text-secondary)', marginLeft: 8 }}>{storagePercent.toFixed(1)}%</span>
                </div>
                <div style={{ marginTop: 8, height: 8, background: 'var(--bg-primary)', borderRadius: 4, overflow: 'hidden' }}>
                  <div style={{ width: `${storagePercent}%`, height: '100%', background: storageColor, transition: 'width 0.3s' }} />
                </div>
              </div>
            </div>
            <div style={{ marginTop: 12, fontSize: 13, color: 'var(--text-secondary)' }}>超过设定值后，系统将自动清理旧日志和临时文件以释放空间。</div>
            <div style={{ marginTop: 12, textAlign: 'right' }}>
              <button className="track-btn" onClick={async () => {
                try { await updateAdminConfig({ maxStorageGB: maxStorage }); alert('配置已保存') } catch (e: any) { alert('保存失败: ' + (e?.message || '未知错误')) }
              }}>保存更改</button>
            </div>
          </div>
          <div className="admin-card" style={{ overflow: 'auto' }}>
            <h3>各模块占用详情</h3>
            <table className="data-table" style={{ marginTop: 12 }}>
              <thead>
                <tr>
                  <th>分类</th>
                  <th>占用</th>
                  <th>文件数</th>
                  <th>说明</th>
                </tr>
              </thead>
              <tbody>
                {storage.map(s => (
                  <tr key={s.id}>
                    <td><strong>{s.name}</strong></td>
                    <td>{formatBytes(s.size)}</td>
                    <td>{s.files}</td>
                    <td>{s.description}</td>
                  </tr>
                ))}
                <tr style={{ fontWeight: 'bold', borderTop: '2px solid var(--border)' }}>
                  <td>合计</td>
                  <td>{formatBytes(totalStorage)}</td>
                  <td>{storage.reduce((sum, s) => sum + s.files, 0)}</td>
                  <td>-</td>
                </tr>
              </tbody>
            </table>
            <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
              <button className="track-btn" disabled title="暂未实现" style={{ opacity: 0.5, cursor: 'not-allowed' }}>清理日志文件</button>
              <button className="track-btn" disabled title="暂未实现" style={{ opacity: 0.5, cursor: 'not-allowed' }}>清理临时文件</button>
            </div>
          </div>
        </>
      )}
      {subTab === 'shares' && <GlobalSharesSection />}
    </>
  )
}
