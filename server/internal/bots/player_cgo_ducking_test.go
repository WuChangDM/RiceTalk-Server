//go:build cgo

package bots

import (
	"math"
	"testing"
	"time"

	"github.com/hraban/opus"
)

// newDuckTestPlayer constructs a BotPlayer with the minimum fields needed
// to exercise the M18 ducking logic without connecting to LiveKit or the
// database. The opus decoder/encoder are left nil by default; callers that
// need real PCM scaling (TestScaleOpusVolume_ScalesPCM) set them explicitly.
func newDuckTestPlayer() *BotPlayer {
	return &BotPlayer{
		volume:         100,
		duckMultiplier: 1.0,
		stopCh:         make(chan struct{}),
		ttsStopCh:      make(chan struct{}),
	}
}

// TestRestoreDucking_GradualRecovery verifies that restoreDucking gradually
// restores duckMultiplier from duckMultiplierDucked (0.3) back to 1.0 over
// 10 steps × 20ms (~200ms total). After allowing extra time for scheduling
// jitter, the multiplier must reach exactly 1.0.
func TestRestoreDucking_GradualRecovery(t *testing.T) {
	p := newDuckTestPlayer()

	p.mu.Lock()
	p.duckGen = 1
	p.duckMultiplier = duckMultiplierDucked // 0.3
	p.mu.Unlock()

	// Trigger recovery bound to generation 1.
	p.restoreDucking(1)

	// Recovery takes 10 steps × 20ms = 200ms. Sleep a bit longer to
	// accommodate goroutine scheduling jitter.
	time.Sleep(300 * time.Millisecond)

	p.mu.RLock()
	got := p.duckMultiplier
	p.mu.RUnlock()

	if got != 1.0 {
		t.Errorf("duckMultiplier = %v, want 1.0 after gradual recovery", got)
	}
}

// TestRestoreDucking_AbortsOnNewerDuck verifies that a stale recovery
// goroutine aborts when a newer ducking event bumps duckGen. The multiplier
// must NOT reach 1.0 because the recovery is cancelled partway through.
func TestRestoreDucking_AbortsOnNewerDuck(t *testing.T) {
	p := newDuckTestPlayer()

	p.mu.Lock()
	p.duckGen = 1
	p.duckMultiplier = duckMultiplierDucked // 0.3
	p.mu.Unlock()

	// Start recovery bound to generation 1.
	p.restoreDucking(1)

	// Immediately simulate a newer ducking request (new TTS started) by
	// bumping duckGen. The recovery goroutine sleeps 20ms before its first
	// check, so this bump happens before any step completes.
	p.mu.Lock()
	p.duckGen = 2
	p.mu.Unlock()

	// Wait long enough for the aborted recovery to have otherwise finished.
	time.Sleep(300 * time.Millisecond)

	p.mu.RLock()
	got := p.duckMultiplier
	p.mu.RUnlock()

	// Recovery was aborted, so the multiplier must stay well below 1.0.
	// With an immediate bump it should remain at 0.3, but allow a small
	// margin in case a single step updated before the bump was observed.
	if got >= 0.9 {
		t.Errorf("duckMultiplier = %v, want < 0.9 (recovery should have aborted on newer duck)", got)
	}
}

// TestScaleOpusVolume_IdentityCoeff verifies that scaleOpusVolume returns the
// original packet unchanged when coeff ≈ 1.0 (the fast path that skips
// decode/scale/re-encode).
func TestScaleOpusVolume_IdentityCoeff(t *testing.T) {
	// opusDecoder/opusEncoder are intentionally nil: the identity fast path
	// returns before touching them.
	p := newDuckTestPlayer()

	pkt := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0xFF, 0x10}

	out, err := p.scaleOpusVolume(pkt, 1.0)
	if err != nil {
		t.Fatalf("scaleOpusVolume(1.0) returned error: %v", err)
	}
	if len(out) != len(pkt) {
		t.Fatalf("output length = %d, want %d (identity should preserve length)", len(out), len(pkt))
	}
	for i := range pkt {
		if out[i] != pkt[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x (identity coeff must return original packet)", i, out[i], pkt[i])
		}
	}
}

