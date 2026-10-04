//go:build cgo

package sfx

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hraban/opus"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"ridgericetalk/internal/config"
)

const (
	opusSampleRate    = 48000
	opusFrameDuration = 20 * time.Millisecond
	opusChannels      = 2
	opusBitrate       = "128k"

	// maxOpusPacketSize is the maximum encoded opus packet size we handle.
	maxOpusPacketSize = 4000
	// pcmFrameSize is the number of float32 samples per 20ms frame at 48kHz stereo.
	pcmFrameSize = opusSampleRate / 1000 * int(opusFrameDuration/time.Millisecond) * opusChannels

	// sfxIdleTimeout is how long the player stays connected with no playback before disconnecting.
	sfxIdleTimeout = 45 * time.Second

	// sfxPreConnectDelay gives the LiveKit subscription handshake time to settle before
	// we start writing samples, so the very first sfx play into a room (where the sfx bot
	// just connected and clients haven't finished subscribing to the new track) isn't lost.
	sfxPreConnectDelay = 800 * time.Millisecond
)

// oggPacketInfo records the file offset and size of an opus packet
type oggPacketInfo struct {
	offset int64
	size   int
}

// oggTrackReader holds an open OGG file and pre-scanned packet offsets
type oggTrackReader struct {
	file    *os.File
	packets []oggPacketInfo
}

func (r *oggTrackReader) ReadPacket(index int) ([]byte, error) {
	if index < 0 || index >= len(r.packets) {
		return nil, io.EOF
	}
	pkt := r.packets[index]
	buf := make([]byte, pkt.size)
	if _, err := r.file.ReadAt(buf, pkt.offset); err != nil {
		return nil, err
	}
	return buf, nil
}

func (r *oggTrackReader) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}

func (r *oggTrackReader) Len() int {
	return len(r.packets)
}

// SfxPlayer broadcasts short entrance/exit sound effects into a LiveKit room on a
// dedicated participant ("bot-sfx-<room>"), isolated from the music/TTS bots so it
// never preempts or ducks them. Each PlayOnce publishes a fresh track and unpublishes
// it when done, so concurrent sounds play simultaneously without interrupting each other.
type SfxPlayer struct {
	cfg    *config.Config
	logger protoLogger.Logger

	mu         sync.Mutex
	room       *lksdk.Room
	roomID     string
	lastActive time.Time
	idleTimer  *time.Timer

	opusDecoder *opus.Decoder
	opusEncoder *opus.Encoder
}

// NewSfxPlayer constructs the player (connection is lazy, on first PlayOnce).
func NewSfxPlayer(cfg *config.Config) *SfxPlayer {
	return &SfxPlayer{
		cfg:    cfg,
		logger: protoLogger.GetLogger(),
	}
}

// ensureConnected connects to the target room, reconnecting if the room changed.
// Callers must hold p.mu.
func (p *SfxPlayer) ensureConnected(roomID string) error {
	if p.room != nil && p.roomID == roomID {
		return nil
	}
	p.disconnectLocked()

	roomName := "rrt-room-" + roomID

	// Publish-only token. Hidden must stay false: hidden participants don't emit
	// ParticipantJoined on other clients, which makes the LiveKit SDK reject their
	// tracks. The sfx bot is filtered from UI via the API-sourced participant list.
	at := auth.NewAccessToken(p.cfg.LiveKitAPIKey, p.cfg.LiveKitAPISecret)
	canPublish := true
	canSubscribe := false
	hidden := false
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
		Hidden:       hidden,
	}
	at.SetVideoGrant(grant).
		SetIdentity("bot-sfx-" + roomID).
		SetName("音效")

	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to generate sfx token: %w", err)
	}

	room, err := lksdk.ConnectToRoomWithToken(p.cfg.LiveKitURL, token, nil)
	if err != nil {
		return fmt.Errorf("failed to connect sfx to room: %w", err)
	}

	decoder, err := opus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}
	encoder, err := opus.NewEncoder(opusSampleRate, opusChannels, opus.AppAudio)
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("failed to create opus encoder: %w", err)
	}
	if err := encoder.SetBitrate(128000); err != nil {
		p.logger.Warnw("failed to set sfx opus encoder bitrate", err)
	}

	p.room = room
	p.roomID = roomID
	p.opusDecoder = decoder
	p.opusEncoder = encoder
	return nil
}

