import assert from 'node:assert/strict';
import test from 'node:test';
import {
  buildFfmpegInputOptions,
  shouldEndOnStall,
} from '../utils/audioDecode.js';

test('网易云输入参数包含来源请求头和完整重连策略', () => {
  const options = buildFfmpegInputOptions('https://m10.music.126.net/song.mp3', 12);
  assert.deepEqual(options.slice(-2), ['-ss', '12']);
  assert.ok(options.includes('-headers'));
  assert.ok(options.includes('-reconnect_at_eof'));
  assert.ok(options.includes('-reconnect_on_http_error'));
  assert.match(options.join(' '), /Referer: https:\/\/music\.163\.com\//);
});

test('本地文件不启用网络重连参数', () => {
  const options = buildFfmpegInputOptions('C:\\music\\local.mp3', 0);
  assert.deepEqual(options, []);
});

test('临近结尾使用短阈值，远离结尾使用长阈值', () => {
  assert.equal(shouldEndOnStall(49, true, 50, 600), false);
  assert.equal(shouldEndOnStall(50, true, 50, 600), true);
  assert.equal(shouldEndOnStall(50, false, 50, 600), false);
  assert.equal(shouldEndOnStall(600, false, 50, 600), true);
});
