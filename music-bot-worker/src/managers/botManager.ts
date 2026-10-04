// RidgeRiceTalk Music Bot Worker - BotManager
// 阶段 3：实现核心播放能力（Play/Stop/Status/Pause/Resume/Seek/Volume）
// 阶段 4：实现队列与四种播放模式（order/repeat-all/repeat-one/random）
//
// 职责：
// - 管理多个 Bot 会话（每个 Bot 对应一个语音频道）
// - 连接 LiveKit 房间并发布音频 track
// - 使用 FFmpeg 读取音频文件并推流到 LiveKit
// - 维护播放状态（playing/paused/currentTime/currentTrack/volume/playMode）
// - 支持 seek（通过 -ss 参数重启 FFmpeg）
// - 支持暂停/恢复（playLoop 阻塞在 Promise 上，避免丢失音频帧）
// - 支持运行时音量调整（按帧缩放 Int16 采样）
// - 阶段 4：自主管理队列、播放历史、shuffle 顺序、空闲断连
//
// 不在本阶段实现的能力（后续阶段）：
// - HTTP 回调通知 Go 服务端（阶段 5）
// - TTS 与音量 ducking（阶段 6）

import { Mutex } from '@livekit/mutex';
import {
  Room,
  RoomEvent,
  AudioSource,
  LocalAudioTrack,
  TrackSource,
  AudioFrame,
} from '@livekit/rtc-node';
import { TrackPublishOptions } from '@livekit/rtc-ffi-bindings';
import type { LocalTrackPublication } from '@livekit/rtc-node';
import { config } from '../config.js';
import { moduleLogger } from '../logger.js';
import { generateBotToken } from '../utils/token.js';
import {
  audioFramesFromFileWithSeek,
  shouldEndOnStall,
} from '../utils/audioDecode.js';
import {
  DuckingGainEnvelope,
  VoiceDuckingController,
} from '../audio/voiceDucking.js';
import { musicSourceResolver } from '../providers/musicSourceResolver.js';
import type { BotSession, PlayRequest, StatusResponse, Track, PlayMode, PauseResult } from '../types/botSession.js';
import { EMPTY_STATUS } from '../types/botSession.js';

const log = moduleLogger('bot-manager');

/** 音频参数（与 Go 端 player_cgo.go 保持一致：48kHz 单声道 Opus） */
const AUDIO_SAMPLE_RATE = 48000;
const AUDIO_NUM_CHANNELS = 1;

/** 队列空 1 分钟后断开 LiveKit 连接（与 Go 端 player_cgo.go StartIdleTimer 一致） */
const IDLE_DISCONNECT_TIMEOUT_MS = 60 * 1000;

/** 播放历史上限（与 Go 端 player_cgo.go 一致） */
const MAX_PLAY_HISTORY = 50;

/** 失速轮询：临近结尾 5 秒、其他位置 60 秒仍无数据时终止死流。 */
const STALL_POLL_INTERVAL_MS = 100;
const MAX_NEAR_END_EMPTY_ATTEMPTS = 5_000 / STALL_POLL_INTERVAL_MS;
const MAX_STALL_EMPTY_ATTEMPTS = 60_000 / STALL_POLL_INTERVAL_MS;
const MAX_CONSECUTIVE_FAILURES = 3;
const HEALTHY_FRAME_RESET = 50;

function isManagedBotIdentity(identity: string): boolean {
  return identity.startsWith('bot-music-') || identity.startsWith('bot-tts-');
}

export class BotManager {
  /** botId → 会话 */
  private sessions = new Map<string, BotSession>();
  /** 串行化每个 bot 的 play/stop 操作，防止并发互相 kill FFmpeg */
  private botMutexes = new Map<string, Mutex>();

  /** 获取或创建指定 bot 的互斥锁 */
  private getMutex(botId: string): Mutex {
    let m = this.botMutexes.get(botId);
    if (!m) {
      m = new Mutex();
      this.botMutexes.set(botId, m);
    }
    return m;
  }

  /** 获取会话（不存在返回 undefined） */
  private getSession(botId: string): BotSession | undefined {
    return this.sessions.get(botId);
  }

  /**
   * 获取或创建会话（不连接 LiveKit）
   *
   * 用于 setPlayMode / updateQueue 等接口在 session 不存在时创建轻量 session，
   * 确保播放模式和队列状态能被持久化，后续 play 时无需重新设置。
   *
   * 若 session 已存在，直接返回（不会覆盖已有的 roomName/channelId）。
   * 若提供的 roomName/channelId 为空，也返回 undefined（无法创建）。
   */
  private getOrCreateSession(
    botId: string,
    roomName?: string,
    channelId?: string,
  ): BotSession | undefined {
    const existing = this.sessions.get(botId);
    if (existing) {
      return existing;
    }
    if (!roomName || !channelId) {
      return undefined;
    }
    return this.createSession(botId, roomName, channelId);
  }

  /** 创建新会话（不连接 LiveKit） */
  private createSession(botId: string, roomName: string, channelId: string): BotSession {
    const duckingGain = new DuckingGainEnvelope();
    const voiceDucking = new VoiceDuckingController(duckingGain, {
      enabled: config.voiceDuckingEnabled,
      volumePercent: config.voiceDuckingVolumePercent,
    });
    const session: BotSession = {
      botId,
      channelId,
      roomName,
      playing: false,
      paused: false,
      currentTime: 0,
      volume: 80,
      playMode: 'order',
      playLoopRunning: false,
      playbackSessionId: 0,
      consecutiveFailures: 0,
      healthyFrames: 0,
      lastActivity: Date.now(),
      duckingGain,
      voiceDucking,
      pauseBlocking: false,
      seekOffset: 0,
      // 阶段 4 新增字段
      queue: [],
      playHistory: [],
      historyIndex: -1,
      shuffleOrder: [],
      shuffleIndex: 0,
    };
    this.sessions.set(botId, session);
    log.info({ botId, roomName, channelId }, 'Created new bot session');
    return session;
  }

