import { useEffect, useState } from 'react'
import { Users, Mic, MonitorUp, Wifi } from 'lucide-react'
import type { RealtimeMetrics } from '../types/admin'
import { getAdminRealtimeMetrics, REALTIME_METRICS_POLL_MS } from '../utils/realtimeMetrics'

// M2 观测性：实时指标卡片。自含 5s 轮询（挂载即拉一次，卸载清理定时器），
// 展示：在线用户数、语音房间数与每房人数、进行中屏幕共享数。
// 拉取失败静默保留上一次数据（与 loadHighFreqData 的容错策略一致），
// 首次未拉到时显示「暂无数据」。
export default function RealtimeMetricsCard() {
  const [metrics, setMetrics] = useState<RealtimeMetrics | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const res = await getAdminRealtimeMetrics()
        if (!cancelled && res?.data) setMetrics(res.data)
      } catch {
        // 单卡片数据源失败不冒泡：保留旧值，下一拍重试
      }
    }
    load()
    const timer = setInterval(load, REALTIME_METRICS_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  const rooms = metrics?.rooms ?? []

  return (
    <div className="admin-card" data-testid="realtime-metrics-card">
      <h3>实时指标</h3>
      <div className="stat-grid" style={{ marginTop: 12, gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))' }}>
        <div className="stat-box">
          <div className="stat-label"><Wifi size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 4 }} />在线用户</div>
          <div className="stat-value" data-testid="realtime-online-users">{metrics ? metrics.onlineUsers : '-'}</div>
          <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>WebSocket 去重连接用户</div>
        </div>
        <div className="stat-box">
          <div className="stat-label"><Mic size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 4 }} />语音房间</div>
          <div className="stat-value" data-testid="realtime-voice-rooms">{metrics ? metrics.voiceRooms : '-'}</div>
          <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>有成员在房的房间数</div>
        </div>
        <div className="stat-box">
          <div className="stat-label"><MonitorUp size={12} aria-hidden="true" style={{ verticalAlign: 'middle', marginRight: 4 }} />屏幕共享</div>
          <div className="stat-value" data-testid="realtime-screenshares">{metrics ? metrics.activeScreenshares : '-'}</div>
          <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>进行中的共享会话</div>
        </div>
      </div>
      {/* 语音房间逐房人数（rooms 为空时隐藏整块） */}
      {rooms.length > 0 && (
        <div style={{ marginTop: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 6, display: 'flex', alignItems: 'center', gap: 4 }}>
            <Users size={12} aria-hidden="true" /> 每房人数
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {rooms.map(room => (
              <span
                key={room.roomId}
                data-testid="realtime-room-chip"
                title={room.roomId}
                style={{
                  display: 'inline-flex', alignItems: 'center', gap: 6,
                  background: 'var(--bg-primary)', borderRadius: 6,
                  padding: '4px 10px', fontSize: 12, color: 'var(--text-primary)',
                }}
              >
                <Mic size={11} aria-hidden="true" style={{ color: 'var(--accent)' }} />
                <span style={{ fontFamily: 'monospace' }}>{room.roomId}</span>
                <strong>{room.participantCount}</strong>
              </span>
            ))}
          </div>
        </div>
      )}
      <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 8 }}>
        {metrics ? `每 ${REALTIME_METRICS_POLL_MS / 1000}s 自动刷新` : '加载实时数据中...'}
      </div>
    </div>
  )
}
