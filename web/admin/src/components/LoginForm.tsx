import { useState } from 'react'
import { Settings } from 'lucide-react'
import {
  adminBootstrapVerify, adminBootstrapRegister, adminLogin,
  detectAdminNetwork, updateAdminNetwork, getServerInfo,
} from '../../../shared/api-core'
import type { AdminSession } from '../../../shared/types'
import type { ServerInfoResponse } from '../../../shared/types'
import type { NetworkConfig } from '../types/admin'
import { EMPTY_NETWORK_CONFIG } from '../types/admin'

type ServerInfoData = ServerInfoResponse['data']

// 从 ws(s)://host:port 形态的 LiveKit 地址解析端口；解析失败返回 0（由调用方兜底）。
const portFromUrl = (url: string): number => {
  try { return Number(new URL(url).port) || 0 } catch { return 0 }
}

interface LoginFormProps {
  bootstrapInitialized: boolean | null
  onSessionChange: (session: AdminSession | null) => void
}

export default function LoginForm({ bootstrapInitialized, onSessionChange }: LoginFormProps) {
  const [loginError, setLoginError] = useState('')
  const [loginEmail, setLoginEmail] = useState('')
  const [loginPassword, setLoginPassword] = useState('')

  // Bootstrap flow states
  const [bootstrapStep, setBootstrapStep] = useState<1 | 2 | 3>(1)
  const [bootstrapToken, setBootstrapToken] = useState('')
  const [bootstrapUsername, setBootstrapUsername] = useState('')
  const [bootstrapDisplayName, setBootstrapDisplayName] = useState('')
  const [bootstrapEmail, setBootstrapEmail] = useState('')
  const [bootstrapPassword, setBootstrapPassword] = useState('')
  const [bootstrapSpaceName, setBootstrapSpaceName] = useState('')
  const [bootstrapError, setBootstrapError] = useState('')
  const [verifyingToken, setVerifyingToken] = useState(false)
  const [creatingOwner, setCreatingOwner] = useState(false)
  const [bootstrapUser, setBootstrapUser] = useState<{ id: string; username: string; role: string } | null>(null)
  const [bootstrapNetwork, setBootstrapNetwork] = useState<NetworkConfig>(EMPTY_NETWORK_CONFIG)
  const [detectingNetwork, setDetectingNetwork] = useState(false)

  // U14：网络步初值不再使用 443+HTTPS 假值（会覆盖服务端智能默认，远程部署必错）。
  // 从 /server/info（公开接口，返回服务端实际配置端口）推导；拉取失败时至少用
  // admin 页自身地址填 host，端口留待人工核对。
  // 注意：该接口响应为裸字段对象（无 {code,data} 信封），做双形态兼容。
  const applyNetworkDefaults = async () => {
    let info: ServerInfoData | null = null
    try {
      const raw: any = await getServerInfo()
      const cand = raw && raw.apiPort != null ? raw : raw?.data
      info = cand && cand.apiPort != null ? (cand as ServerInfoData) : null
    } catch { /* /server/info 不可达，走 location 兜底 */ }
    setBootstrapNetwork(prev => ({
      ...prev,
      externalHost: prev.externalHost || window.location.hostname,
      externalHttpPort: Number(info?.apiPort) || prev.externalHttpPort,
      externalAdminPort: Number(info?.adminPort) || prev.externalAdminPort,
      externalLiveKitWsPort: Number(info?.livekitPort) || portFromUrl(String(info?.livekitUrl || '')) || 7880,
      externalMediaUdpPort: Number(info?.mediaUdpPort) || prev.externalMediaUdpPort,
      useHttps: typeof info?.useHttps === 'boolean' ? info.useHttps : prev.useHttps,
    }))
  }

  const handleLogin = async () => {
    setLoginError('')
    try {
      const res = await adminLogin(loginEmail, loginPassword)
      if (res.data?.accessToken || res.data?.token) {
        localStorage.setItem('rrt_admin_token', res.data.accessToken || res.data.token)
      }
      const user = res.data?.user
      if (user) {
        onSessionChange({ id: user.id, username: user.username, role: user.role })
      }
    } catch (err: any) {
      setLoginError(err.message || '登录失败')
    }
  }

  return (
    <div className="auth-screen">
      <div className="auth-box">
        {bootstrapInitialized === false ? (
          <>
            <h1 style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8 }}>
              <Settings size={22} aria-hidden="true" /> RidgeRiceTalk 管理端初始化
            </h1>
            {bootstrapStep === 1 ? (
              <>
                <div className="auth-hint" style={{ marginBottom: 12 }}>服务器尚未初始化，请输入一次性设置令牌</div>
                {bootstrapError && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 8, textAlign: 'center' }}>{bootstrapError}</div>}
                <input
                  className="auth-input"
                  aria-label="一次性设置令牌"
                  placeholder="一次性设置令牌"
                  value={bootstrapToken}
                  onChange={e => setBootstrapToken(e.target.value)}
                  disabled={verifyingToken}
                />
                <button
                  className="auth-btn"
                  disabled={verifyingToken || !bootstrapToken.trim()}
                  onClick={async () => {
                    setBootstrapError('')
                    setVerifyingToken(true)
                    try {
                      const res = await adminBootstrapVerify(bootstrapToken.trim())
                      if (res.data?.valid === true) {
                        setBootstrapStep(2)
                      } else {
                        setBootstrapError('令牌无效或已过期')
                      }
                    } catch (err: any) {
                      setBootstrapError(err.message || '令牌无效或已过期')
                    } finally {
                      setVerifyingToken(false)
                    }
                  }}
                >
                  {verifyingToken ? '验证中...' : '验证令牌'}
                </button>
              </>
            ) : bootstrapStep === 2 ? (
              <>
                <div className="auth-hint" style={{ marginBottom: 12 }}>令牌验证通过，请创建 Owner 账户</div>
                {bootstrapError && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 8, textAlign: 'center' }}>{bootstrapError}</div>}
                <input className="auth-input" aria-label="用户名" placeholder="用户名" value={bootstrapUsername} onChange={e => setBootstrapUsername(e.target.value)} />
                <input className="auth-input" aria-label="显示名" placeholder="显示名（可选，默认同用户名）" value={bootstrapDisplayName} onChange={e => setBootstrapDisplayName(e.target.value)} />
                <input className="auth-input" aria-label="邮箱" placeholder="邮箱" value={bootstrapEmail} onChange={e => setBootstrapEmail(e.target.value)} />
                <input className="auth-input" aria-label="密码" placeholder="密码（至少 8 位）" type="password" value={bootstrapPassword} onChange={e => setBootstrapPassword(e.target.value)} />
                <input className="auth-input" aria-label="空间名称" placeholder="空间名称（可选，默认 RidgeRiceTalk）" value={bootstrapSpaceName} onChange={e => setBootstrapSpaceName(e.target.value)} />
                <button
                  className="auth-btn"
                  disabled={creatingOwner}
                  onClick={async () => {
                    setBootstrapError('')
                    if (!bootstrapUsername.trim() || !bootstrapEmail.trim() || !bootstrapPassword.trim()) {
                      setBootstrapError('请填写所有必填字段')
                      return
                    }
                    if (bootstrapPassword.length < 8) {
                      setBootstrapError('密码至少 8 位')
                      return
                    }
                    setCreatingOwner(true)
                    try {
                      const res = await adminBootstrapRegister({
                        bootstrapToken: bootstrapToken.trim(),
                        username: bootstrapUsername.trim(),
                        displayName: bootstrapDisplayName.trim() || bootstrapUsername.trim(),
                        email: bootstrapEmail.trim(),
                        password: bootstrapPassword,
                        spaceName: bootstrapSpaceName.trim() || undefined,
                      })
                      const d = res.data
                      const user = d?.user
                      if (user) {
                        setBootstrapUser({ id: user.id, username: user.username, role: user.role as 'OWNER' | 'ADMIN' })
                      }
                      if (d?.accessToken || d?.token) {
                        localStorage.setItem('rrt_admin_token', d.accessToken || d.token)
                        setBootstrapStep(3)
                        void applyNetworkDefaults()
                      } else {
                        setBootstrapError('初始化响应异常')
                      }
                    } catch (err: any) {
                      setBootstrapError(err.message || '初始化失败')
                    } finally {
                      setCreatingOwner(false)
                    }
                  }}
                >
                  {creatingOwner ? '初始化中...' : '下一步：配置网络'}
                </button>
                <button className="auth-link" onClick={() => { setBootstrapStep(1); setBootstrapError('') }}>返回令牌验证</button>
              </>
            ) : bootstrapStep === 3 ? (
              <>
                <div className="auth-hint" style={{ marginBottom: 12 }}>Owner 账户已创建，请配置服务器外部网络</div>
                {bootstrapError && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 8, textAlign: 'center' }}>{bootstrapError}</div>}
                <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
                  <button
                    className="track-btn"
                    disabled={detectingNetwork}
                    onClick={async () => {
                      setDetectingNetwork(true)
                      setBootstrapError('')
                      try {
                        const res = await detectAdminNetwork()
                        const result = res.result || {}
                        setBootstrapNetwork(prev => ({
                          ...prev,
                          externalHost: result.publicIpv4 || result.localIpv4s?.[0] || prev.externalHost,
                        }))
                      } catch (err: any) {
                        setBootstrapError(err.message || '网络探测失败')
                      } finally {
                        setDetectingNetwork(false)
                      }
                    }}
                  >
                    {detectingNetwork ? '探测中...' : '自动探测'}
                  </button>
                </div>
                <input className="auth-input" aria-label="外部域名或IP" placeholder="外部域名或公网 IP" value={bootstrapNetwork.externalHost} onChange={e => setBootstrapNetwork({ ...bootstrapNetwork, externalHost: e.target.value })} />
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
                  <input className="auth-input" aria-label="HTTP端口" type="number" placeholder="HTTP 端口" value={bootstrapNetwork.externalHttpPort} onChange={e => setBootstrapNetwork({ ...bootstrapNetwork, externalHttpPort: Number(e.target.value) })} />
                  <input className="auth-input" aria-label="LiveKit WS端口" type="number" placeholder="LiveKit WS 端口" value={bootstrapNetwork.externalLiveKitWsPort} onChange={e => setBootstrapNetwork({ ...bootstrapNetwork, externalLiveKitWsPort: Number(e.target.value) })} />
                </div>
                <input className="auth-input" aria-label="媒体UDP端口" type="number" placeholder="媒体 UDP 端口" value={bootstrapNetwork.externalMediaUdpPort} onChange={e => setBootstrapNetwork({ ...bootstrapNetwork, externalMediaUdpPort: Number(e.target.value) })} />
                <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 4 }}>
                  <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)' }}>
                    <input type="checkbox" checked={bootstrapNetwork.useHttps} onChange={e => setBootstrapNetwork({ ...bootstrapNetwork, useHttps: e.target.checked })} /> 使用 HTTPS
                  </label>
                  <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--text-secondary)' }}>
                    <input type="checkbox" checked={bootstrapNetwork.webVoiceEnabled} onChange={e => setBootstrapNetwork({ ...bootstrapNetwork, webVoiceEnabled: e.target.checked })} /> 启用 Web 语音
                  </label>
                </div>
                <button
                  className="auth-btn"
                  disabled={creatingOwner || !bootstrapNetwork.externalHost.trim()}
                  onClick={async () => {
                    setBootstrapError('')
                    setCreatingOwner(true)
                    try {
                      await updateAdminNetwork(bootstrapNetwork)
                      if (bootstrapUser) {
                        onSessionChange({ id: bootstrapUser.id, username: bootstrapUser.username, role: bootstrapUser.role as 'OWNER' | 'ADMIN' })
                      }
                    } catch (err: any) {
                      setBootstrapError(err.message || '网络配置保存失败')
                    } finally {
                      setCreatingOwner(false)
                    }
                  }}
                >
                  {creatingOwner ? '保存中...' : '完成初始化'}
                </button>
              </>
            ) : null}
          </>
        ) : (
          <>
            <h1 style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8 }}>
              <Settings size={22} aria-hidden="true" /> RidgeRiceTalk 管理端
            </h1>
            {loginError && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 8, textAlign: 'center' }}>{loginError}</div>}
            <input className="auth-input" aria-label="邮箱" placeholder="管理员邮箱" type="email" autoComplete="email" value={loginEmail} onChange={e => setLoginEmail(e.target.value)} />
            <input className="auth-input" aria-label="密码" placeholder="密码" type="password" autoComplete="current-password" value={loginPassword} onChange={e => setLoginPassword(e.target.value)} />
            <button className="auth-btn" onClick={handleLogin}>登录</button>
            <div className="auth-hint">请输入管理员账号密码登录</div>
          </>
        )}
      </div>
    </div>
  )
}
