package bots

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// createUserSpaceMembership creates a test user, space, and membership record.
func createUserSpaceMembership(t *testing.T, db *gorm.DB) (userID, spaceID string) {
	userID = idgen.NextString()
	spaceID = idgen.NextString()
	now := time.Now()

	user := model.User{
		ID:           userID,
		Username:     idgen.NextString(),
		Email:        idgen.NextString() + "@example.com",
		PasswordHash: "hash",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	space := model.Space{
		ID:        spaceID,
		Name:      "TestSpace",
		OwnerID:   userID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&space).Error; err != nil {
		t.Fatalf("failed to create space: %v", err)
	}

	membership := model.Membership{
		ID:        idgen.NextString(),
		UserID:    userID,
		SpaceID:   spaceID,
		Role:      "MEMBER",
		JoinedAt:  now,
		CreatedAt: now,
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}

	return userID, spaceID
}

func TestIsValidAudioMagic(t *testing.T) {
	tests := []struct {
		name   string
		header []byte
		want   bool
	}{
		// MP3 with ID3 tag
		{"mp3 id3 tag", []byte("ID3\x04\x00\x00\x00"), true},
		// MP3 with MPEG sync word
		{"mp3 sync word", []byte{0xFF, 0xE0, 0x00, 0x00}, true},
		{"mp3 sync word variant", []byte{0xFF, 0xFB, 0x90, 0x00}, true},
		// WAV
		{"wav valid", []byte("RIFF\x00\x00\x00\x00WAVE"), true},
		{"wav riff but not wave", []byte("RIFF\x00\x00\x00\x00AVI "), false},
		// OGG
		{"ogg valid", []byte("OggS\x00\x02\x00\x00"), true},
		// FLAC
		{"flac valid", []byte("fLaC\x00\x00\x00\x00"), true},
		// Invalid
		{"empty", []byte{}, false},
		{"too short", []byte{0x00, 0x01}, false},
		{"random bytes", []byte{0xDE, 0xAD, 0xBE, 0xEF}, false},
		{"png header", []byte("\x89PNG\r\n\x1a\n"), false},
		{"pdf header", []byte("%PDF-1.4"), false},
		{"zip header", []byte("PK\x03\x04"), false},
		{"exe header", []byte("MZ\x90\x00"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidAudioMagic(tt.header)
			if got != tt.want {
				t.Errorf("isValidAudioMagic() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllowedAudioTypes(t *testing.T) {
	allowed := []string{
		"audio/mpeg",
		"audio/mp3",
		"audio/wav",
		"audio/ogg",
		"audio/opus",
		"audio/flac",
	}
	for _, mt := range allowed {
		t.Run("allowed_"+mt, func(t *testing.T) {
			if !allowedAudioTypes[mt] {
				t.Errorf("expected %s to be allowed", mt)
			}
		})
	}

	disallowed := []string{
		"audio/aac",
		"audio/midi",
		"video/mp4",
		"application/octet-stream",
		"image/png",
		"text/plain",
	}
	for _, mt := range disallowed {
		t.Run("disallowed_"+mt, func(t *testing.T) {
			if allowedAudioTypes[mt] {
				t.Errorf("expected %s to be disallowed", mt)
			}
		})
	}
}

// TestBotTokenManagementBatch9c 批次9c M22新增：验证 Bot token 管理 service 方法
func TestBotTokenManagementBatch9c(t *testing.T) {
	t.Run("CreateBotToken generates unique token", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		userID, spaceID := createUserSpaceMembership(t, db)
		bot, err := svc.CreateBotToken(spaceID, nil, "MusicBot", userID)
		if err != nil {
			t.Fatalf("CreateBotToken failed: %v", err)
		}
		if bot.ID == "" {
			t.Error("expected bot ID to be set")
		}
		if bot.Token == "" {
			t.Error("expected bot token to be set")
		}
		if len(bot.Token) != 64 { // 32 bytes hex = 64 chars
			t.Errorf("expected token length 64, got %d", len(bot.Token))
		}
		if bot.SpaceID != spaceID {
			t.Errorf("expected spaceID %s, got %s", spaceID, bot.SpaceID)
		}
		if bot.Name != "MusicBot" {
			t.Errorf("expected name 'MusicBot', got '%s'", bot.Name)
		}
		if bot.CreatedBy != userID {
			t.Errorf("expected createdBy '%s', got '%s'", userID, bot.CreatedBy)
		}
	})

	t.Run("CreateBotToken generates unique tokens each call", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		userID, spaceID := createUserSpaceMembership(t, db)
		bot1, _ := svc.CreateBotToken(spaceID, nil, "Bot1", userID)
		bot2, _ := svc.CreateBotToken(spaceID, nil, "Bot2", userID)

		if bot1.Token == bot2.Token {
			t.Error("expected different tokens for different bots")
		}
	})

	t.Run("ListBotTokens returns tokens with masked token field", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		userID, spaceID := createUserSpaceMembership(t, db)
		_, err := svc.CreateBotToken(spaceID, nil, "MusicBot", userID)
		if err != nil {
			t.Fatalf("CreateBotToken failed: %v", err)
		}

		items, err := svc.ListBotTokens(spaceID)
		if err != nil {
			t.Fatalf("ListBotTokens failed: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("expected 1 token, got %d", len(items))
		}

		masked, ok := items[0]["token"].(string)
		if !ok {
			t.Fatal("expected token field to be string")
		}
		if !strings.HasPrefix(masked, "****") {
			t.Errorf("expected token to be masked with '****' prefix, got '%s'", masked)
		}
		if len(masked) != 8 { // "****" + last 4 chars
			t.Errorf("expected masked token length 8, got %d", len(masked))
		}
	})

	t.Run("ListBotTokens returns empty for space with no tokens", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		_, spaceID := createUserSpaceMembership(t, db)
		items, err := svc.ListBotTokens(spaceID)
		if err != nil {
			t.Fatalf("ListBotTokens failed: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("expected 0 tokens, got %d", len(items))
		}
	})

	t.Run("DeleteBotToken soft-deletes token", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		userID, spaceID := createUserSpaceMembership(t, db)
		bot, err := svc.CreateBotToken(spaceID, nil, "MusicBot", userID)
		if err != nil {
			t.Fatalf("CreateBotToken failed: %v", err)
		}

		// Delete the token
		if err := svc.DeleteBotToken(bot.ID); err != nil {
			t.Fatalf("DeleteBotToken failed: %v", err)
		}

		// Verify token no longer appears in list (soft-deleted)
		items, err := svc.ListBotTokens(spaceID)
		if err != nil {
			t.Fatalf("ListBotTokens failed: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("expected 0 tokens after deletion, got %d", len(items))
		}
	})

	t.Run("DeleteBotToken returns NotFound for non-existent ID", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		err := svc.DeleteBotToken("non-existent-id")
		if err == nil {
			t.Error("expected error for non-existent bot token ID")
		}
	})
}

func TestNeteaseAPIEndpoint(t *testing.T) {
	t.Run("env variable takes precedence", func(t *testing.T) {
		s := &Service{
			cfg: &config.Config{NeteaseAPIEndpoint: "http://from-config:3000"},
		}
		t.Setenv("RRT_NETEASE_API_ENDPOINT", "http://from-env:3300")
		got := s.neteaseAPIEndpoint()
		if got != "http://from-env:3300" {
			t.Errorf("neteaseAPIEndpoint() = %q, want %q", got, "http://from-env:3300")
		}
	})

	t.Run("falls back to config", func(t *testing.T) {
		s := &Service{
			cfg: &config.Config{NeteaseAPIEndpoint: "http://from-config:3000"},
		}
		t.Setenv("RRT_NETEASE_API_ENDPOINT", "")
		got := s.neteaseAPIEndpoint()
		if got != "http://from-config:3000" {
			t.Errorf("neteaseAPIEndpoint() = %q, want %q", got, "http://from-config:3000")
		}
	})

	t.Run("strips trailing slash", func(t *testing.T) {
		s := &Service{cfg: &config.Config{}}
		t.Setenv("RRT_NETEASE_API_ENDPOINT", "http://localhost:3300/")
		got := s.neteaseAPIEndpoint()
		if got != "http://localhost:3300" {
			t.Errorf("neteaseAPIEndpoint() = %q, want %q", got, "http://localhost:3300")
		}
	})

	t.Run("returns empty when not configured", func(t *testing.T) {
		s := &Service{cfg: &config.Config{}}
		t.Setenv("RRT_NETEASE_API_ENDPOINT", "")
		got := s.neteaseAPIEndpoint()
		if got != "" {
			t.Errorf("neteaseAPIEndpoint() = %q, want empty", got)
		}
	})
}

func TestGetNeteaseSearchEmptyKeyword(t *testing.T) {
	// When keyword is empty, should return empty result without calling API
	s := &Service{cfg: &config.Config{}}
	t.Setenv("RRT_NETEASE_API_ENDPOINT", "")
	result, err := s.GetNeteaseSearch("", 20)
	if err != nil {
		t.Fatalf("GetNeteaseSearch('') error = %v", err)
	}
	songs, ok := result["songs"].([]interface{})
	if !ok {
		t.Fatal("expected 'songs' key with []interface{} value")
	}
	if len(songs) != 0 {
		t.Errorf("expected empty songs, got %d", len(songs))
	}
}

func TestGetNeteaseFallbacks(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	t.Setenv("RRT_NETEASE_API_ENDPOINT", "")

	t.Run("GetNeteaseLoginStatus fallback", func(t *testing.T) {
		result, err := s.GetNeteaseLoginStatus()
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if result["profile"] != nil {
			t.Error("expected nil profile when not configured")
		}
	})

	t.Run("GetNeteaseUserAccount fallback", func(t *testing.T) {
		result, err := s.GetNeteaseUserAccount()
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if result["account"] != nil {
			t.Error("expected nil account when not configured")
		}
	})

	t.Run("GetNeteaseRecommend fallback", func(t *testing.T) {
		result, err := s.GetNeteaseRecommend()
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatal("expected 'data' key with map value")
		}
		if _, exists := data["dailySongs"]; !exists {
			t.Error("expected 'dailySongs' key in fallback")
		}
	})

	t.Run("GetNeteasePlaylists fallback", func(t *testing.T) {
		result, err := s.GetNeteasePlaylists("")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if _, exists := result["playlist"]; !exists {
			t.Error("expected 'playlist' key in fallback")
		}
	})

	t.Run("GetNeteaseQRCheck fallback", func(t *testing.T) {
		result, err := s.GetNeteaseQRCheck("test-key")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		code, ok := result["code"]
		if !ok {
			t.Fatal("expected 'code' key in fallback")
		}
		// JSON unmarshals numbers as float64, but the fallback uses a literal int
		switch v := code.(type) {
		case float64:
			if v != 800 {
				t.Errorf("expected code 800, got %v", v)
			}
		case int:
			if v != 800 {
				t.Errorf("expected code 800, got %v", v)
			}
		default:
			t.Errorf("expected numeric code, got %T: %v", code, code)
		}
	})

	t.Run("GetNeteaseQRCreate fallback", func(t *testing.T) {
		result, err := s.GetNeteaseQRCreate("test-key", true)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if _, exists := result["qrimg"]; !exists {
			t.Error("expected 'qrimg' key in fallback")
		}
		if _, exists := result["qrurl"]; !exists {
			t.Error("expected 'qrurl' key in fallback")
		}
	})
}

// TestSynthesizeTTSRejectsControlChars 批次9h L14新增：验证 TTS 文本含 \r\n 控制字符时被拒绝
func TestSynthesizeTTSRejectsControlChars(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	userID, spaceID := createUserSpaceMembership(t, db)
	bot, err := svc.CreateBotToken(spaceID, nil, "TTSBot", userID)
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}

	// 预置 TTS 同意记录，使校验流程能进入文本验证阶段
	consent := model.TTSConsent{
		ID:        idgen.NextString(),
		UserID:    userID,
		Consented: true,
	}
	if err := db.Create(&consent).Error; err != nil {
		t.Fatalf("failed to create TTS consent: %v", err)
	}

	tests := []struct {
		name  string
		text  string
		want  bool // true 表示期望被拒绝（返回错误）
		match string
	}{
		{"contains CR", "hello\rworld", true, "control characters"},
		{"contains LF", "hello\nworld", true, "control characters"},
		{"contains CRLF", "hello\r\nworld", true, "control characters"},
		{"plain text accepted by validation", "hello world", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.SynthesizeTTS(userID, "testuser", bot.ID, tt.text, "", 1.0, 1.0, 1.0)
			if tt.want {
				if err == nil {
					t.Fatalf("expected error for text %q, got nil", tt.text)
				}
				appErr, ok := err.(*errors.AppError)
				if !ok {
					t.Fatalf("expected *errors.AppError, got %T: %v", err, err)
				}
				if appErr.Code != errors.SYSTEM_BAD_REQUEST {
					t.Errorf("expected code %s, got %s", errors.SYSTEM_BAD_REQUEST, appErr.Code)
				}
				if !strings.Contains(appErr.Details, tt.match) {
					t.Errorf("expected details to contain %q, got %q", tt.match, appErr.Details)
				}
			} else {
				// 对于通过文本校验的输入，后续会尝试执行 edge-tts 命令；
				// 测试环境未安装 edge-tts 时会返回内部错误，这里只验证不是参数错误即可
				if err != nil {
					appErr, ok := err.(*errors.AppError)
					if ok && appErr.Code == errors.SYSTEM_BAD_REQUEST {
						t.Fatalf("did not expect bad request for text %q, got: %v", tt.text, err)
					}
				}
			}
		})
	}
}
