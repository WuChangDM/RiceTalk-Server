import dotenv from 'dotenv';
import path from 'node:path';

dotenv.config();

export interface Config {
  /** HTTP 服务器端口 */
  port: number;
  /** Go 服务端回调 URL */
  goServerUrl: string;
  /** Go 服务端内部回调鉴权 token */
  goServerInternalToken: string;
  /** LiveKit 服务地址 */
  livekitUrl: string;
  /** LiveKit API Key */
  livekitApiKey: string;
  /** LiveKit API Secret */
  livekitApiSecret: string;
  /** 日志级别 */
  logLevel: string;
  /** 音频临时文件目录 */
  audioTmpDir: string;
  /** 有真人说话时是否自动降低音乐音量 */
  voiceDuckingEnabled: boolean;
  /** 闪避后的音乐音量百分比（0-100） */
  voiceDuckingVolumePercent: number;
}

function required(key: string, fallback?: string): string {
  const value = process.env[key] ?? fallback;
  if (!value) {
    throw new Error(`Missing required env var: ${key}`);
  }
  return value;
}

function loadConfig(): Config {
  const duckingVolume = Number.parseInt(process.env.VOICE_DUCKING_VOLUME_PERCENT ?? '30', 10);
  return {
    port: parseInt(process.env.PORT ?? '5012', 10),
    goServerUrl: required('GO_SERVER_URL', 'http://127.0.0.1:5000'),
    goServerInternalToken: process.env.GO_SERVER_INTERNAL_TOKEN ?? '',
    livekitUrl: required('LIVEKIT_URL', 'ws://127.0.0.1:7880'),
    livekitApiKey: required('LIVEKIT_API_KEY', 'devkey'),
    livekitApiSecret: required('LIVEKIT_API_SECRET', 'devsecret'),
    logLevel: process.env.LOG_LEVEL ?? 'info',
    audioTmpDir: process.env.AUDIO_TMP_DIR ?? path.join(process.cwd(), 'tmp', 'audio'),
    voiceDuckingEnabled: !['0', 'false', 'off'].includes(
      (process.env.VOICE_DUCKING_ENABLED ?? 'true').toLowerCase(),
    ),
    voiceDuckingVolumePercent: Number.isFinite(duckingVolume)
      ? Math.max(0, Math.min(100, duckingVolume))
      : 30,
  };
}

export const config = loadConfig();
