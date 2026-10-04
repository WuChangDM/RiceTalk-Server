import { config } from '../config.js';
import { moduleLogger } from '../logger.js';
import type { Track } from '../types/botSession.js';

const log = moduleLogger('music-source-resolver');
const RESOLVE_TIMEOUT_MS = 10_000;
const EXPIRY_SAFETY_WINDOW_MS = 5 * 60 * 1000;

interface ResolveResponse {
  url?: unknown;
  expiresAt?: unknown;
}

function usableCachedURL(track: Track, now = Date.now()): string | undefined {
  if (!track.audioUrl) return undefined;
  if (track.source !== 'netease') return track.audioUrl;
  if (!track.urlExpireAt || track.urlExpireAt <= now + EXPIRY_SAFETY_WINDOW_MS) return undefined;
  return track.audioUrl;
}

/**
 * 通过 Go 服务端解析临时音源 URL。
 *
 * 队列只保留元数据；网易云 URL 在真正播放前获取并缓存 30 分钟。上传音频仍由
 * Go 服务端解析绝对路径，避免 Worker 自行拼接数据目录。
 */
export class MusicSourceResolver {
  async resolve(track: Track): Promise<string> {
    const cached = usableCachedURL(track);
    if (cached) return cached;

    const endpoint = new URL('/api/v1/internal/music-bot/resolve', config.goServerUrl);
    endpoint.searchParams.set('source', track.source);
    endpoint.searchParams.set('trackId', track.trackId);

    const headers: Record<string, string> = { Accept: 'application/json' };
    if (config.goServerInternalToken) {
      headers.Authorization = `Bearer ${config.goServerInternalToken}`;
    }

    const response = await fetch(endpoint, {
      method: 'GET',
      headers,
      signal: AbortSignal.timeout(RESOLVE_TIMEOUT_MS),
    });

    if (!response.ok) {
      const body = (await response.text()).slice(0, 300);
      throw new Error(`音源解析失败（HTTP ${response.status}）：${body}`);
    }

    const payload = await response.json() as ResolveResponse;
    if (typeof payload.url !== 'string' || payload.url.length === 0) {
      throw new Error('音源解析失败：服务端未返回播放地址');
    }

    track.audioUrl = payload.url;
    if (track.source === 'netease') {
      track.urlExpireAt = typeof payload.expiresAt === 'number'
        ? payload.expiresAt
        : Date.now() + 30 * 60 * 1000;
    }
    log.debug({ trackId: track.trackId, source: track.source }, 'Resolved fresh audio URL');
    return payload.url;
  }
}

export const musicSourceResolver = new MusicSourceResolver();