  /**
   * 连接 LiveKit 房间并发布音频 track
   * 幂等：若已连接则直接返回
   */
  private async connectRoom(session: BotSession): Promise<void> {
    if (session.room && session.track && session.publication) {
      return; // 已连接
    }

    log.info({ botId: session.botId, roomName: session.roomName }, 'Connecting to LiveKit room');
    const token = await generateBotToken(session.roomName, session.botId);
    const room = new Room();
    await room.connect(config.livekitUrl, token);
    log.info({ botId: session.botId }, 'Connected to LiveKit room');

    const activeSpeakersListener: NonNullable<BotSession['activeSpeakersListener']> = (speakers) => {
      for (const participant of speakers) {
        if (!isManagedBotIdentity(participant.identity)) {
          session.voiceDucking.handleVoiceActivity(participant.identity);
        }
      }
    };
    const participantDisconnectedListener: NonNullable<BotSession['participantDisconnectedListener']> = (participant) => {
      session.voiceDucking.removeSpeaker(participant.identity);
    };
    room.on(RoomEvent.ActiveSpeakersChanged, activeSpeakersListener);
    room.on(RoomEvent.ParticipantDisconnected, participantDisconnectedListener);
    session.activeSpeakersListener = activeSpeakersListener;
    session.participantDisconnectedListener = participantDisconnectedListener;

    // 创建音频源与本地 track
    const audioSource = new AudioSource(AUDIO_SAMPLE_RATE, AUDIO_NUM_CHANNELS);
    const track = LocalAudioTrack.createAudioTrack('bot-music', audioSource);

    // 发布 track（source=MICROPHONE 与 Go 端一致）
    // localParticipant 在 connect 成功后一定存在，用非空断言
    const publishOpts = new TrackPublishOptions({
      source: TrackSource.SOURCE_MICROPHONE,
    });
    const publication = await room.localParticipant!.publishTrack(track, publishOpts);
    log.info({ botId: session.botId, trackSid: publication.sid }, 'Published audio track');

    session.room = room;
    session.audioSource = audioSource;
    session.track = track;
    session.publication = publication;
  }

  /** 关闭 LiveKit 连接并释放资源 */
  private async disconnectRoom(session: BotSession): Promise<void> {
    session.voiceDucking.reset(true);
    if (session.room && session.activeSpeakersListener) {
      session.room.off(RoomEvent.ActiveSpeakersChanged, session.activeSpeakersListener);
      session.activeSpeakersListener = undefined;
    }
    if (session.room && session.participantDisconnectedListener) {
      session.room.off(RoomEvent.ParticipantDisconnected, session.participantDisconnectedListener);
      session.participantDisconnectedListener = undefined;
    }
    if (session.audioSource) {
      try {
        session.audioSource.clearQueue();
        await session.audioSource.close();
      } catch (err) {
        log.warn({ botId: session.botId, err }, 'Failed to close audio source');
      }
      session.audioSource = undefined;
    }
    if (session.track) {
      try {
        await session.track.close();
      } catch (err) {
        log.warn({ botId: session.botId, err }, 'Failed to close audio track');
      }
      session.track = undefined;
    }
    if (session.publication && session.room) {
      try {
        const sid = session.publication.sid;
        if (sid) {
          await session.room.localParticipant!.unpublishTrack(sid);
        }
      } catch (err) {
        log.warn({ botId: session.botId, err }, 'Failed to unpublish track');
      }
      session.publication = undefined;
    }
    if (session.room) {
      try {
        await session.room.disconnect();
      } catch (err) {
        log.warn({ botId: session.botId, err }, 'Failed to disconnect room');
      }
      session.room = undefined;
      log.info({ botId: session.botId }, 'Disconnected from LiveKit room');
    }
  }

