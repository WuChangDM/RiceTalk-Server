package voice

import "testing"

// Red-line guard: bot identities must be filtered so the sfx bot joining a room
// does not retrigger sfx playback in an infinite loop, and so bots never write
// ghost VoiceParticipant rows.
func TestIsBotIdentity(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"bot-music-abc123", true},
		{"bot-tts", true},
		{"bot-sfx-room42", true},
		{"bot-sfx-", true},
		// Regular users must NOT be filtered
		{"user123", false},
		{"550e8400-e29b-41d4-a716-446655440000", false},
		{"bot", false},          // prefix but not a full bot identity
		{"bot-musician", false}, // close but not "bot-music-"
		{"", false},
		{"a-bot-sfx-x", false}, // prefix must be at the start
	}
	for _, c := range cases {
		if got := isBotIdentity(c.id); got != c.want {
			t.Errorf("isBotIdentity(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestIsVirtualIdentity A2 红线：{uid}:share 虚身份必须被过滤（不写参与者表、
// 不触发音效），真实用户不受影响，原 bot 判定不被破坏。
func TestIsVirtualIdentity(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"uid123:share", true},
		{"550e8400-e29b-41d4-a716-446655440000:share", true},
		// 真实身份
		{"uid123", false},
		{"", false},
		// 原 bot 判定不破坏
		{"bot-music-x", true},
		{"bot-tts", true},
		{"bot-sfx-room42", true},
		// 边界：纯后缀、bot 前缀+
		{":share", false},
		{"bot-music-x:share", true}, // bot 判定先行命中，仍过滤（真实 uid 无冒号，不冲突）
	}
	for _, c := range cases {
		if got := isVirtualIdentity(c.id); got != c.want {
			t.Errorf("isVirtualIdentity(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestNormalizeIdentity A2 红线：track_published/unpublished 必须把 :share
// 虚身份归一化到真实 uid（is_screen_sharing 落在真实用户行）。
func TestNormalizeIdentity(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"uid123:share", "uid123"},
		{"uid123", "uid123"},
		{"bot-music-x", "bot-music-x"},
		{"", ""},
		{":share", ""},
	}
	for _, c := range cases {
		if got := normalizeIdentity(c.id); got != c.want {
			t.Errorf("normalizeIdentity(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}
