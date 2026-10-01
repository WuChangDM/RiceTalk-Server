import assert from 'node:assert/strict';
import test from 'node:test';
import {
  DuckingGainEnvelope,
  VoiceDuckingController,
  type VoiceDuckingGainTarget,
} from '../audio/voiceDucking.js';

test('闪避增益包络按 attack 时间平滑插值', () => {
  let now = 0;
  const envelope = new DuckingGainEnvelope(() => now);
  envelope.setDuckingGain(0.3, 50);
  now = 25;
  assert.equal(envelope.currentGain(), 0.65);
  now = 50;
  assert.equal(envelope.currentGain(), 0.3);
});

test('真人停止发言后经过 hold 恢复音乐音量', async () => {
  const changes: Array<{ gain: number; rampMs?: number }> = [];
  const target: VoiceDuckingGainTarget = {
    setDuckingGain(gain, rampMs) {
      changes.push({ gain, rampMs });
    },
  };
  const controller = new VoiceDuckingController(
    target,
    { enabled: true, volumePercent: 30 },
    { timing: { attackMs: 2, holdMs: 5, releaseMs: 3 } },
  );

  controller.handleVoiceActivity('user-1');
  assert.deepEqual(changes[0], { gain: 0.3, rampMs: 2 });
  assert.equal(controller.isDucking(), true);

  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.deepEqual(changes.at(-1), { gain: 1, rampMs: 3 });
  assert.equal(controller.isDucking(), false);
});
