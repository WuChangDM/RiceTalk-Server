// 基于 TSMusicBot VoiceDuckingController 的 MIT 许可设计改造：
// https://github.com/ZHANGTIANYAO1/teamspeak-music-bot/blob/main/src/bot/voice-ducking.ts
// 适配 LiveKit ActiveSpeakersChanged 事件及 RidgeRiceTalk 的 PCM 帧播放链路。

export interface VoiceDuckingSettings {
  enabled: boolean;
  volumePercent: number;
}

export interface VoiceDuckingTiming {
  attackMs: number;
  holdMs: number;
  releaseMs: number;
}

export interface VoiceDuckingGainTarget {
  setDuckingGain(gain: number, rampMs?: number): void;
}

export interface VoiceDuckingControllerOptions {
  timing?: Partial<VoiceDuckingTiming>;
  now?: () => number;
}

export const DEFAULT_VOICE_DUCKING_TIMING: Readonly<VoiceDuckingTiming> = {
  attackMs: 50,
  holdMs: 700,
  releaseMs: 500,
};

function clampGain(value: number, fallback = 1): number {
  return Number.isFinite(value) ? Math.max(0, Math.min(1, value)) : fallback;
}

function nonNegativeFinite(value: number | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(0, value)
    : fallback;
}

function normalizeSettings(settings: VoiceDuckingSettings): VoiceDuckingSettings {
  return {
    enabled: settings.enabled === true,
    volumePercent: Number.isFinite(settings.volumePercent)
      ? Math.max(0, Math.min(100, settings.volumePercent))
      : 30,
  };
}

/**
 * 每个播放会话独立持有的平滑增益包络。
 *
 * setDuckingGain 只改变临时增益，不会改写用户保存的 volume；currentGain
 * 在每个 PCM 帧到达时按时间插值，因此无需高频 timer。
 */
export class DuckingGainEnvelope implements VoiceDuckingGainTarget {
  private rampStartGain = 1;
  private targetGain = 1;
  private rampStartedAt = 0;
  private rampDurationMs = 0;

  constructor(private readonly now: () => number = () => performance.now()) {}

  setDuckingGain(gain: number, rampMs = 0): void {
    this.rampStartGain = this.currentGain();
    this.targetGain = clampGain(gain);
    this.rampStartedAt = this.now();
    this.rampDurationMs = nonNegativeFinite(rampMs, 0);
    if (this.rampDurationMs === 0) {
      this.rampStartGain = this.targetGain;
    }
  }

  currentGain(at = this.now()): number {
    if (this.rampDurationMs <= 0) return this.targetGain;
    const progress = Math.max(0, Math.min(1, (at - this.rampStartedAt) / this.rampDurationMs));
    if (progress >= 1) {
      this.rampStartGain = this.targetGain;
      this.rampDurationMs = 0;
      return this.targetGain;
    }
    return this.rampStartGain + (this.targetGain - this.rampStartGain) * progress;
  }
}

/**
 * 将 LiveKit 活跃说话者事件转换为稳定的音乐闪避包络。
 *
 * 每个说话者只更新 deadline，全控制器最多保留一个 timer；旧 timer 通过
 * generation 隔离，避免在快速加入/离开及断线重连时误恢复音量。
 */
export class VoiceDuckingController {
  private settings: VoiceDuckingSettings;
  private readonly timing: VoiceDuckingTiming;
  private readonly now: () => number;
  private readonly activeUntil = new Map<string, number>();
  private expiryTimer: ReturnType<typeof setTimeout> | undefined;
  private timerDueAt = Number.POSITIVE_INFINITY;
  private timerGeneration = 0;
  private ducking = false;

  constructor(
    private readonly target: VoiceDuckingGainTarget,
    initialSettings: VoiceDuckingSettings,
    options: VoiceDuckingControllerOptions = {},
  ) {
    this.settings = normalizeSettings(initialSettings);
    this.timing = {
      attackMs: nonNegativeFinite(options.timing?.attackMs, DEFAULT_VOICE_DUCKING_TIMING.attackMs),
      holdMs: nonNegativeFinite(options.timing?.holdMs, DEFAULT_VOICE_DUCKING_TIMING.holdMs),
      releaseMs: nonNegativeFinite(options.timing?.releaseMs, DEFAULT_VOICE_DUCKING_TIMING.releaseMs),
    };
    this.now = options.now ?? (() => performance.now());
  }

  handleVoiceActivity(participantIdentity: string): void {
    if (!this.settings.enabled || !participantIdentity) return;
    const now = this.now();
    this.activeUntil.set(participantIdentity, now + this.timing.holdMs);
    if (!this.ducking) {
      this.ducking = true;
      this.target.setDuckingGain(this.settings.volumePercent / 100, this.timing.attackMs);
    }
    this.scheduleNextSweep(now);
  }

  removeSpeaker(participantIdentity: string): void {
    if (!this.activeUntil.delete(participantIdentity)) return;
    if (this.activeUntil.size === 0) {
      this.cancelTimer();
      this.release();
    }
  }

  updateSettings(settings: VoiceDuckingSettings): void {
    const previous = this.settings;
    this.settings = normalizeSettings(settings);
    if (!this.settings.enabled) {
      this.activeUntil.clear();
      this.cancelTimer();
      this.release();
      return;
    }
    if (this.ducking && previous.volumePercent !== this.settings.volumePercent) {
      this.target.setDuckingGain(this.settings.volumePercent / 100, this.timing.attackMs);
    }
  }

  reset(immediate = true): void {
    this.activeUntil.clear();
    this.cancelTimer();
    this.ducking = false;
    this.target.setDuckingGain(1, immediate ? 0 : this.timing.releaseMs);
  }

  isDucking(): boolean {
    return this.ducking;
  }

  activeSpeakerCount(): number {
    return this.activeUntil.size;
  }

  private scheduleNextSweep(now = this.now()): void {
    if (this.activeUntil.size === 0) return;
    let nextDueAt = Number.POSITIVE_INFINITY;
    for (const deadline of this.activeUntil.values()) {
      if (deadline < nextDueAt) nextDueAt = deadline;
    }

    // 保留更早的 timer；它触发时会读取更新后的 deadline，避免语音包造成 timer churn。
    if (this.expiryTimer && this.timerDueAt <= nextDueAt) return;
    this.cancelTimer();
    const generation = ++this.timerGeneration;
    this.timerDueAt = nextDueAt;
    this.expiryTimer = setTimeout(() => {
      if (generation !== this.timerGeneration) return;
      this.expiryTimer = undefined;
      this.timerDueAt = Number.POSITIVE_INFINITY;
      this.sweepExpiredSpeakers();
    }, Math.max(0, nextDueAt - now));
    this.expiryTimer.unref?.();
  }

  private sweepExpiredSpeakers(): void {
    const now = this.now();
    for (const [participantIdentity, deadline] of this.activeUntil) {
      if (deadline <= now) this.activeUntil.delete(participantIdentity);
    }
    if (this.activeUntil.size > 0) {
      this.scheduleNextSweep(now);
    } else {
      this.release();
    }
  }

  private release(): void {
    if (!this.ducking) return;
    this.ducking = false;
    this.target.setDuckingGain(1, this.timing.releaseMs);
  }

  private cancelTimer(): void {
    this.timerGeneration++;
    if (this.expiryTimer) clearTimeout(this.expiryTimer);
    this.expiryTimer = undefined;
    this.timerDueAt = Number.POSITIVE_INFINITY;
  }
}
