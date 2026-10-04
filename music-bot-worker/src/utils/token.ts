// RidgeRiceTalk Music Bot Worker - LiveKit 令牌生成工具
// 与 Go 端 player_cgo.go Connect() 的令牌权限保持一致：
// - identity: bot-music-<botId>
// - name: 音乐机器人
// - 权限：canPublish=true, canSubscribe=false, hidden=false
//
// hidden=false 的原因（与 Go 端一致）：
// 隐藏参与者无法收到 ParticipantJoined 通知，LiveKit SDK 会以
// "Tried to add a track for a participant, that's not present" 拒绝其发布的 track。
// Bot 通过前端的 channelParticipants 过滤，而不是 LiveKit 参与者列表。

import { AccessToken } from 'livekit-server-sdk';
import { config } from '../config.js';
import { moduleLogger } from '../logger.js';

const log = moduleLogger('token');

/**
 * 生成 Bot 加入 LiveKit 房间的 JWT 令牌
 *
 * @param roomName 房间名（如 "rrt-room-<channelId>"）
 * @param botId Bot ID（用于构造 identity）
 * @returns JWT 令牌字符串
 */
export async function generateBotToken(roomName: string, botId: string): Promise<string> {
  const at = new AccessToken(config.livekitApiKey, config.livekitApiSecret, {
    identity: `bot-music-${botId}`,
    name: '音乐机器人',
    // TTL 1 小时：Bot 单次会话足够长，过期后会触发重连逻辑（阶段 4 实现）
    ttl: 3600,
  });

  at.addGrant({
    roomJoin: true,
    room: roomName,
    canPublish: true,
    canSubscribe: false,
    // hidden 必须为 false，否则 SDK 会拒绝发布 track（见文件头注释）
    hidden: false,
  });

  const token = await at.toJwt();
  log.debug({ roomName, botId, tokenLen: token.length }, 'Generated bot token');
  return token;
}