  /**
   * 开始播放指定曲目
   * 流程：
   * 1. 获取/创建会话
   * 2. 停止当前播放（若有）
   * 3. 连接 LiveKit 房间
   * 4. 启动 playLoop 读取音频文件并推流
   *
   * 注意：此方法立即返回（不等待播放完成），playLoop 在后台运行。
   * 调用方可通过 getStatus() 查询播放进度。
   */
  async play(botId: string, req: PlayRequest): Promise<void> {
    if (!req.audioUrl) {
      req.audioUrl = await musicSourceResolver.resolve(req.track);
    } else {
      req.track.audioUrl = req.audioUrl;
      if (req.track.source === 'netease' && !req.track.urlExpireAt) {
        req.track.urlExpireAt = Date.now() + 30 * 60 * 1000;
      }
    }

    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      let session = this.getSession(botId);
      if (!session) {
        session = this.createSession(botId, req.roomName, req.channelId);
      } else {
        // 房间名/频道变更时更新（极少发生，但需处理）
        if (session.roomName !== req.roomName || session.channelId !== req.channelId) {
          log.warn(
            { botId, oldRoom: session.roomName, newRoom: req.roomName },
            'Room/channel changed during play, disconnecting old session'
          );
          await this.disconnectRoom(session);
          session.roomName = req.roomName;
          session.channelId = req.channelId;
        }
      }

      // 应用请求中的音量与播放模式
      if (typeof req.volume === 'number') {
        session.volume = Math.max(0, Math.min(100, req.volume));
      }
      if (req.playMode) {
        session.playMode = req.playMode;
      }

      // 停止当前播放（不立即断开 LiveKit，复用连接）
      await this.stopPlayback(session);

      // 立即更新 currentTrack，让 status 查询能返回新曲目
      // （Go 端 Play() 也是先更新 currentTrack 再转码，保证 UI 即时切换）
      // 阶段 4：在更新 currentTrack 前记录播放历史（若存在旧 currentTrack）
      this.recordPlayHistory(session);
      session.currentTrack = req.track;
      session.currentAudioUrl = req.audioUrl;
      session.playing = true;
      session.paused = false;
      session.currentTime = 0;
      session.lastActivity = Date.now();
      // 取消空闲定时器（有曲目在播放，不需要断连）
      this.stopIdleTimer(session);

      // 连接 LiveKit（若未连接）
      await this.connectRoom(session);

      // 启动 playLoop（后台运行）
      const playbackSessionId = ++session.playbackSessionId;
      session.playLoopRunning = true;
      this.startPlayLoop(session, playbackSessionId).catch((err) => {
        log.error({ botId, err }, 'playLoop crashed');
        if (session.playbackSessionId === playbackSessionId) {
          session.playLoopRunning = false;
          session.playing = false;
        }
        // 阶段 5 在此处调用 Go 回调通知错误
      });

      log.info({ botId, trackId: req.track.trackId, title: req.track.title }, 'Play started');
    } finally {
      release();
    }
  }

  /**
   * playLoop：读取音频文件并推流到 LiveKit
   *
   * 阶段 3 增强：
   * - 支持从 seekOffset 位置开始解码（通过 FFmpeg -ss 参数）
   * - 支持暂停：检测 paused=true 时阻塞在 Promise 上，resume 时唤醒
   * - 支持运行时音量调整：按帧缩放 Int16 采样
   *
   * 使用 audioFramesFromFileWithSeek（基于 FFmpeg）将音频文件解码为 AudioFrame 流，
   * 然后通过 AudioSource.captureFrame 推送。captureFrame 会在内部队列满时阻塞，
   * 自然实现流量控制（不需要手动 sleep）。
   *
   * 播放完成后：
   * - 标记 playing=false（保留 currentTrack，让前端能显示最后播放的曲目）
   * - 阶段 4 会在此调用 playNext（根据 playMode 决定下一首或停止）
   */
  private async startPlayLoop(session: BotSession, playbackSessionId: number): Promise<void> {
    const { botId, currentAudioUrl, currentTrack, audioSource } = session;
    if (!currentAudioUrl || !currentTrack || !audioSource) {
      log.error({ botId }, 'playLoop started without audioUrl/track/audioSource');
      return;
    }

    const abortController = new AbortController();
    session.abortController = abortController;

    // 读取并消费 seekOffset（playLoop 启动时使用）
    const seekOffset = Math.max(0, session.seekOffset);
    session.seekOffset = 0;

    // 起始时间累加 seekOffset（用于计算 currentTime）
    let totalSamples = Math.floor(seekOffset * AUDIO_SAMPLE_RATE);
    let emptyAttempts = 0;
    let terminalError: Error | undefined;

    log.info({ botId, audioUrl: currentAudioUrl, seekOffset }, 'playLoop: starting FFmpeg decode');

    try {
      const stream = audioFramesFromFileWithSeek(currentAudioUrl, {
        sampleRate: AUDIO_SAMPLE_RATE,
        numChannels: AUDIO_NUM_CHANNELS,
        seekSeconds: seekOffset,
        abortSignal: abortController.signal,
      });

      const reader = stream.getReader();
      let pendingRead: ReturnType<typeof reader.read> | undefined;

      while (true) {
        if (session.playbackSessionId !== playbackSessionId) {
          log.debug({ botId, playbackSessionId }, 'playLoop: stale session discarded');
          break;
        }
        if (!session.playing) {
          log.info({ botId }, 'playLoop: stopped by session.playing=false');
          break;
        }
        if (abortController.signal.aborted) {
          log.info({ botId }, 'playLoop: aborted');
          break;
        }

        // 阶段 3：暂停检测（在 reader.read() 之前，避免读取下一帧）
        if (session.paused) {
          session.pauseBlocking = true;
          log.info({ botId, currentTime: session.currentTime }, 'playLoop: paused, blocking');
          await new Promise<void>((resolve) => {
            session.pauseResolver = resolve;
          });
          session.pauseResolver = undefined;
          session.pauseBlocking = false;
          log.info({ botId }, 'playLoop: resumed');
          // 恢复后再次检查状态（可能被 stop/seek 中断）
          if (!session.playing || abortController.signal.aborted) {
            log.info({ botId }, 'playLoop: stopped after resume');
            break;
          }
        }

        pendingRead ??= reader.read();
        const readResult = await Promise.race([
          pendingRead.then((value) => ({ kind: 'frame' as const, value })),
          new Promise<{ kind: 'timeout' }>((resolve) => {
            const timer = setTimeout(
              () => resolve({ kind: 'timeout' }),
              STALL_POLL_INTERVAL_MS,
            );
            timer.unref?.();
          }),
        ]);

        if (readResult.kind === 'timeout') {
          emptyAttempts++;
          const duration = currentTrack.duration || 0;
          const isNearEnd = duration > 0 && duration - session.currentTime <= 5;
          if (shouldEndOnStall(
            emptyAttempts,
            isNearEnd,
            MAX_NEAR_END_EMPTY_ATTEMPTS,
            MAX_STALL_EMPTY_ATTEMPTS,
          )) {
            await reader.cancel('audio stream stalled').catch(() => undefined);
            abortController.abort();
            if (isNearEnd) {
              log.info(
                { botId, currentTime: session.currentTime, duration },
                'playLoop: near-end stall treated as completed playback',
              );
              break;
            }
            throw new Error(
              '音频流超过 60 秒没有输出',
            );
          }
          continue;
        }

        pendingRead = undefined;
        emptyAttempts = 0;
        const { done, value: frame } = readResult.value;
        if (done) {
          log.info({ botId }, 'playLoop: stream ended naturally');
          break;
        }
        if (!frame) continue;

        // 阶段 3：应用音量调整（in-place 缩放，避免创建新对象）
        applyVolumeInPlace(frame, session.volume, session.duckingGain.currentGain());

        // 推送帧到 AudioSource（流量控制：队列满时阻塞）
        if (session.playbackSessionId !== playbackSessionId) break;
        await audioSource.captureFrame(frame);

        // 累加播放时间
        totalSamples += frame.samplesPerChannel;
        session.currentTime = Math.floor(totalSamples / AUDIO_SAMPLE_RATE);
        session.lastActivity = Date.now();
        session.healthyFrames++;
        if (session.healthyFrames >= HEALTHY_FRAME_RESET) {
          session.consecutiveFailures = 0;
          session.healthyFrames = 0;
        }
      }

      // 等待 AudioSource 队列中的音频播放完毕
      if (session.playbackSessionId === playbackSessionId) {
        await audioSource.waitForPlayout();
        log.info({ botId, title: currentTrack.title }, 'playLoop: playout completed');
      }
    } catch (err) {
      if (abortController.signal.aborted) {
        if (session.playbackSessionId === playbackSessionId) {
          const message = err instanceof Error ? err.message : String(err);
          if (message.includes('失速') || message.includes('没有输出')) {
            terminalError = err instanceof Error ? err : new Error(message);
          } else {
            log.info({ botId }, 'playLoop: aborted during read');
          }
        }
      } else {
        log.error({ botId, err }, 'playLoop: error');
        terminalError = err instanceof Error ? err : new Error(String(err));
      }
    } finally {
      if (session.playbackSessionId === playbackSessionId) {
        session.abortController = undefined;
      }
    }

    // 旧播放会话退出时不得覆盖新会话状态。
    if (session.playbackSessionId !== playbackSessionId) return;

    if (terminalError) {
      session.consecutiveFailures++;
      session.healthyFrames = 0;
      log.warn(
        { botId, failures: session.consecutiveFailures, err: terminalError.message },
        'playLoop: terminal playback failure',
      );
    } else if (totalSamples > Math.floor(seekOffset * AUDIO_SAMPLE_RATE)) {
      // 很短的提示音可能不足 50 帧，但只要自然完成，也应视为健康播放。
      session.consecutiveFailures = 0;
      session.healthyFrames = 0;
    }

    // 自然播放结束的处理
    // 阶段 4：根据 playMode 决定下一首或停止
    //
    // 关键：区分"自然结束"和"被中断"：
    // - 自然结束（stream ended naturally）：session.playing 仍为 true，应触发 playNext
    // - 被中断（stopPlayback 设置 playing=false）：不应触发 playNext，因为调用方
    //   （play/seek/stop 等方法）会自己启动新 playLoop 或处理状态
    //
    // 之前的 bug：无论何种退出原因都触发 playNext，导致 play→stopPlayback→playLoop 退出
    // →setImmediate(playNext)→play→stopPlayback→新 playLoop 退出→又 playNext...
    // 形成死循环，每秒触发 5-10 次 playLoop，FFmpeg 频繁启停无法正常解码。
    const naturalEnd = session.playing;

    session.playLoopRunning = false;
    session.playing = false;
    session.paused = false;

    if (naturalEnd && session.consecutiveFailures < MAX_CONSECUTIVE_FAILURES) {
      log.info({ botId, title: currentTrack.title }, 'playLoop: finished naturally, triggering playNext');
      // 阶段 4：自然结束，isUserSkip=false（repeat-one 模式下会重复当前曲目）
      // 在下一个事件循环中执行，避免在 playLoop 持有 mutex 时重入
      setImmediate(() => {
        this.playNext(botId, false).catch((err) => {
          log.error({ botId, err }, 'playNext after natural end failed');
        });
      });
    } else if (terminalError && session.consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
      log.error(
        { botId, failures: session.consecutiveFailures },
        'FFmpeg failures limit reached; refusing automatic replay',
      );
      session.currentTrack = undefined;
      session.currentAudioUrl = undefined;
      session.currentTime = 0;
    } else {
      log.info({ botId, title: currentTrack.title }, 'playLoop: interrupted, not triggering playNext');
    }
    // TODO(阶段 5): HTTP 回调通知 Go 服务端播放结束
  }

  /**
   * 停止当前播放（不断开 LiveKit 连接）
   * 用于 Play() 内部切换曲目，或 Stop 接口调用
   *
   * 阶段 3：在停止前唤醒可能正在暂停的 playLoop，避免它一直阻塞。
   */
  private async stopPlayback(session: BotSession): Promise<void> {
    // 先递增代次，确保旧 reader/FFmpeg 回调即使稍后到达也只能 no-op。
    session.playbackSessionId++;
    // 阶段 3：唤醒可能正在暂停阻塞的 playLoop，让它能感知 abort 信号并退出
    if (session.pauseResolver) {
      try {
        session.pauseResolver();
      } catch {
        // resolver 调用失败不应阻塞 stop
      }
      session.pauseResolver = undefined;
    }
    session.pauseBlocking = false;

    // 取消正在运行的 playLoop
    if (session.abortController) {
      session.abortController.abort();
    }
    // 标记停止，让 playLoop 主动退出
    session.playing = false;
    session.playLoopRunning = false;

    // 等待 playLoop 退出（最多 2 秒）
    const deadline = Date.now() + 2000;
    while (session.abortController && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 50));
    }

    // 清空 AudioSource 队列
    if (session.audioSource) {
      try {
        session.audioSource.clearQueue();
      } catch (err) {
        log.warn({ botId: session.botId, err }, 'Failed to clear audio queue');
      }
    }

    // 阶段 4：不在此清除 currentTrack，让 playNext 能读取 currentTrack 决定下一首
    // 仅清除播放进度与暂停状态
    session.currentTime = 0;
    session.paused = false;
    session.seekOffset = 0;
  }

  /**
   * 停止播放（外部接口）
   * @param disconnect 是否同时断开 LiveKit 连接
   */
  async stop(botId: string, disconnect = false): Promise<void> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'stop: session not found (no-op)');
        return;
      }
      log.info({ botId, disconnect }, 'Stopping playback');
      await this.stopPlayback(session);
      // 外部 stop 清除 currentTrack（与内部 stopPlayback 区分）
      session.currentTrack = undefined;
      session.currentAudioUrl = undefined;

      if (disconnect) {
        this.stopIdleTimer(session);
        await this.disconnectRoom(session);
        this.sessions.delete(botId);
        this.botMutexes.delete(botId);
      }
    } finally {
      release();
    }
  }

  /**
   * 查询播放状态
   * 与 Go 端 BotPlayer.Status() 字段对齐
   *
   * 状态语义（参考 Navidrome play_tracker.go + Lavalink TrackEndEvent）：
   * - playing=true / paused=true：currentTrack 始终保留（业界共识，4 个开源项目一致）
   * - playing=false && paused=false：停止态/自然结束态
   *   - 若 session.currentTrack 存在：保留指针（让前端能显示最后播放的曲目）
   *   - 仅在显式 stop()/disconnect() 时才清空 currentTrack
   *
   * 注：本方法只读取 session 字段，不主动清空。
   * 清空动作由 stop()/disconnect() 负责（与 Lavalink `reason: stopped` 对齐）。
   */
  getStatus(botId: string): StatusResponse {
    const session = this.getSession(botId);
    if (!session) {
      return { ...EMPTY_STATUS };
    }

    // 关键修复：暂停态保留 currentTrack（参考 Jellyfin PausedGroupState / CyTube / Navidrome / Lavalink）
    // 旧实现 `if (session.currentTrack && (session.playing || session.paused))` 在 paused=true 时
    // 虽然也保留，但被 stopPlayback 后的 playing=false 误清空（业界共识是暂停保留 currentTrack）。
    // 新实现：只要 session.currentTrack 存在就返回，stop/disconnect 负责显式清空。
    const currentTrack: Track | null = session.currentTrack ?? null;

    // currentTime 在停止状态下归零（保留 currentTrack 但清进度，避免前端误以为还在播放）
    let currentTime = session.currentTime;
    if (!session.playing && !session.paused) {
      currentTime = 0;
    }

    return {
      playing: session.playing,
      paused: session.paused,
      currentTime,
      volume: session.volume,
      playMode: session.playMode,
      currentTrack,
    };
  }

  /** 获取所有会话的 botId（用于调试/健康检查） */
  listBots(): string[] {
    return Array.from(this.sessions.keys());
  }

  // ===== 阶段 3：Pause / Resume / Seek / SetVolume =====

  /**
   * 暂停播放
   * 仅设置 paused=true，playLoop 在下一次循环检测时阻塞。
   * 不中断 FFmpeg 进程，保留 AudioSource 队列中的音频帧。
   *
   * 返回值语义（参考 Jellyfin PausedGroupState prevState.Equals(Type) 模式）：
   * - noop=false：成功暂停，Go 端应广播状态
   * - noop=true：no-op，原因可能是：
   *   - session_not_found：会话不存在
   *   - not_playing：当前未在播放（已停止/已暂停后调用）
   *   - already_paused：重复调用暂停
   *   no-op 时仍回送 currentState 快照，Go 端可据此校正本地缓存（参考 Jellyfin
   *   "Client got lost, sending current state." 注释）
   *
   * Go 端处理：noop=true 时不调用 broadcastWorkerState 给所有 WS 客户端推送，
   * 避免前端收到错误的"暂停态 currentTrack=null"导致 UI 清空。
   */
  async pause(botId: string): Promise<PauseResult> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'pause: session not found (no-op)');
        return { noop: true, reason: 'session_not_found', currentState: null };
      }
      if (!session.playing) {
        log.warn({ botId }, 'pause: not playing, no-op');
        return {
          noop: true,
          reason: session.paused ? 'already_paused' : 'not_playing',
          currentState: this.getStatus(botId),
        };
      }
      if (session.paused) {
        log.debug({ botId }, 'pause: already paused (no-op)');
        return {
          noop: true,
          reason: 'already_paused',
          currentState: this.getStatus(botId),
        };
      }
      session.paused = true;
      session.lastActivity = Date.now();
      log.info({ botId, currentTime: session.currentTime }, 'Paused');
      return {
        noop: false,
        reason: 'paused',
        currentState: this.getStatus(botId),
      };
    } finally {
      release();
    }
  }

  /**
   * 恢复播放
   * 设置 paused=false 并唤醒 playLoop（调用 pauseResolver）。
   * 如果 playLoop 还未进入阻塞态（pauseBlocking=false），resume 也会先于 playLoop 完成，
   * playLoop 检测时 paused 已为 false，不会阻塞。
   */
  async resume(botId: string): Promise<void> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'resume: session not found (no-op)');
        return;
      }
      if (!session.paused) {
        log.warn({ botId }, 'resume: not paused, ignore');
        return;
      }
      session.paused = false;
      session.lastActivity = Date.now();
      // 唤醒 playLoop
      if (session.pauseResolver) {
        session.pauseResolver();
        session.pauseResolver = undefined;
      }
      log.info({ botId, currentTime: session.currentTime }, 'Resumed');
    } finally {
      release();
    }
  }

  /**
   * Seek 到指定位置
   *
   * 实现：通过 abortController 停止当前 playLoop，设置 seekOffset，
   * 然后重启 playLoop（FFmpeg 用 -ss 参数从指定位置开始解码）。
   *
   * 行为约定（与 QQ 音乐 / 网易云音乐一致）：
   * - 若当前处于暂停状态，seek 后自动恢复播放
   * - 若当前未在播放（playing=false），仅更新 seekOffset 不重启 playLoop
   *   （下次 play 会从该位置开始；但通常 seek 仅在播放/暂停态调用）
   */
  async seek(botId: string, position: number): Promise<void> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'seek: session not found (no-op)');
        return;
      }
      if (!session.currentTrack || !session.currentAudioUrl) {
        log.warn({ botId }, 'seek: no current track, ignore');
        return;
      }

      const pos = Math.max(0, Math.floor(position));
      log.info({ botId, position: pos, currentTime: session.currentTime, paused: session.paused }, 'Seek');

      // 唤醒暂停中的 playLoop（让它能感知 abort 并退出）
      if (session.pauseResolver) {
        session.pauseResolver();
        session.pauseResolver = undefined;
      }
      session.pauseBlocking = false;
      session.paused = false; // seek 后默认恢复播放

      // 取消当前 playLoop
      if (session.abortController) {
        session.playbackSessionId++;
        session.abortController.abort();
      }
      const playbackSessionId = session.playbackSessionId;
      const wasPlaying = session.playing;
      session.playing = false;

      // 等待 playLoop 退出（最多 2 秒）
      const deadline = Date.now() + 2000;
      while (session.abortController && Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 50));
      }

      // 清空 AudioSource 队列（避免旧音频继续播放）
      if (session.audioSource) {
        try {
          session.audioSource.clearQueue();
        } catch (err) {
          log.warn({ botId, err }, 'seek: failed to clear audio queue');
        }
      }

      // 设置 seekOffset，重启 playLoop
      session.seekOffset = pos;
      session.currentTime = pos;

      if (wasPlaying && session.audioSource) {
        session.playing = true;
        session.playLoopRunning = true;
        this.startPlayLoop(session, playbackSessionId).catch((err) => {
          log.error({ botId, err }, 'seek: playLoop crashed');
          if (session.playbackSessionId === playbackSessionId) {
            session.playLoopRunning = false;
            session.playing = false;
          }
        });
        log.info({ botId, position: pos }, 'Seek: playLoop restarted');
      } else {
        log.info({ botId, position: pos }, 'Seek: not playing, only updated seekOffset');
      }
    } finally {
      release();
    }
  }

  /**
   * 设置音量（0-100）
   * 仅更新 session.volume，playLoop 在下一帧自动应用新音量。
   * 不需要重启 playLoop。
   */
  async setVolume(botId: string, volume: number): Promise<void> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'setVolume: session not found (no-op)');
        return;
      }
      const oldVol = session.volume;
      session.volume = Math.max(0, Math.min(100, Math.floor(volume)));
      session.lastActivity = Date.now();
      log.info({ botId, oldVolume: oldVol, newVolume: session.volume }, 'Volume set');
    } finally {
      release();
    }
  }

  // ===== 阶段 4：队列与播放模式 =====

  /**
   * 同步队列（Go → Worker）
   * Go 在队列变更时（Enqueue/Remove/Clear/PlayNow 失败删除）调用此接口
   * 将最新队列推送到 Worker，保证 Worker 内部队列与 DB 一致。
   */
  async updateQueue(botId: string, queue: Track[], roomName?: string, channelId?: string): Promise<void> {
    // 标记是否需要在释放锁后自动播放
    let shouldAutoPlay = false;
    let autoPlayTrack: Track | undefined;
    let autoPlayAudioUrl: string | undefined;

    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      // session 不存在时创建轻量 session（不连接 LiveKit）
      // 这样队列能被 Worker 缓存，后续 playNext/skip 能读取到正确的队列
      let session = this.getSession(botId);
      if (!session) {
        session = this.getOrCreateSession(botId, roomName, channelId);
        if (!session) {
          log.warn({ botId, queueLen: queue.length }, 'updateQueue: session not found and no roomName/channelId provided (no-op)');
          return;
        }
        log.info({ botId, queueLen: queue.length }, 'updateQueue: created lightweight session for queue sync');
      }
      const oldLen = session.queue.length;
      const previousTracks = new Map(session.queue.map((track) => [track.id, track]));
      session.queue = queue.map((track) => {
        const previous = previousTracks.get(track.id);
        if (!track.audioUrl && previous?.audioUrl) {
          return {
            ...track,
            audioUrl: previous.audioUrl,
            urlExpireAt: previous.urlExpireAt,
          };
        }
        return { ...track };
      });
      // 队列变更后，shuffle 顺序需要重新生成（如果当前是 random 模式）
      if (session.playMode === 'random' && session.queue.length > 0) {
        this.regenerateShuffle(session);
      }
      log.info({ botId, oldLen, newLen: queue.length, playing: session.playing, hasCurrentTrack: !!session.currentTrack }, 'Queue updated');
      // 队列空时启动空闲定时器（若未在播放）
      if (session.queue.length === 0 && !session.playing) {
        this.startIdleTimer(session);
      }
      // 阶段 4：如果队列非空且当前未播放（无 currentTrack），自动播放第一首
      // 这替代了 Go 端 AddToQueue 中的自动播放逻辑（Worker 模式下已跳过）
      if (session.queue.length > 0 && !session.playing && !session.currentTrack) {
        shouldAutoPlay = true;
        // random 模式下从 shuffleOrder 取第一首，否则取 queue[0]
        if (session.playMode === 'random' && session.shuffleOrder.length > 0) {
          const firstId = session.shuffleOrder[0];
          autoPlayTrack = session.queue.find((t) => t.id === firstId) || session.queue[0];
          session.shuffleIndex = 0;
        } else {
          autoPlayTrack = session.queue[0];
        }
        autoPlayAudioUrl = await this.resolveAudioUrl(autoPlayTrack);
        log.info(
          { botId, trackTitle: autoPlayTrack.title, hasAudioUrl: !!autoPlayAudioUrl },
          'updateQueue: auto-playing first track'
        );
      }
    } finally {
      release();
    }

    // 在锁外触发播放（避免死锁：play 方法需要获取同一个互斥锁）
    if (shouldAutoPlay && autoPlayTrack) {
      try {
        await this.play(botId, {
          track: autoPlayTrack,
          audioUrl: autoPlayAudioUrl || '',
          roomName: this.getSession(botId)?.roomName || '',
          channelId: this.getSession(botId)?.channelId || '',
          volume: this.getSession(botId)?.volume ?? 80,
          playMode: this.getSession(botId)?.playMode || 'order',
        });
      } catch (err) {
        log.error({ botId, err }, 'updateQueue: auto-play failed');
      }
    }
  }

  /**
   * 下一首（用户主动点击或自然播放结束）
   *
   * isUserSkip 区分两种场景：
   * - true：用户点击"下一首"按钮，repeat-one 模式下也切换到下一首
   * - false：自然播放结束，repeat-one 模式下重复当前曲目
   *
   * 四种播放模式行为（对齐 QQ 音乐 / 网易云音乐 / Spotify / Apple Music）：
   * - order：到最后一首停止（不再循环回第一首）
   * - repeat-all：到最后一首循环回第一首
   * - repeat-one：自然结束重复当前曲目；用户 Skip 切换到下一首（同 repeat-all）
   * - random：按 shuffleOrder 顺序播放，一轮结束重新洗牌
   */
  async playNext(botId: string, isUserSkip: boolean): Promise<void> {
    // 标记是否需要在释放锁后触发播放
    let nextPlayReq: PlayRequest | undefined;

    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'playNext: session not found (no-op)');
        return;
      }

      const queue = session.queue;
      if (queue.length === 0) {
        // 队列空，停止播放并启动空闲定时器
        log.info({ botId, isUserSkip }, 'playNext: queue empty, stopping');
        await this.stopPlayback(session);
        session.currentTrack = undefined;
        session.currentAudioUrl = undefined;
        this.startIdleTimer(session);
        return;
      }

      const currentTrack = session.currentTrack;
      const playMode = session.playMode;
      let nextTrack: Track | undefined;

      switch (playMode) {
        case 'random':
          nextTrack = this.nextShuffleTrack(session);
          break;
        case 'repeat-one':
          if (!isUserSkip && currentTrack) {
            // 自然结束：重复当前曲目
            nextTrack = currentTrack;
          } else if (isUserSkip && currentTrack) {
            // 用户 Skip：切换到下一首（同 repeat-all 逻辑）
            nextTrack = this.findNextInQueue(session, currentTrack.id, true);
          }
          break;
        case 'repeat-all':
          nextTrack = this.findNextInQueue(session, currentTrack?.id, true);
          break;
        case 'order':
        default:
          // order 模式：到最后一首停止，不循环
          nextTrack = this.findNextInQueue(session, currentTrack?.id, false);
          break;
      }

      // fallback：若未找到下一首，根据模式决定行为
      if (!nextTrack) {
        if (playMode === 'order') {
          // order 模式到末尾，停止播放
          log.info({ botId, playMode }, 'playNext: reached end of queue in order mode, stopping');
          await this.stopPlayback(session);
          session.currentTrack = undefined;
          session.currentAudioUrl = undefined;
          this.startIdleTimer(session);
          return;
        }
        // 其他模式 fallback 到 queue[0]
        nextTrack = queue[0];
      }

      // 检查 audioUrl 是否可用
      let audioUrl = await this.resolveAudioUrl(nextTrack);
      if (!audioUrl) {
        // audioUrl 为空（netease URL 未同步或过期）
        // 阶段 4：跳过该曲目，尝试下一首（避免 play 失败死循环）
        // 阶段 5：此处应回调 Go 端解析 audioUrl
        log.warn(
          { botId, trackId: nextTrack.id, title: nextTrack.title, source: nextTrack.source },
          'playNext: failed to resolve next track audio URL, skipping'
        );
        // 迭代查找下一首有 audioUrl 的曲目
        const attempted = new Set<string>([nextTrack.id]);
        const maxAttempts = queue.length;
        for (let attempt = 0; attempt < maxAttempts; attempt++) {
          let candidate: Track | undefined;
          if (playMode === 'random') {
            candidate = this.nextShuffleTrack(session);
          } else {
            candidate = this.findNextInQueue(session, nextTrack.id, playMode !== 'order');
          }
          if (!candidate) break;
          if (attempted.has(candidate.id)) {
            nextTrack = candidate;
            continue;
          }
          const candidateUrl = await this.resolveAudioUrl(candidate);
          if (candidateUrl) {
            nextTrack = candidate;
            audioUrl = candidateUrl;
            break;
          }
          attempted.add(candidate.id);
          nextTrack = candidate;
        }
        if (!audioUrl) {
          // 队列中所有曲目都无 audioUrl，停止播放
          log.warn({ botId }, 'playNext: no track with audioUrl found, stopping');
          await this.stopPlayback(session);
          session.currentTrack = undefined;
          session.currentAudioUrl = undefined;
          this.startIdleTimer(session);
          return;
        }
      }

      log.info(
        { botId, isUserSkip, playMode, nextTitle: nextTrack.title, hasAudioUrl: !!audioUrl },
        'playNext: switching to next track'
      );

      // 准备播放请求（在锁外调用 play，避免死锁：play 方法需要获取同一个互斥锁）
      nextPlayReq = {
        track: nextTrack,
        audioUrl,
        roomName: session.roomName,
        channelId: session.channelId,
        volume: session.volume,
        playMode: session.playMode,
      };
    } finally {
      release();
    }

    // 在锁外触发播放（避免死锁：play 方法需要获取同一个互斥锁）
    if (nextPlayReq) {
      try {
        await this.play(botId, nextPlayReq);
      } catch (err) {
        log.error({ botId, err }, 'playNext: play failed after lock release');
      }
    }
  }

  /**
   * 上一首（回溯播放历史）
   * 若历史中存在上一首，则回溯播放；若无则无操作。
   */
  async previous(botId: string): Promise<void> {
    // 标记是否需要在释放锁后触发播放
    let prevPlayReq: PlayRequest | undefined;

    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'previous: session not found (no-op)');
        return;
      }

      const history = session.playHistory;
      if (history.length === 0) {
        log.info({ botId }, 'previous: no history, no-op');
        return;
      }

      // 若当前不在回溯态，从最新一条开始回溯
      let idx = session.historyIndex;
      if (idx === -1) {
        idx = history.length - 1;
      } else if (idx > 0) {
        idx -= 1;
      } else {
        // 已到历史最早一条，无操作
        log.info({ botId, idx }, 'previous: already at oldest history, no-op');
        return;
      }
      session.historyIndex = idx;
      const prevTrack = history[idx];
      log.info({ botId, historyIndex: idx, title: prevTrack.title }, 'previous: playing from history');

      // 准备播放请求（在锁外调用 play，避免死锁：play 方法需要获取同一个互斥锁）
      prevPlayReq = {
        track: prevTrack,
        audioUrl: await this.resolveAudioUrl(prevTrack),
        roomName: session.roomName,
        channelId: session.channelId,
        volume: session.volume,
        playMode: session.playMode,
      };
    } finally {
      release();
    }

    // 在锁外触发播放（避免死锁：play 方法需要获取同一个互斥锁）
    if (prevPlayReq) {
      try {
        await this.play(botId, prevPlayReq);
      } catch (err) {
        log.error({ botId, err }, 'previous: play failed after lock release');
      }
    }
  }

  /**
   * 设置播放模式
   * 切换到 random 模式时重新生成 shuffle 顺序
   */
  async setPlayMode(botId: string, mode: PlayMode, roomName?: string, channelId?: string): Promise<void> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      // session 不存在时创建轻量 session（不连接 LiveKit）
      // 这样 setPlayMode 能在 play 之前生效，后续 play 时直接使用已设置的模式
      let session = this.getSession(botId);
      if (!session) {
        session = this.getOrCreateSession(botId, roomName, channelId);
        if (!session) {
          log.warn({ botId, mode }, 'setPlayMode: session not found and no roomName/channelId provided (no-op)');
          return;
        }
        log.info({ botId, mode }, 'setPlayMode: created lightweight session for mode persistence');
      }
      const oldMode = session.playMode;
      session.playMode = mode;
      session.lastActivity = Date.now();
      // 切换到 random 时生成 shuffle 顺序（包含当前曲目之后的所有曲目）
      if (mode === 'random' && session.queue.length > 0) {
        this.regenerateShuffle(session);
      }
      // 离开 random 模式时清空 shuffle 状态
      if (oldMode === 'random' && mode !== 'random') {
        session.shuffleOrder = [];
        session.shuffleIndex = 0;
      }
      log.info({ botId, oldMode, newMode: mode }, 'PlayMode set');
    } finally {
      release();
    }
  }

  /** 断开 LiveKit 连接（外部接口） */
  async disconnect(botId: string): Promise<void> {
    const mutex = this.getMutex(botId);
    const release = await mutex.lock();
    try {
      const session = this.getSession(botId);
      if (!session) {
        log.debug({ botId }, 'disconnect: session not found (no-op)');
        return;
      }
      log.info({ botId }, 'Disconnecting bot');
      this.stopIdleTimer(session);
      await this.stopPlayback(session);
      session.currentTrack = undefined;
      session.currentAudioUrl = undefined;
      await this.disconnectRoom(session);
      this.sessions.delete(botId);
      this.botMutexes.delete(botId);
    } finally {
      release();
    }
  }

  // ===== 阶段 4：内部辅助方法 =====

  /**
   * 记录播放历史
   * 在 currentTrack 即将被替换为新曲目时调用
   * 若正在回溯历史（historyIndex >= 0），截断回溯位置之后的历史
   */
  private recordPlayHistory(session: BotSession): void {
    if (!session.currentTrack) return;
    // 若正在回溯历史，截断 forward 历史
    if (session.historyIndex >= 0 && session.playHistory.length > 0) {
      session.playHistory = session.playHistory.slice(0, session.historyIndex + 1);
    }
    session.playHistory.push(session.currentTrack);
    // 限制历史上限
    if (session.playHistory.length > MAX_PLAY_HISTORY) {
      session.playHistory = session.playHistory.slice(session.playHistory.length - MAX_PLAY_HISTORY);
    }
    session.historyIndex = -1; // 新播放重置回溯态
  }

  /**
   * 在队列中查找下一首曲目
   * @param currentId 当前曲目 ID（可选）
   * @param loop 是否循环到队列开头
   * @returns 下一首曲目，找不到返回 undefined
   */
  private findNextInQueue(session: BotSession, currentId: string | undefined, loop: boolean): Track | undefined {
    const queue = session.queue;
    if (queue.length === 0) return undefined;
    if (!currentId) return queue[0];

    const idx = queue.findIndex((t) => t.id === currentId);
    if (idx === -1) {
      // 当前曲目不在队列中（可能已被删除），从第一首开始
      return queue[0];
    }
    if (idx + 1 < queue.length) {
      return queue[idx + 1];
    }
    // 到末尾
    return loop ? queue[0] : undefined;
  }

  /**
   * 重新生成 shuffle 顺序（Fisher-Yates 洗牌）
   * 保留当前正在播放的曲目在 shuffle 顺序开头（避免立即切换）
   */
  private regenerateShuffle(session: BotSession): void {
    const queue = session.queue;
    if (queue.length === 0) {
      session.shuffleOrder = [];
      session.shuffleIndex = 0;
      return;
    }
    // 提取所有 track.id
    const ids = queue.map((t) => t.id);
    // Fisher-Yates 洗牌
    for (let i = ids.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [ids[i], ids[j]] = [ids[j], ids[i]];
    }
    // 若当前曲目在队列中，将其移到 shuffle 顺序开头（避免立即切换）
    if (session.currentTrack) {
      const currentIdx = ids.indexOf(session.currentTrack.id);
      if (currentIdx > 0) {
        [ids[0], ids[currentIdx]] = [ids[currentIdx], ids[0]];
      }
    }
    session.shuffleOrder = ids;
    session.shuffleIndex = 0;
    log.debug({ botId: session.botId, shuffleCount: ids.length }, 'Shuffle order regenerated');
  }

  /**
   * 获取 shuffle 顺序中的下一首曲目
   * 一轮播完后重新洗牌
   */
  private nextShuffleTrack(session: BotSession): Track | undefined {
    const queue = session.queue;
    if (queue.length === 0) return undefined;

    // shuffle 顺序为空或队列变更后未重新生成
    if (session.shuffleOrder.length === 0 || session.shuffleIndex >= session.shuffleOrder.length) {
      this.regenerateShuffle(session);
    }

    // 一轮播完，重新洗牌（排除当前曲目）
    if (session.shuffleIndex >= session.shuffleOrder.length) {
      this.regenerateShuffle(session);
    }

    const nextId = session.shuffleOrder[session.shuffleIndex];
    session.shuffleIndex++;
    // 在 queue 中查找对应曲目（queue 可能已变更）
    const track = queue.find((t) => t.id === nextId);
    if (!track) {
      // shuffle 顺序中的曲目已不在队列（被删除），递归找下一首
      return this.nextShuffleTrack(session);
    }
    return track;
  }

  /**
   * 解析曲目的音频 URL
   *
   * 优先复用曲目上仍有效的 URL；URL 缺失或临近过期时，通过 Go 内部接口
   * 按需解析。解析失败返回空字符串，让队列推进逻辑继续尝试下一首。
   */
  private async resolveAudioUrl(track: Track): Promise<string> {
    try {
      return await musicSourceResolver.resolve(track);
    } catch (err) {
      log.warn(
        { trackId: track.id, source: track.source, title: track.title, err },
        'Failed to resolve track audio URL'
      );
      return '';
    }
  }

  /** 启动空闲定时器（队列空 1 分钟后断连） */
  private startIdleTimer(session: BotSession): void {
    this.stopIdleTimer(session);
    session.idleTimer = setTimeout(() => {
      log.info({ botId: session.botId }, 'Idle timer fired, disconnecting');
      this.disconnect(session.botId).catch((err) => {
        log.error({ botId: session.botId, err }, 'Idle disconnect failed');
      });
    }, IDLE_DISCONNECT_TIMEOUT_MS);
  }

  /** 停止空闲定时器 */
  private stopIdleTimer(session: BotSession): void {
    if (session.idleTimer) {
      clearTimeout(session.idleTimer);
      session.idleTimer = undefined;
    }
  }
}