// PlayOnce converts the source audio to OGG Opus and plays it into the room on a
// freshly published track, then unpublishes. volume is 0-200 (100 = unity). Safe for
// concurrent use: connection lifecycle is serialized, each play uses its own track.
func (p *SfxPlayer) PlayOnce(roomID, srcAbsPath string, volume int) error {
	p.mu.Lock()
	freshConnect := p.room == nil || p.roomID != roomID
	if err := p.ensureConnected(roomID); err != nil {
		p.mu.Unlock()
		return err
	}
	room := p.room
	p.touchLocked()
	p.mu.Unlock()

	// On a fresh connection, give clients a moment to discover the new participant and
	// subscribe to its track before we start streaming — otherwise the first play into a
	// room is silently lost (aligned with Discord/KOOK: entrance sound must be audible
	// to everyone including the person who just joined).
	if freshConnect {
		time.Sleep(sfxPreConnectDelay)
	}

	// Convert to OGG Opus in a temp file (removed when done).
	tmpDir := filepath.Join(p.cfg.LocalDataPath, "cache", "sfx")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("failed to create sfx cache dir: %w", err)
	}
	tmpOgg := filepath.Join(tmpDir, "sfx-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".ogg")
	defer os.Remove(tmpOgg)

	if err := convertToOpus(p.cfg, srcAbsPath, tmpOgg); err != nil {
		return err
	}
	reader, err := scanOggPackets(tmpOgg)
	if err != nil {
		return fmt.Errorf("failed to scan ogg: %w", err)
	}
	defer reader.Close()

	// Publish a dedicated track for this play (unpublished when done).
	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: opusSampleRate,
		Channels:  opusChannels,
	})
	if err != nil {
		return fmt.Errorf("failed to create sfx track: %w", err)
	}
	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "bot-sfx",
		Source: livekit.TrackSource_MICROPHONE,
		Stereo: true,
	})
	if err != nil {
		track.Close()
		return fmt.Errorf("failed to publish sfx track: %w", err)
	}
	// Give subscribers one more beat after publish before the first sample — the
	// track subscription handshake (client receives TrackPublished → auto-subscribes)
	// takes a few hundred ms; without this the opening of the sound is clipped.
	time.Sleep(300 * time.Millisecond)
	defer func() {
		room.LocalParticipant.UnpublishTrack(pub.SID())
		track.Close()
	}()

	coeff := float32(volume) / 100.0
	startTime := time.Now()
	for i := 0; i < reader.Len(); i++ {
		// Pace packets to real time (20ms each).
		target := startTime.Add(time.Duration(i) * opusFrameDuration)
		if d := time.Until(target); d > 0 {
			time.Sleep(d)
		}
		pkt, err := reader.ReadPacket(i)
		if err != nil {
			break
		}
		if volume != 100 {
			scaled, serr := p.scaleOpusVolume(pkt, coeff)
			if serr == nil {
				pkt = scaled
			}
		}
		if err := track.WriteSample(media.Sample{Data: pkt, Duration: opusFrameDuration}, nil); err != nil {
			break
		}
	}
	return nil
}

// scaleOpusVolume decodes an opus packet to float32 PCM, scales every sample by
// coeff, and re-encodes. coeff ~1.0 returns the packet unchanged; on any failure the
// original packet is returned as a fallback.
func (p *SfxPlayer) scaleOpusVolume(pkt []byte, coeff float32) ([]byte, error) {
	if coeff > 0.999 && coeff < 1.001 {
		return pkt, nil
	}
	if p.opusDecoder == nil || p.opusEncoder == nil {
		return pkt, nil
	}
	pcm := make([]float32, pcmFrameSize)
	n, err := p.opusDecoder.DecodeFloat32(pkt, pcm)
	if err != nil {
		return pkt, fmt.Errorf("opus decode: %w", err)
	}
	total := n * opusChannels
	for i := 0; i < total; i++ {
		pcm[i] *= coeff
	}
	outBuf := make([]byte, maxOpusPacketSize)
	m, err := p.opusEncoder.EncodeFloat32(pcm[:total], outBuf)
	if err != nil {
		return pkt, fmt.Errorf("opus encode: %w", err)
	}
	return outBuf[:m], nil
}

