import { Router } from 'express';
import { config } from '../config.js';
import { moduleLogger } from '../logger.js';
import { botManager } from '../managers/botManager.js';
import type {
  PlayRequest,
  StopRequest,
  SeekRequest,
  SetVolumeRequest,
  SkipRequest,
  SetPlayModeRequest,
  UpdateQueueRequest,
} from '../types/botSession.js';

const log = moduleLogger('bot-routes');
export const botRouter = Router();

/**
 * 阶段 3：实现 play/stop/status/pause/resume/seek/volume
 * 其余接口（skip/previous/play-mode/disconnect）
 * 在后续阶段实现。
 *
 * 所有接口路径：/internal/bots/:botId/<action>
 * Go 服务端通过 HTTP 调用这些接口控制 Bot。
 */

/** 鉴权中间件：校验 Authorization Bearer token（与 Go 端约定一致） */
botRouter.use((req, res, next) => {
  // 健康检查不需要鉴权（health 路由已单独挂载在 /internal/health）
  // 这里仅校验 /internal/bots/* 路径
  if (!req.path.startsWith('/bots/')) {
    return next();
  }
  const expected = config.goServerInternalToken;
  const provided = req.get('Authorization')?.replace(/^Bearer\s+/i, '') ?? '';
  if (expected && provided === expected) {
    return next();
  }
  const remoteAddress = req.socket.remoteAddress ?? '';
  const isLoopback = remoteAddress === '127.0.0.1'
    || remoteAddress === '::1'
    || remoteAddress === '::ffff:127.0.0.1';
  if (!expected && isLoopback) {
    return next();
  }
  return res.status(401).json({ error: 'unauthorized', message: 'Invalid worker token' });
});

/** POST /internal/bots/:botId/play - 开始播放指定曲目 */
botRouter.post('/bots/:botId/play', async (req, res) => {
  const { botId } = req.params;
  const body = req.body as Partial<PlayRequest>;

  // 参数校验
  if (!body || !body.track || !body.roomName || !body.channelId) {
    log.warn({ botId, body }, 'play: missing required fields');
    return res.status(400).json({
      error: 'invalid_request',
      message: 'Required fields: track, roomName, channelId',
    });
  }

  log.info(
    { botId, trackId: body.track.trackId, title: body.track.title, source: body.track.source },
    'POST /play'
  );

  try {
    await botManager.play(botId, body as PlayRequest);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'play failed');
    return res.status(500).json({ error: 'play_failed', message });
  }
});

/** POST /internal/bots/:botId/stop - 停止播放 */
botRouter.post('/bots/:botId/stop', (req, res) => {
  const { botId } = req.params;
  const body = (req.body || {}) as Partial<StopRequest>;
  const disconnect = body.disconnect === true;

  log.info({ botId, disconnect }, 'POST /stop');

  // stop 是异步的，但路由不等待（让客户端立即收到响应）
  // playLoop 的退出最多 2 秒（见 stopPlayback）
  botManager
    .stop(botId, disconnect)
    .then(() => log.info({ botId }, 'stop completed'))
    .catch((err) => log.error({ botId, err }, 'stop failed'));

  return res.json({ status: 'ok' });
});

/** GET /internal/bots/:botId/status - 查询播放状态 */
botRouter.get('/bots/:botId/status', (req, res) => {
  const { botId } = req.params;
  const status = botManager.getStatus(botId);
  log.debug({ botId, playing: status.playing }, 'GET /status');
  return res.json(status);
});

// ===== 阶段 3：Pause / Resume / Seek / Volume =====

/**
 * POST /internal/bots/:botId/pause - 暂停播放
 *
 * 响应格式（参考 Jellyfin SyncPlay CurrentSession 回送模式）：
 * HTTP 200 + body: { noop: boolean, reason: string, currentState: StatusResponse | null }
 *
 * - noop=false：成功暂停，Go 端应广播状态
 * - noop=true：no-op（重复暂停/未在播放/会话不存在），Go 端不应广播
 *   - reason: 'session_not_found' | 'not_playing' | 'already_paused' | 'paused'
 *   - currentState：当前状态快照（session_not_found 时为 null），Go 端可据此校正本地缓存
 *
 * 注意：不使用 204/304/202 状态码（详见修复方案 music-bot-fix-research.md §2.5）：
 * - 204 无 body 无法携带 reason
 * - 304 是 GET 缓存协商语义，POST 不应用
 * - 202 异步语义与当前同步调用不匹配
 * - 409 不是冲突，是「幂等成功」
 */
botRouter.post('/bots/:botId/pause', async (req, res) => {
  const { botId } = req.params;
  log.info({ botId }, 'POST /pause');
  try {
    const result = await botManager.pause(botId);
    log.info({ botId, noop: result.noop, reason: result.reason }, 'POST /pause result');
    return res.json(result);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'pause failed');
    return res.status(500).json({ error: 'pause_failed', message });
  }
});