/**
 * 按 volume（0-100）对 AudioFrame 进行 in-place 音量缩放
 *
 * 实现：
 * - volume=100 时直接返回（不修改数据）
 * - volume=0 时清零所有采样
 * - 其他情况按 scale = volume/100 缩放 Int16 采样
 *
 * 为什么用 in-place 修改：
 * - AudioFrame 由 AudioByteStream 创建，每帧有独立 Int16Array，无其他消费者
 * - 避免创建新 AudioFrame 对象（减少 GC 压力）
 *
 * 性能：48kHz 单声道 100ms 帧 = 4800 样本，O(n) 线性扫描，开销可忽略。
 */
function applyVolumeInPlace(frame: AudioFrame, volume: number, duckingGain = 1): void {
  const scale = (Math.max(0, Math.min(100, volume)) / 100)
    * Math.max(0, Math.min(1, duckingGain));
  if (scale >= 0.999) {
    // volume=100，无需处理
    return;
  }
  const data = frame.data;
  if (scale === 0) {
    // 静音
    data.fill(0);
    return;
  }
  for (let i = 0; i < data.length; i++) {
    // Math.round 避免截断偏差；scale <= 1 保证不会溢出 Int16
    data[i] = Math.round(data[i] * scale);
  }
}

/**
 * 全局唯一的 BotManager 单例
 * 由 botRoutes.ts 和 index.ts 共享
 * 在模块加载时创建，避免与 index.ts 形成循环依赖
 */
export const botManager = new BotManager();
