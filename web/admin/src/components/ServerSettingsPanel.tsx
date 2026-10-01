import { useState, useEffect } from 'react'
import type { CSSProperties } from 'react'
import { Check, X, Lock, RefreshCw, Copy, AlertTriangle, Power } from 'lucide-react'
import type { ServerConfigSnapshot, PortInfo, AdminSession } from '../../../shared/types'
import { updateAdminConfig, detectAdminNetwork, verifyAdminNetwork, updateAdminNetwork, getAdminPorts, restartAdminService, getAdminConfig } from '../../../shared/api-core'
import type { NetworkConfig } from '../types/admin'
import { formatBytes } from '../utils/format'

interface ServerSettingsPanelProps {
  config: ServerConfigSnapshot | null
  setConfig: (config: ServerConfigSnapshot | null) => void
  networkConfig: NetworkConfig
  setNetworkConfig: (config: NetworkConfig) => void
  networkConfigSaved: NetworkConfig
  setNetworkConfigSaved: (config: NetworkConfig) => void
  dataLoading: boolean
  session: AdminSession | null
}

// Task 8.3: 协议徽章样式 —— TCP 绿色、UDP 蓝色
const protocolBadgeStyle = (protocol: string): CSSProperties => ({
  display: 'inline-block',
  padding: '2px 8px',
  borderRadius: 4,
  fontSize: 12,
  fontWeight: 600,
  color: '#fff',
  backgroundColor: protocol === 'tcp' ? '#28a745' : '#007bff',
  fontFamily: 'monospace',
})

// Task 8.5: 端口 key → NetworkConfig 字段映射
const PORT_KEY_TO_NETWORK_FIELD: Record<string, keyof NetworkConfig> = {
  api: 'externalHttpPort',
  admin: 'externalAdminPort',
  lkWs: 'externalLiveKitWsPort',
  lkTcp: 'externalLiveKitTcpPort',
  lkUdp: 'externalMediaUdpPort',
}

