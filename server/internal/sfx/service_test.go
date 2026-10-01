package sfx

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// newTestService builds a Service with an in-memory DB and a temp LocalDataPath.
func newTestService(t *testing.T, maxPerUser int) *Service {
	t.Helper()
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{
		LocalDataPath: t.TempDir(),
		SfxMaxPerUser: maxPerUser,
	}
	svc := NewService(db, cfg, nil)
	return svc
}

// validWav returns a minimal valid WAV header (RIFF....WAVE) padded with data.
func validWav(n int) []byte {
	b := []byte("RIFF")
	b = append(b, 0x24, 0x00, 0x00, 0x00) // chunk size
	b = append(b, []byte("WAVE")...)
	b = append(b, []byte("fmt ")...)
	b = append(b, bytes.Repeat([]byte{0}, 32)...)
	for len(b) < n {
		b = append(b, 0)
	}
	return b[:n]
}

func TestUploadSound_Success(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(2048)
	sound, err := svc.UploadSound("user1", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("UploadSound failed: %v", err)
	}
	if sound.ID == "" || sound.UserID != "user1" {
		t.Fatalf("unexpected sound: %+v", sound)
	}
	// File persisted
	abs := svc.absPath(sound.FilePath)
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("expected file on disk: %v", err)
	}
	// In library list
	list, err := svc.ListSounds("user1")
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 sound in library, got %d (%v)", len(list), err)
	}
}

