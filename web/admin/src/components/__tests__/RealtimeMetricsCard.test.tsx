import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import RealtimeMetricsCard from '../RealtimeMetricsCard'
import { REALTIME_METRICS_POLL_MS } from '../../utils/realtimeMetrics'
import type { RealtimeMetrics } from '../../types/admin'

// mock shared api-core：本组件经 utils/realtimeMetrics 调用 apiGet。
vi.mock('../../../../shared/api-core', () => ({
  apiGet: vi.fn(),
}))

import { apiGet } from '../../../../shared/api-core'
const mockApiGet = vi.mocked(apiGet)

const metrics: RealtimeMetrics = {
  rooms: [
    { roomId: 'vr_room_a', participantCount: 3 },
    { roomId: 'vr_room_b', participantCount: 1 },
  ],
  onlineUsers: 12,
  activeScreenshares: 1,
  voiceRooms: 2,
}

function ok(res: RealtimeMetrics) {
  return Promise.resolve({ code: 'OK', data: res })
}

describe('RealtimeMetricsCard', () => {
  beforeEach(() => {
    // shouldAdvanceTime：假时钟跟随真实时间推进，testing-library 的 waitFor
    // 依赖的 setTimeout 仍会触发；同时保留 advanceTimersByTime 手动快进能力。
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mockApiGet.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('拉取并渲染在线用户/语音房间/屏幕共享与每房人数', async () => {
    mockApiGet.mockImplementation(() => ok(metrics))

    render(<RealtimeMetricsCard />)

    // 首拍即拉取，路径为 admin 实时指标端点
    await waitFor(() => {
      expect(mockApiGet).toHaveBeenCalledWith('/api/admin/metrics/realtime')
    })
    expect(await screen.findByTestId('realtime-online-users')).toHaveTextContent('12')
    expect(screen.getByTestId('realtime-voice-rooms')).toHaveTextContent('2')
    expect(screen.getByTestId('realtime-screenshares')).toHaveTextContent('1')
    const chips = screen.getAllByTestId('realtime-room-chip')
    expect(chips).toHaveLength(2)
    expect(chips[0]).toHaveTextContent('vr_room_a')
    expect(chips[0]).toHaveTextContent('3')
    expect(chips[1]).toHaveTextContent('vr_room_b')
    expect(chips[1]).toHaveTextContent('1')
  })

  it('rooms 为空时隐藏每房人数区块', async () => {
    mockApiGet.mockImplementation(() => ok({ ...metrics, rooms: [], voiceRooms: 0 }))

    render(<RealtimeMetricsCard />)
    await waitFor(() => {
      expect(screen.getByTestId('realtime-voice-rooms')).toHaveTextContent('0')
    })
    expect(screen.queryByTestId('realtime-room-chip')).toBeNull()
  })

  it('按 5s 周期轮询并更新数据', async () => {
    mockApiGet.mockImplementation(() => ok(metrics))

    render(<RealtimeMetricsCard />)
    await waitFor(() => {
      expect(screen.getByTestId('realtime-online-users')).toHaveTextContent('12')
    })
    const firstCalls = mockApiGet.mock.calls.length
    expect(firstCalls).toBeGreaterThanOrEqual(1)

    const updated: RealtimeMetrics = { ...metrics, onlineUsers: 30, activeScreenshares: 2 }
    mockApiGet.mockImplementation(() => ok(updated))

    await vi.advanceTimersByTimeAsync(REALTIME_METRICS_POLL_MS)
    expect(mockApiGet.mock.calls.length).toBeGreaterThan(firstCalls)
    await waitFor(() => {
      expect(screen.getByTestId('realtime-online-users')).toHaveTextContent('30')
    })
    expect(screen.getByTestId('realtime-screenshares')).toHaveTextContent('2')
  })

  it('拉取失败时保留旧数据且不抛错', async () => {
    mockApiGet.mockImplementation(() => ok(metrics))
    render(<RealtimeMetricsCard />)
    await waitFor(() => {
      expect(screen.getByTestId('realtime-online-users')).toHaveTextContent('12')
    })

    mockApiGet.mockImplementation(() => Promise.reject(new Error('network down')))
    await vi.advanceTimersByTimeAsync(REALTIME_METRICS_POLL_MS)
    // 旧值仍在，无异常冒泡
    expect(screen.getByTestId('realtime-online-users')).toHaveTextContent('12')
  })
})
