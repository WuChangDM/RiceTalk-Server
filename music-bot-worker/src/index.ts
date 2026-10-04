import express from 'express';
// 阶段 3：@livekit/agents 的 AudioByteStream 在构造时调用 log()，
// 必须先调用 initializeLogger() 否则抛出 "logger not initialized" 错误。
// 这里在最早时机初始化（pretty=false 生产模式，level=info）。
import { initializeLogger } from '@livekit/agents';
initializeLogger({ pretty: false, level: 'info' });

import { config } from './config.js';
import { logger, moduleLogger } from './logger.js';
import { healthRouter } from './routes/health.js';
import { botRouter } from './routes/botRoutes.js';
import { botManager } from './managers/botManager.js';

const log = moduleLogger('server');

async function main() {
  log.info({ config: { ...config, livekitApiSecret: '***' } }, 'Starting music bot worker');

  // 创建 HTTP 服务器（仅本机访问）
  const app = express();
  app.use(express.json({ limit: '10mb' }));

  // 健康检查
  app.use('/internal', healthRouter);

  // Bot 控制接口（阶段 2：play/stop/status 已实现）
  app.use('/internal', botRouter);

  // 404 处理
  app.use((_req, res) => {
    res.status(404).json({ error: 'not_found' });
  });

  // 错误处理
  app.use((err: Error, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
    log.error({ err }, 'Unhandled error');
    res.status(500).json({ error: 'internal_error', message: err.message });
  });

  // 启动 HTTP 服务器
  app.listen(config.port, '127.0.0.1', () => {
    log.info({ port: config.port }, 'HTTP server listening on 127.0.0.1');
    logger.info('=== RidgeRiceTalk Music Bot Worker (Stage 2: Play/Stop/Status) ===');
    logger.info('Health check: GET http://127.0.0.1:%d/internal/health', config.port);
    logger.info('Bot routes:   POST http://127.0.0.1:%d/internal/bots/:botId/<action>', config.port);
    logger.info('LiveKit URL:  %s', config.livekitUrl);
    logger.info('Go callback:  %s', config.goServerUrl);
    logger.info('===================================================================');
  });

  // 优雅退出：停止所有 Bot 会话再退出
  const shutdown = async (signal: string) => {
    log.info({ signal }, 'Shutting down gracefully...');
    try {
      // 停止所有活跃的 Bot 会话
      const botIds = botManager.listBots();
      log.info({ count: botIds.length }, 'Stopping all bot sessions');
      await Promise.allSettled(botIds.map((id) => botManager.stop(id, true)));
    } catch (err) {
      log.error({ err }, 'Error during shutdown');
    }
    process.exit(0);
  };
  process.on('SIGTERM', () => void shutdown('SIGTERM'));
  process.on('SIGINT', () => void shutdown('SIGINT'));

  // 未捕获异常
  process.on('uncaughtException', (err) => {
    log.error({ err }, 'Uncaught exception');
    process.exit(1);
  });
  process.on('unhandledRejection', (reason) => {
    log.error({ reason }, 'Unhandled rejection');
  });
}

main().catch((err) => {
  logger.error({ err }, 'Failed to start music bot worker');
  process.exit(1);
});