export default function ServerSettingsPanel({
  config,
  setConfig,
  networkConfig,
  setNetworkConfig,
  networkConfigSaved,
  setNetworkConfigSaved,
  dataLoading,
  session,
}: ServerSettingsPanelProps) {
  const [serverSettingsTab, setServerSettingsTab] = useState<'basic' | 'webvoice' | 'ports' | 'network' | 'srv'>('basic')
  const [networkSaving, setNetworkSaving] = useState(false)
  const [detectingNetwork, setDetectingNetwork] = useState(false)
  const [detectionResult, setDetectionResult] = useState<any>(null)
  const [networkRecommendations, setNetworkRecommendations] = useState<Array<{ method: string; priority: number; reason: string }>>([])
  const [networkVerifyResult, setNetworkVerifyResult] = useState<any>(null)

  // Task 8.2: 端口配置 tab 相关 state
  const [ports, setPorts] = useState<PortInfo[]>([])
  const [portsLoading, setPortsLoading] = useState(false)
  const [portsLoaded, setPortsLoaded] = useState(false)
  // Task 8.5: 编辑弹窗
  const [editingPort, setEditingPort] = useState<PortInfo | null>(null)
  const [editingExternalPort, setEditingExternalPort] = useState<string>('')
  const [savingPort, setSavingPort] = useState(false)
  // Task 9: 服务重启
  const [showRestartModal, setShowRestartModal] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [restartNotice, setRestartNotice] = useState<string>('')

  const networkConfigDirty = JSON.stringify(networkConfig) !== JSON.stringify(networkConfigSaved)

  // NAT 环境判定：任一端口 natRequired 为 true 即视为 NAT 环境
  const isNatEnvironment = ports.some(p => p.natRequired)
  // Task 9: 仅 Owner 可见重启按钮
  const isOwner = session?.role === 'OWNER'

  const handleSaveNetwork = async () => {
    setNetworkSaving(true)
    try {
      await updateAdminNetwork(networkConfig)
      setNetworkConfigSaved(networkConfig)
    } catch (err: any) {
      alert('保存失败: ' + (err.message || '未知错误'))
    } finally {
      setNetworkSaving(false)
    }
  }

  // Task 8.2: 切换到 ports tab 时加载端口列表
  useEffect(() => {
    if (serverSettingsTab !== 'ports' || portsLoaded) return
    let cancelled = false
    setPortsLoading(true)
    getAdminPorts()
      .then(list => {
        if (cancelled) return
        setPorts(list || [])
        setPortsLoaded(true)
      })
      .catch(err => {
        if (cancelled) return
        alert('加载端口列表失败: ' + (err?.message || '未知错误'))
      })
      .finally(() => {
        if (!cancelled) setPortsLoading(false)
      })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverSettingsTab, portsLoaded])

  // Task 8.5: 打开编辑弹窗
  const openEditPortModal = (port: PortInfo) => {
    setEditingPort(port)
    setEditingExternalPort(String(port.externalPort))
  }

  const closeEditPortModal = () => {
    if (savingPort) return
    setEditingPort(null)
    setEditingExternalPort('')
  }

  // Task 8.5: 保存编辑后的外部端口
  const handleSavePort = async () => {
    if (!editingPort) return
    // vpn key 不通过 NetworkConfig 更新（内外端口通常一致）
    if (editingPort.key === 'vpn') {
      closeEditPortModal()
      return
    }
    const field = PORT_KEY_TO_NETWORK_FIELD[editingPort.key]
    if (!field) {
      closeEditPortModal()
      return
    }
    const newPort = Number(editingExternalPort)
    if (!Number.isFinite(newPort) || newPort <= 0 || newPort > 65535) {
      alert('外部端口必须为 1-65535 之间的整数')
      return
    }
    // NAT 环境下强制要求外部端口 == 内部端口
    if (editingPort.natRequired && newPort !== editingPort.internalPort) {
      alert('NAT 环境下外部端口必须与内部端口一致（' + editingPort.internalPort + '）')
      return
    }
    setSavingPort(true)
    try {
      const next: NetworkConfig = { ...networkConfig, [field]: newPort }
      await updateAdminNetwork(next)
      setNetworkConfig(next)
      setNetworkConfigSaved(next)
      // 刷新端口列表
      try {
        const list = await getAdminPorts()
        setPorts(list || [])
      } catch { /* ignore */ }
      setEditingPort(null)
      setEditingExternalPort('')
    } catch (err: any) {
      alert('保存失败: ' + (err?.message || '未知错误'))
    } finally {
      setSavingPort(false)
    }
  }

  // Task 9.1-9.4: 确认重启服务
  const handleConfirmRestart = async () => {
    setRestarting(true)
    setRestartNotice('')
    try {
      const res = await restartAdminService('ridgericetalk')
      setShowRestartModal(false)
      // P1-6: 提示文案来自 data.message（信封顶层 message 恒为 "success"）。
      // restartAdminService 已经解析好并保证返回可展示文案。
      setRestartNotice(res || '重启指令已发送，服务将在 3-5 秒后重新连接')
      // 5 秒后自动重新拉取配置，验证服务已恢复
      setTimeout(async () => {
        try {
          const [cfgRes, portsList] = await Promise.all([
            getAdminConfig().catch(() => null),
            getAdminPorts().catch(() => []),
          ])
          if (cfgRes?.data) setConfig(cfgRes.data)
          if (portsList) setPorts(portsList)
          setRestartNotice('服务已恢复连接，配置已刷新')
        } catch {
          setRestartNotice('服务恢复检查失败，请手动刷新页面')
        } finally {
          setRestarting(false)
        }
      }, 5000)
    } catch (err: any) {
      setRestarting(false)
      const message = err?.message || '未知错误'
      // Break-Glass 锁定（429）等错误透传
      alert('重启失败: ' + message)
    }
  }

  return (
    <>
      <h2 style={{ marginBottom: 20, fontSize: 20 }}>服务器设置</h2>
      <div style={{ display: 'flex', gap: 8, marginBottom: 20, borderBottom: '1px solid var(--border)' }}>
        {[
          { key: 'basic', label: '基本配置' },
          { key: 'webvoice', label: 'Web 语音' },
          { key: 'ports', label: '端口配置' },
          { key: 'network', label: '网络配置' },
          { key: 'srv', label: 'SRV 记录' },
        ].map(item => (
          <button
            key={item.key}
            className="track-btn"
            style={{
              borderRadius: '6px 6px 0 0',
              borderBottom: serverSettingsTab === item.key ? '2px solid var(--accent)' : '2px solid transparent',
              background: serverSettingsTab === item.key ? 'var(--bg-primary)' : 'transparent',
            }}
            onClick={() => setServerSettingsTab(item.key as typeof serverSettingsTab)}
          >
            {item.label}
          </button>
        ))}
      </div>

      {serverSettingsTab === 'basic' && (
        <>
          {!config ? (
            <div className="admin-card" style={{ textAlign: 'center', color: 'var(--text-secondary)', padding: 40 }}>
              {dataLoading ? '加载配置中...' : '暂无配置数据'}
            </div>
          ) : (
            <>
              <div className="admin-card">
                <h3>基本配置</h3>
                <div className="stat-grid">
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="server-name">服务器名称</label>
                    <input id="server-name" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} value={config.serverName} onChange={e => setConfig({ ...config, serverName: e.target.value })} />
                  </div>
                  <div className="stat-box">
                    <div className="stat-label">注册开关</div>
                    <div style={{ marginTop: 8, display: 'flex', gap: 8 }}>
                      <button className={`track-btn ${config.allowRegister ? 'active' : ''}`} onClick={() => setConfig({ ...config, allowRegister: true })}><Check size={14} aria-hidden="true" /> 开启</button>
                      <button className={`track-btn ${!config.allowRegister ? 'active' : ''}`} onClick={() => setConfig({ ...config, allowRegister: false })}><X size={14} aria-hidden="true" /> 关闭</button>
                    </div>
                  </div>
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="max-users">最大用户数</label>
                    <input id="max-users" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={config.maxUsers} onChange={e => setConfig({ ...config, maxUsers: Number(e.target.value) })} />
                  </div>
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="max-file-size">最大文件大小 (MB)</label>
                    <input id="max-file-size" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" min={0} value={config.maxFileSize ?? 0} onChange={e => setConfig({ ...config, maxFileSize: Number(e.target.value) })} />
                    <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>0 表示不限（云文件上传大小限制）</div>
                  </div>
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="public-address">公网地址（旧版文件/CloudFS））</label>
                    <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
                      <input id="public-address" className="auth-input" style={{ margin: 0, flex: 1 }} value={config.publicAddress} onChange={e => setConfig({ ...config, publicAddress: e.target.value })} />
                      <button className="icon-btn" title="刷新"><RefreshCw size={14} aria-hidden="true" /></button>
                    </div>
                  </div>
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="api-port">服务端口</label>
                    <input id="api-port" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={config.apiPort} onChange={e => setConfig({ ...config, apiPort: Number(e.target.value) })} />
                  </div>
                  <div className="stat-box">
                    <div className="stat-label">管理端口 (只读)</div>
                    <div style={{ marginTop: 8, display: 'flex', alignItems: 'center', gap: 8, padding: '8px 12px', background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', color: 'var(--text-secondary)', fontSize: 14 }}>
                      <Lock size={14} aria-hidden="true" /> {config.adminPort}
                    </div>
                  </div>
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="livekit-port">语音端口 (LiveKit)</label>
                    <input id="livekit-port" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={config.livekitPort} onChange={e => setConfig({ ...config, livekitPort: Number(e.target.value) })} />
                  </div>
                  <div className="stat-box">
                    <label className="stat-label" htmlFor="vpn-port">虚拟局域网端口</label>
                    <input id="vpn-port" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={config.vpnPort} onChange={e => setConfig({ ...config, vpnPort: Number(e.target.value) })} />
                  </div>
                </div>
                <div style={{ marginTop: 16, textAlign: 'right' }}>
                  <button className="track-btn" onClick={async () => {
                    try { await updateAdminConfig(config); alert('配置已保存') } catch (e: any) { alert('保存失败: ' + (e?.message || '未知错误')) }
                  }}>保存更改</button>
                </div>
              </div>
              <div className="admin-card">
                <h3>端口信息</h3>
                <div className="stat-grid">
                  <div className="stat-box">
                    <div className="stat-value" style={{ fontSize: 18 }}>{config.apiPort}</div>
                    <div className="stat-label" style={{ marginTop: 4 }}>API / Web 端口</div>
                    <div style={{ marginTop: 6 }}><span style={protocolBadgeStyle('tcp')}>TCP</span></div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-value" style={{ fontSize: 18 }}>{config.adminPort}</div>
                    <div className="stat-label" style={{ marginTop: 4 }}>管理端口</div>
                    <div style={{ marginTop: 6 }}><span style={protocolBadgeStyle('tcp')}>TCP</span></div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-value" style={{ fontSize: 18 }}>{config.livekitPort}</div>
                    <div className="stat-label" style={{ marginTop: 4 }}>LiveKit WS</div>
                    <div style={{ marginTop: 6 }}><span style={protocolBadgeStyle('tcp')}>TCP</span></div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-value" style={{ fontSize: 18 }}>{(config as any).livekitTcpPort ?? config.livekitPort + 1}</div>
                    <div className="stat-label" style={{ marginTop: 4 }}>LiveKit TCP</div>
                    <div style={{ marginTop: 6 }}><span style={protocolBadgeStyle('tcp')}>TCP</span></div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-value" style={{ fontSize: 18 }}>{(config as any).livekitUdpPort ?? config.livekitPort + 2}</div>
                    <div className="stat-label" style={{ marginTop: 4 }}>LiveKit UDP</div>
                    <div style={{ marginTop: 6 }}><span style={protocolBadgeStyle('udp')}>UDP</span></div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-value" style={{ fontSize: 18 }}>{config.vpnPort}</div>
                    <div className="stat-label" style={{ marginTop: 4 }}>虚拟局域网端口</div>
                    <div style={{ marginTop: 6 }}><span style={protocolBadgeStyle('udp')}>UDP</span></div>
                  </div>
                </div>
              </div>
              {/* P1-3: 原「版本更新」卡片已移除。
                  服务端 GET /api/admin/update/check 是无发布渠道的占位实现，
                  卡片上的"最新版本 / 已是最新版本"是编造出来的结论。
                  在真正接入更新源之前不再展示该卡片。 */}
            </>
          )}
        </>
      )}

      {serverSettingsTab === 'webvoice' && (
        <div className="admin-card">
          <h3>Web 语音开关</h3>
          <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>Web 语音默认锁定，仅在完成外部网络配置并明确启用后，Web 用户端才能使用语音功能。</div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
            <button className={`toggle ${networkConfig.webVoiceEnabled ? 'on' : ''}`} onClick={() => { setNetworkConfig({ ...networkConfig, webVoiceEnabled: !networkConfig.webVoiceEnabled }) }} />
            <span style={{ fontSize: 14, color: 'var(--text-secondary)' }}>{networkConfig.webVoiceEnabled ? '已启用 Web 语音' : '已锁定 Web 语音'}</span>
          </div>
          <div className="stat-grid" style={{ gridTemplateColumns: 'repeat(2, 1fr)' }}>
            <div className="stat-box">
              <div className="stat-label">LiveKit URL</div>
              <div style={{ marginTop: 8, padding: '8px 12px', background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', color: 'var(--text-secondary)', fontSize: 14, fontFamily: 'monospace', wordBreak: 'break-all' }}>
                {networkConfig.useHttps ? 'wss' : 'ws'}://{networkConfig.externalHost || 'localhost'}{networkConfig.useHttps && networkConfig.externalLiveKitWsPort === 443 ? '' : `:${networkConfig.externalLiveKitWsPort}`}/livekit
              </div>
            </div>
            <div className="stat-box">
              <div className="stat-label">媒体 UDP 端口</div>
              <div style={{ marginTop: 8, padding: '8px 12px', background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', color: 'var(--text-secondary)', fontSize: 14, fontFamily: 'monospace' }}>
                {networkConfig.externalMediaUdpPort}
              </div>
            </div>
          </div>
          <div style={{ marginTop: 16, textAlign: 'right' }}>
            <button className="track-btn" disabled={networkSaving || !networkConfigDirty} onClick={handleSaveNetwork}>{networkSaving ? '保存中...' : '保存更改'}</button>
          </div>
        </div>
      )}

      {serverSettingsTab === 'ports' && (
        <div className="admin-card">
          <h3>端口配置</h3>
          <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
            查看服务监听的端口与外部映射。修改外部端口将直接保存到网络配置；NAT 环境下外部端口必须与内部端口一致，否则 LiveKit 媒体无法连通。
          </div>
          {portsLoading ? (
            <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-secondary)' }}>加载端口列表中...</div>
          ) : ports.length === 0 ? (
            <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-secondary)' }}>暂无端口数据</div>
          ) : (
            <>
              {isNatEnvironment && (
                <div style={{ color: '#b91c1c', padding: '10px 12px', border: '1px solid #b91c1c', background: 'rgba(185,28,28,0.08)', borderRadius: 6, marginBottom: 12, fontSize: 13, display: 'flex', alignItems: 'center', gap: 8 }}>
                  <AlertTriangle size={16} aria-hidden="true" />
                  <span>检测到 NAT 网络，外部映射端口号必须与内部端口号一致，否则 LiveKit 媒体无法连通。</span>
                </div>
              )}
              <div style={{ overflowX: 'auto' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
                  <thead>
                    <tr style={{ textAlign: 'left', borderBottom: '1px solid var(--border)', color: 'var(--text-secondary)' }}>
                      <th style={{ padding: '8px 10px' }}>端口名称</th>
                      <th style={{ padding: '8px 10px' }}>协议</th>
                      <th style={{ padding: '8px 10px' }}>内部端口</th>
                      <th style={{ padding: '8px 10px' }}>外部端口</th>
                      <th style={{ padding: '8px 10px' }}>用途</th>
                      <th style={{ padding: '8px 10px' }}>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ports.map(port => (
                      <tr key={port.key} style={{ borderBottom: '1px solid var(--border)' }}>
                        <td style={{ padding: '8px 10px' }}>{port.label}</td>
                        <td style={{ padding: '8px 10px' }}>
                          <span style={protocolBadgeStyle(port.protocol)}>{port.protocol.toUpperCase()}</span>
                        </td>
                        <td style={{ padding: '8px 10px', fontFamily: 'monospace' }}>{port.internalPort}</td>
                        <td style={{ padding: '8px 10px', fontFamily: 'monospace' }}>{port.externalPort}</td>
                        <td style={{ padding: '8px 10px', color: 'var(--text-secondary)' }}>{port.description}</td>
                        <td style={{ padding: '8px 10px' }}>
                          <button className="track-btn" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => openEditPortModal(port)}>编辑</button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}

          {isOwner && (
            <div style={{ marginTop: 16, paddingTop: 16, borderTop: '1px solid var(--border)' }}>
              <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 8 }}>
                重启服务会使所有连接短暂中断（约 3-5 秒）。该操作受后端 Break-Glass 锁定保护，频繁重启将被限制。
              </div>
              <button className="track-btn" onClick={() => setShowRestartModal(true)}>
                <Power size={14} aria-hidden="true" style={{ marginRight: 6, verticalAlign: 'middle' }} />
                重启服务
              </button>
            </div>
          )}

          {restartNotice && (
            <div style={{ marginTop: 12, padding: '10px 12px', borderRadius: 6, background: 'rgba(40,167,69,0.1)', border: '1px solid #28a745', color: '#1b6b2b', fontSize: 13, display: 'flex', alignItems: 'center', gap: 8 }}>
              <RefreshCw size={14} aria-hidden="true" style={restarting ? { animation: 'spin 1s linear infinite' } : undefined} />
              <span>{restartNotice}</span>
            </div>
          )}
        </div>
      )}

      {/* Task 8.5: 端口编辑弹窗 */}
      {editingPort && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={closeEditPortModal}>
          <div className="admin-card" role="dialog" aria-modal="true" aria-label="编辑端口" style={{ width: 420, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={protocolBadgeStyle(editingPort.protocol)}>{editingPort.protocol.toUpperCase()}</span>
              编辑端口 - {editingPort.label}
            </h3>
            <div className="stat-grid" style={{ marginTop: 12 }}>
              <div className="stat-box">
                <div className="stat-label">内部端口（只读）</div>
                <div style={{ marginTop: 8, padding: '8px 12px', background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', color: 'var(--text-secondary)', fontSize: 14, fontFamily: 'monospace' }}>
                  <Lock size={14} aria-hidden="true" style={{ marginRight: 6, verticalAlign: 'middle' }} />{editingPort.internalPort}
                </div>
              </div>
              <div className="stat-box">
                <label className="stat-label" htmlFor="edit-external-port">外部端口</label>
                <input
                  id="edit-external-port"
                  className="auth-input"
                  style={{ marginTop: 8, marginBottom: 0, fontFamily: 'monospace' }}
                  type="number"
                  min={1}
                  max={65535}
                  disabled={editingPort.key === 'vpn' || savingPort}
                  value={editingExternalPort}
                  onChange={e => setEditingExternalPort(e.target.value)}
                />
                {editingPort.key === 'vpn' && (
                  <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
                    VPN 内外端口通常一致，无需修改。
                  </div>
                )}
              </div>
            </div>
            <div className="stat-box" style={{ marginTop: 12 }}>
              <div className="stat-label">用途说明</div>
              <div style={{ marginTop: 8, padding: '8px 12px', background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', color: 'var(--text-secondary)', fontSize: 13 }}>
                {editingPort.description}
              </div>
            </div>
            {editingPort.natRequired && (
              <div style={{ marginTop: 12, padding: '10px 12px', border: '1px solid #b91c1c', background: 'rgba(185,28,28,0.08)', borderRadius: 6, color: '#b91c1c', fontSize: 13, display: 'flex', alignItems: 'center', gap: 8 }}>
                <AlertTriangle size={14} aria-hidden="true" />
                <span>NAT 环境下，外部端口必须与内部端口（{editingPort.internalPort}）一致。</span>
              </div>
            )}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={closeEditPortModal} disabled={savingPort}>取消</button>
              <button className="track-btn" onClick={handleSavePort} disabled={savingPort || editingPort.key === 'vpn'}>
                {savingPort ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Task 9: 服务重启二次确认对话框 */}
      {showRestartModal && (
        <div className="modal-overlay" role="presentation" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => !restarting && setShowRestartModal(false)}>
          <div className="admin-card" role="dialog" aria-modal="true" aria-label="确认重启服务" style={{ width: 400, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <AlertTriangle size={18} color="#b91c1c" aria-hidden="true" /> 确认重启服务
            </h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14, marginTop: 8 }}>
              确认要重启 <strong>RidgeRiceTalk</strong> 服务吗？重启期间所有连接将短暂中断（约 3-5 秒）。
            </p>
            <div style={{ marginTop: 12, padding: '10px 12px', border: '1px solid #b91c1c', background: 'rgba(185,28,28,0.08)', borderRadius: 6, color: '#b91c1c', fontSize: 13, display: 'flex', alignItems: 'center', gap: 8 }}>
              <Lock size={14} aria-hidden="true" />
              <span>Break-Glass 提示：此操作受后端锁定保护，频繁重启将被限制（HTTP 429）。</span>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button className="track-btn" onClick={() => setShowRestartModal(false)} disabled={restarting}>取消</button>
              <button className="track-btn" style={{ background: 'var(--danger)', color: '#fff' }} onClick={handleConfirmRestart} disabled={restarting}>
                {restarting ? '重启中...' : '确认重启'}
              </button>
            </div>
          </div>
        </div>
      )}

      {serverSettingsTab === 'network' && (
        <>
          <div className="admin-card">
            <h3>外部网络配置</h3>
            <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>配置客户端（含 Web 端与 Windows 客户端）访问服务器时使用的外部地址与端口。</div>
            <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
              <button className="track-btn" disabled={detectingNetwork} onClick={async () => {
                setDetectingNetwork(true)
                try {
                  const res = await detectAdminNetwork()
                  const result = res.result || {}
                  setDetectionResult(result)
                  setNetworkRecommendations(res.recommendations || [])
                  setNetworkConfig({
                    ...networkConfig,
                    externalHost: result.publicIpv4 || result.localIpv4s?.[0] || networkConfig.externalHost,
                  })
                } catch (err: any) {
                  alert('探测失败: ' + (err.message || '未知错误'))
                } finally {
                  setDetectingNetwork(false)
                }
              }}>{detectingNetwork ? '探测中...' : '自动探测网络'}</button>
              <button className="track-btn" onClick={async () => {
                try {
                  const res = await verifyAdminNetwork(networkConfig)
                  // 后端 VerifyNetwork 返回顶层 {success, result}，无信封包裹。
                  setNetworkVerifyResult(res.result || null)
                } catch (err: any) {
                  alert('校验失败: ' + (err.message || '未知错误'))
                }
              }}>校验配置</button>
            </div>
            {detectionResult && (
              <div style={{ marginBottom: 12, padding: 12, background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', fontSize: 13, color: 'var(--text-secondary)' }}>
                <div>本地 IPv4: {(detectionResult.localIpv4s || []).join(', ') || '-'}</div>
                <div>公网 IPv4: {detectionResult.publicIpv4 || '-'}</div>
                <div>UPnP 支持: {detectionResult.upnpSupported ? '是' : '否'}</div>
              </div>
            )}
            {networkRecommendations.length > 0 && (
              <div style={{ marginBottom: 12, padding: 12, background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', fontSize: 13 }}>
                <div style={{ fontWeight: 500, marginBottom: 8, color: 'var(--text-primary)' }}>推荐穿透方案</div>
                {networkRecommendations.map((s, i) => (
                  <div key={i} style={{ display: 'flex', gap: 8, marginBottom: 4, color: 'var(--text-secondary)' }}>
                    <span style={{ minWidth: 24 }}>{s.priority}.</span>
                    <span style={{ fontWeight: 500, minWidth: 140, color: 'var(--text-primary)' }}>{s.method}</span>
                    <span>{s.reason}</span>
                  </div>
                ))}
              </div>
            )}
            <div className="stat-grid">
              <div className="stat-box">
                <label className="stat-label" htmlFor="external-host">外部域名 / 公网 IP</label>
                <input id="external-host" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} value={networkConfig.externalHost} onChange={e => { setNetworkConfig({ ...networkConfig, externalHost: e.target.value }) }} />
              </div>
              <div className="stat-box">
                <label className="stat-label" htmlFor="external-http-port">外部 HTTP 端口</label>
                <input id="external-http-port" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={networkConfig.externalHttpPort} onChange={e => { setNetworkConfig({ ...networkConfig, externalHttpPort: Number(e.target.value) }) }} />
              </div>
              <div className="stat-box">
                <label className="stat-label" htmlFor="external-livekit-ws-port">外部 LiveKit WS 端口</label>
                <input id="external-livekit-ws-port" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={networkConfig.externalLiveKitWsPort} onChange={e => { setNetworkConfig({ ...networkConfig, externalLiveKitWsPort: Number(e.target.value) }) }} />
              </div>
              <div className="stat-box">
                <label className="stat-label" htmlFor="external-media-udp-port">外部媒体 UDP 端口</label>
                <input id="external-media-udp-port" className="auth-input" style={{ marginTop: 8, marginBottom: 0 }} type="number" value={networkConfig.externalMediaUdpPort} onChange={e => { setNetworkConfig({ ...networkConfig, externalMediaUdpPort: Number(e.target.value) }) }} />
              </div>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, marginTop: 12 }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)' }}>
                <input type="checkbox" checked={networkConfig.useHttps} onChange={e => { setNetworkConfig({ ...networkConfig, useHttps: e.target.checked }) }} /> 使用 HTTPS
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)' }}>
                <input type="checkbox" checked={networkConfig.upnpEnabled} onChange={e => { setNetworkConfig({ ...networkConfig, upnpEnabled: e.target.checked }) }} /> 启用 UPnP（MVP 占位）
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)' }}>
                <input type="checkbox" checked={networkConfig.turnTcpFallbackEnabled} onChange={e => { setNetworkConfig({ ...networkConfig, turnTcpFallbackEnabled: e.target.checked }) }} /> 启用 TURN TCP 443 fallback
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)' }}>
                <input type="checkbox" checked={networkConfig.clientAccessEnabled} onChange={e => { setNetworkConfig({ ...networkConfig, clientAccessEnabled: e.target.checked }) }} /> 启用 Windows 客户端访问
              </label>
            </div>
            {networkVerifyResult && (
              <div style={{ marginTop: 12, padding: 12, background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', fontSize: 13, color: 'var(--text-secondary)' }}>
                <div>Server URL: {networkVerifyResult.serverUrl}</div>
                <div>LiveKit URL: {networkVerifyResult.liveKitUrl}</div>
                {(networkVerifyResult.notes || []).map((n: string, i: number) => <div key={i} style={{ marginTop: 4, color: 'var(--warning, #f59e0b)' }}>{n}</div>)}
              </div>
            )}
            <div style={{ marginTop: 16, textAlign: 'right' }}>
              <button className="track-btn" disabled={networkSaving || !networkConfigDirty} onClick={handleSaveNetwork}>{networkSaving ? '保存中...' : '保存更改'}</button>
            </div>
          </div>
        </>
      )}

      {serverSettingsTab === 'srv' && (
        <div className="admin-card">
          <h3>SRV 记录建议</h3>
          <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>Windows 客户端支持 SRV 记录自动发现。若使用 DNS 解析，请添加以下记录（将 example.com 替换为您的域名）。</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {(() => {
              const host = networkConfig.externalHost || 'example.com'
              const httpPort = networkConfig.externalHttpPort || 443
              const udpPort = networkConfig.externalMediaUdpPort || 7882
              const records = [
                `_rrt._tcp.${host}. 300 IN SRV 10 10 ${httpPort} ${host}.`,
                `_rrt-media._udp.${host}. 300 IN SRV 10 10 ${udpPort} ${host}.`,
              ]
              return records.map((r, i) => (
                <div key={i} style={{ padding: 10, background: 'var(--bg-primary)', borderRadius: 6, border: '1px solid var(--border)', fontFamily: 'monospace', fontSize: 13, wordBreak: 'break-all', display: 'flex', justifyContent: 'space-between', gap: 8 }}>
                  <span>{r}</span>
                  <button className="icon-btn" title="复制" onClick={() => navigator.clipboard.writeText(r)}><Copy size={14} aria-hidden="true" /></button>
                </div>
              ))
            })()}
          </div>
        </div>
      )}
    </>
  )
}
