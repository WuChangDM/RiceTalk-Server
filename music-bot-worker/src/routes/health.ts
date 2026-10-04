import { Router } from 'express';
import { moduleLogger } from '../logger.js';

const log = moduleLogger('health');
export const healthRouter = Router();

healthRouter.get('/health', (_req, res) => {
  res.json({
    status: 'ok',
    service: 'ridgericetalk-music-bot-worker',
    version: '0.1.0',
    timestamp: new Date().toISOString(),
    uptime: process.uptime(),
  });
});

log.info('Health check route registered at GET /internal/health');
