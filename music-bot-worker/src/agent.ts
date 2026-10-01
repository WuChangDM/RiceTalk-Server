import { defineAgent, type JobContext } from '@livekit/agents';
import { moduleLogger } from './logger.js';

const log = moduleLogger('agent');

/**
 * RidgeRiceTalk Music Bot Agent
 *
 * 这个 Agent 作为 LiveKit 参与者加入语音频道，负责：
 * - 接收 Go 服务端的播放指令（通过 HTTP API）
 * - 将音频转码为 Opus 格式并发布到 LiveKit
 * - 管理播放状态（playing/paused/currentTime）
 * - 通过 HTTP 回调通知 Go 服务端状态变化
 *
 * 阶段 1：仅实现 Agent 骨架，验证能连接到 LiveKit
 */
export const musicBotAgent = defineAgent({
  entry: async (ctx: JobContext) => {
    log.info({ roomId: ctx.room.name }, 'Agent connected to room');

    // 阶段 1：仅验证连接，不实际播放音频
    // 后续阶段会在这里实现音频发布逻辑

    // 保持 Agent 运行，直到被显式断开
    await new Promise<void>((resolve) => {
      ctx.room.on('disconnected', () => {
        log.info('Agent disconnected from room');
        resolve();
      });
    });
  },
});

export type { JobContext };