func TestUploadSound_RejectsOversize(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(MaxSoundFileSize + 1)
	_, err := svc.UploadSound("user1", "big.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	assertCode(t, err, errors.FILE_TOO_LARGE)
}

func TestUploadSound_RejectsBadType(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(1024)
	_, err := svc.UploadSound("user1", "song.flac", "audio/flac", int64(len(data)), bytes.NewReader(data))
	assertCode(t, err, errors.FILE_INVALID_TYPE)
}

func TestUploadSound_RejectsBadMagic(t *testing.T) {
	svc := newTestService(t, 20)
	data := []byte("not an audio file at all, just text........")
	_, err := svc.UploadSound("user1", "fake.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	assertCode(t, err, errors.FILE_INVALID_TYPE)
}

func TestUploadSound_Quota(t *testing.T) {
	svc := newTestService(t, 2)
	for i := 0; i < 2; i++ {
		data := validWav(512)
		if _, err := svc.UploadSound("user1", "s"+string(rune('a'+i))+".wav", "audio/wav", int64(len(data)), bytes.NewReader(data)); err != nil {
			t.Fatalf("upload %d failed: %v", i, err)
		}
	}
	data := validWav(512)
	_, err := svc.UploadSound("user1", "third.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	assertCode(t, err, errors.SYSTEM_BAD_REQUEST)
}

func TestDeleteSound_OnlyUploader(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(512)
	sound, err := svc.UploadSound("user1", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	// Another user cannot delete
	err = svc.DeleteSound("user2", sound.ID)
	assertCode(t, err, errors.FILE_NOT_FOUND)
}

func TestDeleteSound_ClearsBindings(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(512)
	sound, err := svc.UploadSound("user1", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	// user2 binds it as join sound
	joinID := sound.ID
	if _, err := svc.UpdateSettings("user2", SettingsInput{JoinSoundID: &joinID}); err != nil {
		t.Fatalf("bind failed: %v", err)
	}
	// user1 (uploader) deletes it
	if err := svc.DeleteSound("user1", sound.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	// user2's join binding auto-cleared
	st, err := svc.GetSettings("user2")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if st.JoinSoundID != nil {
		t.Fatalf("expected join binding cleared, got %v", *st.JoinSoundID)
	}
	// deleted sound no longer in library
	list, _ := svc.ListSounds("user1")
	if len(list) != 0 {
		t.Fatalf("expected empty library after delete, got %d", len(list))
	}
}

func TestGetSettings_Default(t *testing.T) {
	svc := newTestService(t, 20)
	st, err := svc.GetSettings("nobody")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if !st.Enabled || st.JoinVolume != 100 || st.LeaveVolume != 100 || st.JoinSoundID != nil {
		t.Fatalf("unexpected defaults: %+v", st)
	}
}

func TestUpdateSettings_ClampAndBindValidation(t *testing.T) {
	svc := newTestService(t, 20)
	// Clamp volume to 0-200
	jv := 999
	st, err := svc.UpdateSettings("user1", SettingsInput{JoinVolume: &jv})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if st.JoinVolume != 200 {
		t.Fatalf("expected clamp to 200, got %d", st.JoinVolume)
	}
	// Binding a nonexistent sound rejected
	bad := "nonexistent-sound-id"
	_, err = svc.UpdateSettings("user1", SettingsInput{JoinSoundID: &bad})
	assertCode(t, err, errors.FILE_NOT_FOUND)
}

func TestSoundFileAbsPath_TraversalGuard(t *testing.T) {
	svc := newTestService(t, 20)
	// Plant a record whose relative path escapes LocalDataPath
	sound := model.UserSound{
		ID:       idgen.NextString(),
		UserID:   "user1",
		Title:    "evil",
		FilePath: filepath.Join("..", "..", "outside.wav"),
	}
	if err := svc.db.Create(&sound).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	_, _, err := svc.SoundFileAbsPath(sound.ID)
	assertCode(t, err, errors.AUTH_FORBIDDEN)
}

func assertCode(t *testing.T, err error, code errors.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", code)
	}
	appErr, ok := err.(*errors.AppError)
	if !ok {
		t.Fatalf("expected AppError %s, got %T: %v", code, err, err)
	}
	if appErr.Code != code {
		t.Fatalf("expected code %s, got %s (%v)", code, appErr.Code, err)
	}
}

func TestSeedPresets_Idempotent(t *testing.T) {
	svc := newTestService(t, 20)
	svc.SeedPresets()
	list, err := svc.ListSounds("user1")
	if err != nil {
		t.Fatalf("ListSounds failed: %v", err)
	}
	if len(list) != len(presets) {
		t.Fatalf("expected %d presets, got %d", len(presets), len(list))
	}
	for _, it := range list {
		if !it.IsPreset {
			t.Fatalf("expected IsPreset=true for %s", it.ID)
		}
		if it.UploaderName != "软件自带" {
			t.Fatalf("expected uploader 软件自带, got %q", it.UploaderName)
		}
	}
	// Re-seed is idempotent (no duplicates).
	svc.SeedPresets()
	list2, _ := svc.ListSounds("user1")
	if len(list2) != len(presets) {
		t.Fatalf("expected idempotent seed, got %d after re-seed", len(list2))
	}
}

func TestDeleteSound_PresetForbidden(t *testing.T) {
	svc := newTestService(t, 20)
	svc.SeedPresets()
	err := svc.DeleteSound("user1", presets[0].id)
	assertCode(t, err, errors.AUTH_FORBIDDEN)
	// Preset still present.
	list, _ := svc.ListSounds("user1")
	if len(list) != len(presets) {
		t.Fatalf("preset should survive delete attempt, got %d", len(list))
	}
}

func TestFavorite_Unfavorite(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(512)
	sound, err := svc.UploadSound("user1", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	// user2 favorites it
	if err := svc.FavoriteSound("user2", sound.ID); err != nil {
		t.Fatalf("favorite failed: %v", err)
	}
	// idempotent
	if err := svc.FavoriteSound("user2", sound.ID); err != nil {
		t.Fatalf("re-favorite failed: %v", err)
	}
	list, _ := svc.ListSounds("user2")
	if len(list) != 1 || !list[0].Favorited {
		t.Fatalf("expected favorited=true for user2, got %+v", list)
	}
	// user1 has not favorited
	list1, _ := svc.ListSounds("user1")
	if len(list1) != 1 || list1[0].Favorited {
		t.Fatalf("expected favorited=false for user1, got %+v", list1)
	}
	// unfavorite
	if err := svc.UnfavoriteSound("user2", sound.ID); err != nil {
		t.Fatalf("unfavorite failed: %v", err)
	}
	list2, _ := svc.ListSounds("user2")
	if len(list2) != 1 || list2[0].Favorited {
		t.Fatalf("expected favorited=false after unfavorite, got %+v", list2)
	}
	// favorite nonexistent sound rejected
	if err := svc.FavoriteSound("user2", "no-such-id"); err == nil {
		t.Fatalf("expected error favoriting nonexistent sound")
	}
}

func TestListSounds_UploaderNameAndOrder(t *testing.T) {
	svc := newTestService(t, 20)
	// seed a real user so uploader name resolves
	u := model.User{ID: "user9", Username: "alice", Email: "a@x.local", DisplayName: "爱丽丝"}
	if err := svc.db.Create(&u).Error; err != nil {
		t.Fatalf("seed user failed: %v", err)
	}
	data := validWav(512)
	if _, err := svc.UploadSound("user9", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	svc.SeedPresets()
	list, _ := svc.ListSounds("user1")
	// presets come first
	if !list[0].IsPreset {
		t.Fatalf("expected presets ordered first, first=%s", list[0].ID)
	}
	// last is the uploaded one, with display name
	last := list[len(list)-1]
	if last.IsPreset {
		t.Fatalf("expected uploaded sound last")
	}
	if last.UploaderName != "爱丽丝" {
		t.Fatalf("expected uploader display name 爱丽丝, got %q", last.UploaderName)
	}
}

// TestPresetAssetsAreRealAudio guards against a placeholder or truncated file being
// embedded: every preset must be a real MP3 of a plausible size for a short chime.
func TestPresetAssetsAreRealAudio(t *testing.T) {
	for _, p := range presets {
		if len(p.data) < 2000 {
			t.Errorf("%s: embedded audio is only %d bytes", p.id, len(p.data))
		}
		if !hasMPEGFrameSync(p.data) {
			t.Errorf("%s: no MPEG frame sync in the first 4KB (not an MP3?)", p.id)
		}
		if p.title == "" || p.filename == "" || p.duration <= 0 {
			t.Errorf("%s: incomplete catalog entry %+v", p.id, p)
		}
	}
}

// hasMPEGFrameSync reports whether data contains an MPEG audio frame header. The
// scan skips any leading ID3v2 tag rather than assuming the file starts at a frame.
func hasMPEGFrameSync(data []byte) bool {
	limit := len(data)
	if limit > 4096 {
		limit = 4096
	}
	for i := 0; i+1 < limit; i++ {
		if data[i] == 0xFF && data[i+1]&0xE0 == 0xE0 {
			return true
		}
	}
	return false
}

// TestSeedPresets_RefreshesOutdatedRow covers a pack revision: an already-seeded row
// whose title/duration/file drifted must be brought back in line (not skipped, which
// is what the old seed-if-missing logic did).
func TestSeedPresets_RefreshesOutdatedRow(t *testing.T) {
	svc := newTestService(t, 20)
	svc.SeedPresets()

	target := presets[0]
	dir := filepath.Join(svc.cfg.LocalDataPath, "uploads", "sounds", "presets")
	abs := filepath.Join(dir, target.filename)

	// Simulate a stale row from an older pack plus a stale file on disk.
	if err := os.WriteFile(abs, []byte("stale"), 0644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := svc.db.Model(&model.UserSound{}).Where("id = ?", target.id).
		Updates(map[string]any{"title": "旧标题", "duration": 9}).Error; err != nil {
		t.Fatalf("age row: %v", err)
	}

	svc.SeedPresets()

	var got model.UserSound
	if err := svc.db.Where("id = ?", target.id).First(&got).Error; err != nil {
		t.Fatalf("preset missing after re-seed: %v", err)
	}
	if got.Title != target.title || got.Duration != target.duration {
		t.Fatalf("row not refreshed: title=%q duration=%d", got.Title, got.Duration)
	}
	if onDisk, err := os.ReadFile(abs); err != nil || !bytes.Equal(onDisk, target.data) {
		t.Fatalf("stale file not rewritten (%d bytes, err=%v)", len(onDisk), err)
	}
}

// TestSeedPresets_PrunesRetiredPreset covers the case this change had to handle:
// presets that are no longer part of the catalog must disappear from the library
// along with every binding and favorite that pointed at them.
func TestSeedPresets_PrunesRetiredPreset(t *testing.T) {
	svc := newTestService(t, 20)
	svc.SeedPresets()

	retiredID := "preset-retired-from-old-pack"
	dir := filepath.Join(svc.cfg.LocalDataPath, "uploads", "sounds", "presets")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	retiredPath := filepath.Join(dir, "retired.mp3")
	if err := os.WriteFile(retiredPath, []byte("old sound"), 0644); err != nil {
		t.Fatalf("write retired file: %v", err)
	}
	rel, _ := filepath.Rel(svc.cfg.LocalDataPath, retiredPath)
	row := model.UserSound{
		ID: retiredID, UserID: presetUserID, Title: "退役提示音",
		FilePath: rel, FileSize: 9, MimeType: "audio/mpeg", Duration: 1,
		IsPreset: true, CreatedAt: time.Now(),
	}
	if err := svc.db.Create(&row).Error; err != nil {
		t.Fatalf("seed retired preset: %v", err)
	}
	// A user bound it as both join and leave sound and favorited it.
	if _, err := svc.UpdateSettings("user1", SettingsInput{
		JoinSoundID: &retiredID, LeaveSoundID: &retiredID,
	}); err != nil {
		t.Fatalf("bind retired preset: %v", err)
	}
	if err := svc.FavoriteSound("user1", retiredID); err != nil {
		t.Fatalf("favorite retired preset: %v", err)
	}

	svc.SeedPresets()

	var count int64
	if err := svc.db.Unscoped().Model(&model.UserSound{}).Where("id = ?", retiredID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("retired preset row survived pruning (count=%d)", count)
	}
	var favCount int64
	if err := svc.db.Model(&model.UserSoundFavorite{}).Where("sound_id = ?", retiredID).Count(&favCount).Error; err != nil {
		t.Fatalf("count favorites: %v", err)
	}
	if favCount != 0 {
		t.Fatalf("favorite of retired preset survived pruning")
	}
	st, err := svc.GetSettings("user1")
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if st.JoinSoundID != nil || st.LeaveSoundID != nil {
		t.Fatalf("dangling binding left behind: join=%v leave=%v", st.JoinSoundID, st.LeaveSoundID)
	}
	if _, err := os.Stat(retiredPath); !os.IsNotExist(err) {
		t.Fatalf("retired preset file not removed (err=%v)", err)
	}
	// The current catalog is untouched.
	list, _ := svc.ListSounds("user1")
	if len(list) != len(presets) {
		t.Fatalf("expected %d presets after prune, got %d", len(presets), len(list))
	}
}

// TestSeedPresets_DisplayOrder pins the order users actually see: ListSounds sorts
// presets by created_at DESC, so seeding has to stamp descending timestamps.
func TestSeedPresets_DisplayOrder(t *testing.T) {
	svc := newTestService(t, 20)
	svc.SeedPresets()
	list, err := svc.ListSounds("user1")
	if err != nil {
		t.Fatalf("ListSounds: %v", err)
	}
	var got []string
	for _, it := range list {
		if it.IsPreset {
			got = append(got, it.ID)
		}
	}
	if len(got) != len(presets) {
		t.Fatalf("expected %d presets, got %d", len(presets), len(got))
	}
	for i, p := range presets {
		if got[i] != p.id {
			t.Fatalf("display order mismatch at %d: got %s, want %s", i, got[i], p.id)
		}
	}
}