// TestScaleOpusVolume_ScalesPCM verifies that scaleOpusVolume with coeff=0.3
// produces a packet whose decoded PCM amplitude is approximately 30% of the
// original (allowing tolerance for the lossy opus codec).
//
// Opus is a stateful, lossy codec: a cold encoder/decoder produces first
// frames whose amplitude deviates significantly from steady state (pre-skip
// delay, energy prediction warm-up). In production the playLoop feeds
// hundreds of frames through the codec, so it is always warm. This test
// mirrors that by processing a continuous stream of frames through
// scaleOpusVolume and priming the measurement decoders with the same stream
// before measuring the steady-state amplitude ratio.
func TestScaleOpusVolume_ScalesPCM(t *testing.T) {
	// External encoder used to synthesise a stream of reference packets.
	enc, err := opus.NewEncoder(opusSampleRate, opusChannels, opus.AppAudio)
	if err != nil {
		t.Fatalf("failed to create reference opus encoder: %v", err)
	}
	if err := enc.SetBitrate(128000); err != nil {
		t.Fatalf("failed to set encoder bitrate: %v", err)
	}

	// Generate a continuous 440 Hz sine wave at amplitude 1.0 across several
	// 20ms stereo frames. Phase advances with the absolute sample index so
	// the signal is continuous across frame boundaries.
	const numFrames = 12 // 8 priming + 1 measurement + margin
	const primeFrames = 8
	const measureIdx = primeFrames
	samplesPerChannel := pcmFrameSize / opusChannels
	allPkts := make([][]byte, numFrames)
	for f := 0; f < numFrames; f++ {
		pcm := make([]float32, pcmFrameSize)
		for i := 0; i < samplesPerChannel; i++ {
			idx := f*samplesPerChannel + i
			v := float32(math.Sin(2 * math.Pi * 440 * float64(idx) / float64(opusSampleRate)))
			pcm[i*opusChannels] = v   // left
			pcm[i*opusChannels+1] = v // right
		}
		buf := make([]byte, maxOpusPacketSize)
		n, err := enc.EncodeFloat32(pcm, buf)
		if err != nil {
			t.Fatalf("failed to encode frame %d: %v", f, err)
		}
		allPkts[f] = buf[:n]
	}

	// Build a player with fresh decoder/encoder. Process every frame with
	// coeff=0.3 so the player's codec warms up AND we produce a continuous
	// stream of scaled packets that a decoder can track.
	p := newDuckTestPlayer()
	p.opusDecoder, err = opus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		t.Fatalf("failed to create player decoder: %v", err)
	}
	p.opusEncoder, err = opus.NewEncoder(opusSampleRate, opusChannels, opus.AppAudio)
	if err != nil {
		t.Fatalf("failed to create player encoder: %v", err)
	}
	if err := p.opusEncoder.SetBitrate(128000); err != nil {
		t.Fatalf("failed to set player encoder bitrate: %v", err)
	}

	scaledPkts := make([][]byte, numFrames)
	for i := 0; i < numFrames; i++ {
		sp, err := p.scaleOpusVolume(allPkts[i], 0.3)
		if err != nil {
			t.Fatalf("scaleOpusVolume frame %d failed: %v", i, err)
		}
		if len(sp) == 0 {
			t.Fatalf("scaled packet %d is empty", i)
		}
		scaledPkts[i] = sp
	}

	// Measure original amplitude: prime a decoder with the first 8 frames of
	// the external encoder's stream, then decode the measurement frame.
	refDec, err := opus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		t.Fatalf("failed to create reference decoder: %v", err)
	}
	primeBuf := make([]float32, pcmFrameSize)
	for i := 0; i < primeFrames; i++ {
		if _, err := refDec.DecodeFloat32(allPkts[i], primeBuf); err != nil {
			t.Fatalf("priming refDec frame %d failed: %v", i, err)
		}
	}
	refPcm := make([]float32, pcmFrameSize)
	refN, err := refDec.DecodeFloat32(allPkts[measureIdx], refPcm)
	if err != nil {
		t.Fatalf("failed to decode reference measurement frame: %v", err)
	}
	origRMS := rms(refPcm[:refN*opusChannels])
	if origRMS == 0 {
		t.Fatal("reference RMS is 0; cannot compute amplitude ratio")
	}

	// Measure scaled amplitude: prime a decoder with the first 8 scaled
	// packets (same encoder stream), then decode the scaled measurement frame.
	scaledDec, err := opus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		t.Fatalf("failed to create scaled decoder: %v", err)
	}
	for i := 0; i < primeFrames; i++ {
		if _, err := scaledDec.DecodeFloat32(scaledPkts[i], primeBuf); err != nil {
			t.Fatalf("priming scaledDec frame %d failed: %v", i, err)
		}
	}
	scaledPcm := make([]float32, pcmFrameSize)
	scaledN, err := scaledDec.DecodeFloat32(scaledPkts[measureIdx], scaledPcm)
	if err != nil {
		t.Fatalf("failed to decode scaled measurement frame: %v", err)
	}
	scaledRMS := rms(scaledPcm[:scaledN*opusChannels])

	ratio := scaledRMS / origRMS
	// coeff=0.3 corresponds to volume=100, duckMultiplier=0.3. Expect the
	// decoded amplitude ratio to match within 5% (tolerance for residual
	// opus quantisation).
	const want = 0.3
	const tol = 0.05
	if ratio < want-tol || ratio > want+tol {
		t.Errorf("amplitude ratio = %.4f, want %.2f ± %.2f (scaledRMS=%.4f, origRMS=%.4f)",
			ratio, want, tol, scaledRMS, origRMS)
	}
}

// rms returns the root-mean-square of a PCM buffer, a robust amplitude
// measure for periodic signals that is less sensitive to single-sample
// codec jitter than peak detection.
func rms(pcm []float32) float32 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, v := range pcm {
		sum += float64(v) * float64(v)
	}
	return float32(math.Sqrt(sum / float64(len(pcm))))
}