/** POST /internal/bots/:botId/resume - 恢复播放 */
botRouter.post('/bots/:botId/resume', async (req, res) => {
  const { botId } = req.params;
  log.info({ botId }, 'POST /resume');
  try {
    await botManager.resume(botId);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'resume failed');
    return res.status(500).json({ error: 'resume_failed', message });
  }
});

/** POST /internal/bots/:botId/seek - 跳转到指定位置（秒） */
botRouter.post('/bots/:botId/seek', async (req, res) => {
  const { botId } = req.params;
  const body = (req.body || {}) as Partial<SeekRequest>;

  if (typeof body.position !== 'number' || body.position < 0) {
    log.warn({ botId, body }, 'seek: invalid position');
    return res.status(400).json({
      error: 'invalid_request',
      message: 'Field "position" (number, >=0) is required',
    });
  }

  log.info({ botId, position: body.position }, 'POST /seek');
  try {
    await botManager.seek(botId, body.position);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'seek failed');
    return res.status(500).json({ error: 'seek_failed', message });
  }
});

/** POST /internal/bots/:botId/volume - 设置音量（0-100） */
botRouter.post('/bots/:botId/volume', async (req, res) => {
  const { botId } = req.params;
  const body = (req.body || {}) as Partial<SetVolumeRequest>;

  if (typeof body.volume !== 'number' || body.volume < 0 || body.volume > 100) {
    log.warn({ botId, body }, 'volume: invalid volume');
    return res.status(400).json({
      error: 'invalid_request',
      message: 'Field "volume" (number, 0-100) is required',
    });
  }

  log.info({ botId, volume: body.volume }, 'POST /volume');
  try {
    await botManager.setVolume(botId, body.volume);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'volume failed');
    return res.status(500).json({ error: 'volume_failed', message });
  }
});

// ===== 阶段 4：Skip / Previous / PlayMode / Queue / Disconnect =====

/** POST /internal/bots/:botId/skip - 下一首 */
botRouter.post('/bots/:botId/skip', async (req, res) => {
  const { botId } = req.params;
  const body = (req.body || {}) as Partial<SkipRequest>;
  const isUserSkip = body.isUserSkip !== false; // 默认 true

  log.info({ botId, isUserSkip }, 'POST /skip');
  try {
    await botManager.playNext(botId, isUserSkip);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'skip failed');
    return res.status(500).json({ error: 'skip_failed', message });
  }
});

/** POST /internal/bots/:botId/previous - 上一首 */
botRouter.post('/bots/:botId/previous', async (req, res) => {
  const { botId } = req.params;
  log.info({ botId }, 'POST /previous');
  try {
    await botManager.previous(botId);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'previous failed');
    return res.status(500).json({ error: 'previous_failed', message });
  }
});

/** POST /internal/bots/:botId/play-mode - 设置播放模式 */
botRouter.post('/bots/:botId/play-mode', async (req, res) => {
  const { botId } = req.params;
  const body = (req.body || {}) as Partial<SetPlayModeRequest>;

  const validModes = ['order', 'repeat-all', 'repeat-one', 'random'];
  if (!body.mode || !validModes.includes(body.mode)) {
    log.warn({ botId, body }, 'play-mode: invalid mode');
    return res.status(400).json({
      error: 'invalid_request',
      message: `Field "mode" must be one of: ${validModes.join(', ')}`,
    });
  }

  log.info({ botId, mode: body.mode, hasChannelId: !!body.channelId, hasRoomName: !!body.roomName }, 'POST /play-mode');
  try {
    await botManager.setPlayMode(botId, body.mode, body.roomName, body.channelId);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'play-mode failed');
    return res.status(500).json({ error: 'play_mode_failed', message });
  }
});

/** POST /internal/bots/:botId/queue - 同步队列（Go → Worker） */
botRouter.post('/bots/:botId/queue', async (req, res) => {
  const { botId } = req.params;
  const body = (req.body || {}) as Partial<UpdateQueueRequest>;

  if (!body.queue || !Array.isArray(body.queue)) {
    log.warn({ botId, body }, 'queue: invalid queue');
    return res.status(400).json({
      error: 'invalid_request',
      message: 'Field "queue" (Track[]) is required',
    });
  }

  log.info({ botId, queueLength: body.queue.length, hasChannelId: !!body.channelId, hasRoomName: !!body.roomName }, 'POST /queue');
  try {
    await botManager.updateQueue(botId, body.queue, body.roomName, body.channelId);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'queue sync failed');
    return res.status(500).json({ error: 'queue_sync_failed', message });
  }
});

/** POST /internal/bots/:botId/disconnect - 断开 LiveKit 连接 */
botRouter.post('/bots/:botId/disconnect', async (req, res) => {
  const { botId } = req.params;
  log.info({ botId }, 'POST /disconnect');
  try {
    await botManager.disconnect(botId);
    return res.json({ status: 'ok' });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error({ botId, err: message }, 'disconnect failed');
    return res.status(500).json({ error: 'disconnect_failed', message });
  }
});

log.info('Bot routes registered (play/stop/status/pause/resume/seek/volume/skip/previous/play-mode/queue/disconnect)');
