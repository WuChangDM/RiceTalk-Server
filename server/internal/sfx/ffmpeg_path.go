package sfx

import "ridgericetalk/internal/config"

// ffmpegPath returns the configured FFmpeg binary path, or "ffmpeg" from PATH
// if no explicit path is set.
func ffmpegPath(cfg *config.Config) string {
	if cfg != nil && cfg.FFmpegBinaryPath != "" {
		return cfg.FFmpegBinaryPath
	}
	return "ffmpeg"
}

// ffprobePath returns the configured ffprobe binary path, or "ffprobe" from
// PATH if no explicit path is set.
func ffprobePath(cfg *config.Config) string {
	if cfg != nil && cfg.FFprobeBinaryPath != "" {
		return cfg.FFprobeBinaryPath
	}
	return "ffprobe"
}
