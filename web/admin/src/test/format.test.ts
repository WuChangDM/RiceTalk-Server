import { describe, it, expect } from 'vitest'
import { formatBytes, formatBytesFromBytes, formatPercent, formatUptime } from '../utils/format'

describe('format utils', () => {
  it('formatBytes converts MB to GB when above 1024', () => {
    expect(formatBytes(2048)).toBe('2.00 GB')
  })

  it('formatBytes keeps MB below 1024', () => {
    expect(formatBytes(512)).toBe('512.0 MB')
  })

  it('formatBytesFromBytes handles byte scale', () => {
    expect(formatBytesFromBytes(0)).toBe('0 B')
    expect(formatBytesFromBytes(512)).toBe('512 B')
    expect(formatBytesFromBytes(1024)).toBe('1.0 KB')
    expect(formatBytesFromBytes(1024 * 1024)).toBe('1.0 MB')
    expect(formatBytesFromBytes(1024 * 1024 * 1024)).toBe('1.0 GB')
  })

  it('formatPercent rounds to one decimal', () => {
    expect(formatPercent(12.345)).toBe('12.3')
  })

  it('formatUptime formats seconds into days/hours/minutes', () => {
    expect(formatUptime(86400 * 3 + 3600 * 5 + 60 * 7 + 9)).toBe('3d 5h 7m')
  })
})
