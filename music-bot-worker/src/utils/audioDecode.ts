// RidgeRiceTalk Music Bot Worker - 音频解码辅助
// 阶段 3：支持 seek 的音频文件解码
//
// @livekit/agents 的 audioFramesFromFile 不支持 seek（无 -ss 参数），
// 此模块基于 fluent-ffmpeg + AudioByteStream 实现带 seek 的版本，
// 行为与 audioFramesFromFile 一致（s16le PCM → AudioFrame 流）。

import ffmpeg from 'fluent-ffmpeg';
import { AudioByteStream } from '@livekit/agents';
import { AudioFrame } from '@livekit/rtc-node';
import { PassThrough } from 'node:stream';
import { ReadableStream } from 'node:stream/web';

export interface AudioDecodeWithSeekOptions {
  sampleRate?: number;
  numChannels?: number;
  /** 起始位置（秒），0 表示从头开始 */
  seekSeconds?: number;
  abortSignal?: AbortSignal;
}

const BUFFER_HIGH_WATER = 640 * 1024;
const BUFFER_LOW_WATER = 256 * 1024;
const BUFFER_RESUME_DESIRED_SIZE = BUFFER_HIGH_WATER - BUFFER_LOW_WATER;
const BROWSER_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36';

/**
 * 基于 TSMusicBot buildFfmpegArgs 的 MIT 许可实现改造：
 * https://github.com/ZHANGTIANYAO1/teamspeak-music-bot/blob/main/src/audio/player.ts
 */
export function buildFfmpegInputOptions(filePath: string, seekSeconds: number): string[] {
  const options: string[] = [];
  const isHTTP = /^https?:\/\//i.test(filePath);

  if (isHTTP && (filePath.includes('bilivideo') || filePath.includes('bilibili'))) {
    options.push(
      '-headers',
      `Referer: https://www.bilibili.com\r\nUser-Agent: ${BROWSER_UA}\r\n`,
    );
  } else if (isHTTP && (filePath.includes('music.126.net') || filePath.includes('music.163.com'))) {
    options.push(
      '-headers',
      `Referer: https://music.163.com/\r\nUser-Agent: ${BROWSER_UA}\r\n`,
    );
  }

  if (isHTTP) {
    options.push(
      '-reconnect', '1',
      '-reconnect_at_eof', '1',
      '-reconnect_streamed', '1',
      '-reconnect_delay_max', '30',
      '-reconnect_on_network_error', '1',
      '-reconnect_on_http_error', '4xx,5xx',
    );
  }

  if (seekSeconds > 0) {
    options.push('-ss', String(seekSeconds));
  }
  return options;
}

/** 区分临近结尾的正常 EOF 与远离结尾的永久死流。 */
export function shouldEndOnStall(
  emptyAttempts: number,
  isNearEnd: boolean,
  maxEmptyAttempts: number,
  maxStallAttempts: number,
): boolean {
  if (isNearEnd && emptyAttempts >= maxEmptyAttempts) return true;
  return emptyAttempts >= maxStallAttempts;
}

/**
 * 从音频文件解码为 AudioFrame 流，支持从指定位置开始
 *
 * 实现与 @livekit/agents.audioFramesFromFile 一致：
 * - 使用 fluent-ffmpeg 调用 FFmpeg
 * - 输出 s16le PCM 格式
 * - 通过 AudioByteStream 将字节流切分为固定大小的 AudioFrame
 *
 * seekSeconds > 0 时，添加 -ss 输入选项（FFmpeg 在解码前快速定位）
 */
export function audioFramesFromFileWithSeek(
  filePath: string,
  options: AudioDecodeWithSeekOptions = {},
): ReadableStream<AudioFrame> {
  const sampleRate = options.sampleRate ?? 48000;
  const numChannels = options.numChannels ?? 1;
  const seekSeconds = Math.max(0, options.seekSeconds ?? 0);

  const audioStream = new AudioByteStream(sampleRate, numChannels);

  // 构造 FFmpeg 命令
  const command = ffmpeg(filePath);
  const inputOptions = buildFfmpegInputOptions(filePath, seekSeconds);
  if (inputOptions.length > 0) {
    command.inputOptions(inputOptions);
  }

  command
    .format('s16le')
    .audioChannels(numChannels)
    .audioFrequency(sampleRate);

  let commandRunning = true;
  let closed = false;
  let outputStream: PassThrough | undefined;
  let outputPaused = false;
  let streamController: ReadableStreamDefaultController<AudioFrame> | undefined;

  const onClose = () => {
    if (closed) return;
    closed = true;
    if (outputPaused && outputStream) {
      outputStream.resume();
      outputPaused = false;
    }
    if (commandRunning) {
      commandRunning = false;
      try {
        command.kill('SIGKILL');
      } catch {
        // 进程可能已退出
      }
    }
  };

  const fail = (err: Error) => {
    if (closed) return;
    // FFmpeg 关闭时的 teardown error 是预期的
    const msg = err.message || '';
    if (!msg.includes('SIGKILL') && !msg.includes('Exiting') && !msg.includes('terminated')) {
      try {
        streamController?.error(err);
      } catch {
        // controller 已结束
      }
    }
    commandRunning = false;
    onClose();
  };

  // 使用 ReadableStream 包装
  const stream = new ReadableStream<AudioFrame>({
    start(controller) {
      streamController = controller;
      command.on('error', fail);
      const ffmpegOutput = new PassThrough();
      outputStream = ffmpegOutput;
      command.pipe(ffmpegOutput);

      ffmpegOutput.on('data', (chunk: Buffer) => {
        if (closed) return;
        const arrayBuffer = chunk.buffer.slice(
          chunk.byteOffset,
          chunk.byteOffset + chunk.byteLength,
        ) as ArrayBuffer;
        const frames = audioStream.write(arrayBuffer);
        for (const frame of frames) {
          controller.enqueue(frame);
        }
        // Web Stream 的 size() 以 PCM 字节数计量。达到高水位后暂停 FFmpeg
        // stdout，等消费到低水位再由 pull() 恢复，避免慢网络/LiveKit 背压时爆内存。
        if ((controller.desiredSize ?? 1) <= 0 && !outputPaused) {
          outputStream?.pause();
          outputPaused = true;
        }
      });

      ffmpegOutput.on('end', () => {
        if (closed) return;
        const frames = audioStream.flush();
        for (const frame of frames) {
          controller.enqueue(frame);
        }
        commandRunning = false;
        controller.close();
      });

      ffmpegOutput.on('error', fail);

      options.abortSignal?.addEventListener('abort', () => {
        onClose();
        try {
          controller.close();
        } catch {
          // controller 可能已关闭
        }
      }, { once: true });
    },
    pull(controller) {
      if (
        outputPaused
        && outputStream
        && (controller.desiredSize ?? 0) >= BUFFER_RESUME_DESIRED_SIZE
      ) {
        outputStream.resume();
        outputPaused = false;
      }
    },
    cancel() {
      onClose();
    },
  }, {
    highWaterMark: BUFFER_HIGH_WATER,
    size(frame) {
      return frame.data.byteLength;
    },
  });

  return stream;
}