// touchLocked records activity and (re)arms the idle-disconnect timer. Caller holds p.mu.
func (p *SfxPlayer) touchLocked() {
	p.lastActive = time.Now()
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	p.idleTimer = time.AfterFunc(sfxIdleTimeout, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if time.Since(p.lastActive) >= sfxIdleTimeout {
			p.disconnectLocked()
		}
	})
}

// Disconnect leaves the room (idempotent).
func (p *SfxPlayer) Disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disconnectLocked()
}

// disconnectLocked tears down the connection. Caller holds p.mu.
func (p *SfxPlayer) disconnectLocked() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
	if p.room != nil {
		p.room.Disconnect()
		p.room = nil
	}
	p.roomID = ""
	p.opusDecoder = nil
	p.opusEncoder = nil
}

// convertToOpus converts audio to OGG Opus using FFmpeg (20ms pages = 1 packet/page).
func convertToOpus(cfg *config.Config, inputPath, outputPath string) error {
	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", inputPath,
		"-vn", // skip video/cover streams so scanOggPackets finds OpusHead
		"-c:a", "libopus",
		"-b:a", opusBitrate,
		"-ar", strconv.Itoa(opusSampleRate),
		"-ac", strconv.Itoa(opusChannels),
		"-page_duration", "20000",
		"-f", "ogg",
		outputPath,
	}
	cmd := exec.Command(ffmpegPath(cfg), args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w, output: %s", err, string(output))
	}
	return nil
}

// scanOggPackets scans an OGG Opus file and returns packet offsets without loading data
func scanOggPackets(path string) (*oggTrackReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	readPage := func(needPayload bool) ([]byte, error) {
		header := make([]byte, 27)
		if _, err := io.ReadFull(f, header); err != nil {
			return nil, err
		}
		if string(header[:4]) != "OggS" {
			return nil, fmt.Errorf("invalid OGG page signature")
		}
		segCount := int(header[26])
		sizeBuf := make([]byte, segCount)
		if _, err := io.ReadFull(f, sizeBuf); err != nil {
			return nil, err
		}
		payloadSize := 0
		for _, s := range sizeBuf {
			payloadSize += int(s)
		}
		if !needPayload {
			if _, err := f.Seek(int64(payloadSize), io.SeekCurrent); err != nil {
				return nil, err
			}
			return nil, nil
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(f, payload); err != nil {
			return nil, err
		}
		return payload, nil
	}

	// Skip OpusHead page
	payload, err := readPage(true)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to read OpusHead: %w", err)
	}
	if !isOpusHeader(payload) {
		f.Close()
		return nil, fmt.Errorf("missing OpusHead")
	}

	// Skip OpusTags page
	payload, err = readPage(true)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to read OpusTags: %w", err)
	}
	if !isOpusTags(payload) {
		f.Close()
		return nil, fmt.Errorf("missing OpusTags")
	}

	// Scan remaining audio pages, recording payload offsets
	var packets []oggPacketInfo
	for {
		pageStart, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			break
		}
		header := make([]byte, 27)
		if _, err := io.ReadFull(f, header); err != nil {
			if err == io.EOF {
				break
			}
			continue
		}
		if string(header[:4]) != "OggS" {
			break
		}
		segCount := int(header[26])
		sizeBuf := make([]byte, segCount)
		if _, err := io.ReadFull(f, sizeBuf); err != nil {
			break
		}
		payloadSize := 0
		for _, s := range sizeBuf {
			payloadSize += int(s)
		}
		if payloadSize > 0 {
			payloadOffset := pageStart + 27 + int64(segCount)
			packets = append(packets, oggPacketInfo{offset: payloadOffset, size: payloadSize})
		}
		if _, err := f.Seek(int64(payloadSize), io.SeekCurrent); err != nil {
			break
		}
	}

	return &oggTrackReader{file: f, packets: packets}, nil
}

func isOpusHeader(payload []byte) bool {
	return len(payload) >= 8 && string(payload[:8]) == "OpusHead"
}

func isOpusTags(payload []byte) bool {
	return len(payload) >= 8 && string(payload[:8]) == "OpusTags"
}
