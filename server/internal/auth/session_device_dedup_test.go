package auth

// task-131：同一台设备重复登录不再新增「登录设备」行。
//
// 语义（Lead 拍板）：
//   - device_id 非空 → (user_id, device_id) 唯一：重登原地更新同一会话行（保留
//     会话 id 与 created_at，刷新 token/设备/网络上下文），refresh 轮换后清理同
//     设备其它行；
//   - device_id 为空串（老客户端）→ 行为与 1.9.4 之前逐字一致（每次登录新行）；
//   - 迁移 000048 对历史行按「用户+设备类型+设备名+完整 IP」四元组合并，保留
//     last_active_at 最大的一行。
//
// 测试全部使用真 SQLite（testutil.MustSetupTestDB）+ 真 Service.Login/RefreshToken。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"gorm.io/gorm"
)

const (
	dedupDeviceID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	dedupDeviceB  = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// loginWithDeviceID 用指定安装级设备标识登录一次。
func loginWithDeviceID(t *testing.T, svc *Service, email, deviceType, deviceName, deviceID string) *TokenResponse {
	t.Helper()
	resp, err := svc.Login(&LoginRequest{
		Email:      email,
		Password:   "Password123!",
		DeviceType: deviceType,
		DeviceName: deviceName,
		DeviceID:   deviceID,
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return resp
}

// sessionsFor 读取某用户的全部会话行（按创建时间升序，便于稳定断言）。
func sessionsFor(t *testing.T, db *gorm.DB, userID string) []model.UserSession {
	t.Helper()
	var sessions []model.UserSession
	if err := db.Where("user_id = ?", userID).
		Order("created_at ASC, id ASC").Find(&sessions).Error; err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	return sessions
}

func TestLoginSameDeviceIDKeepsSingleSession(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	u := newDeviceTestUser(t, db, "dedup-same@example.com")

	first := loginWithDeviceID(t, svc, "dedup-same@example.com", "windows-desktop", "DEV-PC", dedupDeviceID)
	firstSessionID := sessionIDFromToken(t, first.RefreshToken)
	before := sessionsFor(t, db, u.ID)
	if len(before) != 1 {
		t.Fatalf("expected 1 session after first login, got %d", len(before))
	}

	second := loginWithDeviceID(t, svc, "dedup-same@example.com", "windows-desktop", "DEV-PC", dedupDeviceID)
	after := sessionsFor(t, db, u.ID)
	if len(after) != 1 {
		t.Fatalf("same device_id must keep exactly 1 session row, got %d", len(after))
	}
	if after[0].ID != before[0].ID || after[0].ID != firstSessionID {
		t.Errorf("session id must be stable across re-login: first=%q second=%q row=%q",
			before[0].ID, firstSessionID, after[0].ID)
	}
	if !after[0].CreatedAt.Equal(before[0].CreatedAt) {
		t.Errorf("created_at must be preserved on in-place update: before=%v after=%v",
			before[0].CreatedAt, after[0].CreatedAt)
	}
	if after[0].DeviceID != dedupDeviceID {
		t.Errorf("expected device_id %q, got %q", dedupDeviceID, after[0].DeviceID)
	}
	// 第二次登录覆盖了 token_hash：第一次的 refresh token 必须失效。
	if _, err := svc.RefreshToken(first.RefreshToken, nil); err == nil {
		t.Error("first refresh token must be invalidated after re-login on the same device")
	}
	if _, err := svc.RefreshToken(second.RefreshToken, nil); err != nil {
		t.Errorf("second refresh token must be valid, got %v", err)
	}
}

func TestLoginDifferentDeviceIDsTwoSessions(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	u := newDeviceTestUser(t, db, "dedup-two@example.com")

	loginWithDeviceID(t, svc, "dedup-two@example.com", "windows-desktop", "DEV-PC", dedupDeviceID)
	loginWithDeviceID(t, svc, "dedup-two@example.com", "windows-desktop", "DEV-PC-2", dedupDeviceB)

	sessions := sessionsFor(t, db, u.ID)
	if len(sessions) != 2 {
		t.Fatalf("two distinct device_id must yield 2 session rows, got %d", len(sessions))
	}
	if sessions[0].DeviceID == sessions[1].DeviceID {
		t.Error("distinct devices must carry distinct device_id")
	}
}

func TestLoginConcurrentSameDeviceSingleSession(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	u := newDeviceTestUser(t, db, "dedup-race@example.com")

	const workers = 6
	var wg sync.WaitGroup
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			_, err := svc.Login(&LoginRequest{
				Email:      "dedup-race@example.com",
				Password:   "Password123!",
				DeviceType: "windows-desktop",
				DeviceName: "DEV-RACE",
				DeviceID:   dedupDeviceID,
			})
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent login %d failed: %v", i, err)
		}
	}
	if sessions := sessionsFor(t, db, u.ID); len(sessions) != 1 {
		t.Fatalf("concurrent logins from one device must keep 1 row, got %d", len(sessions))
	}
}

func TestRefreshKeepsSingleRowPerDevice(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	u := newDeviceTestUser(t, db, "dedup-refresh@example.com")

	resp := loginWithDeviceID(t, svc, "dedup-refresh@example.com", "windows-desktop", "DEV-PC", dedupDeviceID)
	firstSessionID := sessionIDFromToken(t, resp.RefreshToken)

	// 老客户端风格：refresh 只上报 type/name，不上报 deviceId → 继承旧行。
	refreshed, err := svc.RefreshToken(resp.RefreshToken, &SessionDevice{
		Type: "windows-desktop", Name: "DEV-PC",
	})
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if refreshed.RefreshToken == "" {
		t.Fatal("refresh must return a new refresh token")
	}
	sessions := sessionsFor(t, db, u.ID)
	if len(sessions) != 1 {
		t.Fatalf("refresh must not add session rows for the same device, got %d", len(sessions))
	}
	if sessions[0].DeviceID != dedupDeviceID {
		t.Errorf("device_id must be inherited across refresh, got %q", sessions[0].DeviceID)
	}
	if firstSessionID == "" {
		t.Error("expected non-empty session_id claim on login")
	}
	// 旧 refresh token 已被轮换消费。
	if _, err := svc.RefreshToken(resp.RefreshToken, nil); err == nil {
		t.Error("consumed refresh token must be rejected")
	}
	// 新 token 可用，且再次 refresh 后仍是一行。
	if _, err := svc.RefreshToken(refreshed.RefreshToken, &SessionDevice{
		Type: "windows-desktop", Name: "DEV-PC", DeviceID: dedupDeviceID,
	}); err != nil {
		t.Fatalf("second refresh failed: %v", err)
	}
	if sessions := sessionsFor(t, db, u.ID); len(sessions) != 1 {
		t.Fatalf("repeated refresh must keep 1 row, got %d", len(sessions))
	}
}

func TestLegacyClientWithoutDeviceIDUnchanged(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	u := newDeviceTestUser(t, db, "dedup-legacy@example.com")

	loginWithDeviceID(t, svc, "dedup-legacy@example.com", "windows-desktop", "DEV-PC", "")
	loginWithDeviceID(t, svc, "dedup-legacy@example.com", "windows-desktop", "DEV-PC", "")

	sessions := sessionsFor(t, db, u.ID)
	if len(sessions) != 2 {
		t.Fatalf("legacy clients (no deviceId) must keep pre-1.9.4 behavior: 2 rows, got %d", len(sessions))
	}
	for _, s := range sessions {
		if s.DeviceID != "" {
			t.Errorf("legacy session must have empty device_id, got %q", s.DeviceID)
		}
	}
}

// dedupeStatementsFromMigration 从 000048 up 迁移中抽取历史去重语句（UPDATE/DELETE），
// 用于在 SQLite 上验证迁移逻辑本身（PG 侧语法同样可执行）。
func dedupeStatementsFromMigration(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "migrations", "000048_user_sessions_device_id.up.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	var out []string
	for _, stmt := range strings.Split(string(raw), ";") {
		// 去掉整行注释（迁移里 UPDATE 语句前有中文说明），再判断语句类型。
		var body []string
		for _, line := range strings.Split(stmt, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			body = append(body, line)
		}
		trimmed := strings.TrimSpace(strings.Join(body, "\n"))
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "UPDATE USER_SESSIONS") ||
			strings.HasPrefix(upper, "DELETE FROM USER_SESSIONS") {
			out = append(out, trimmed)
		}
	}
	if len(out) < 2 {
		t.Fatalf("expected >=2 dedupe statements in migration, got %d", len(out))
	}
	return out
}

func TestMigrationDedupesLegacyDeviceSessions(t *testing.T) {
	db := testutil.MustSetupTestDB()
	u := newDeviceTestUser(t, db, "dedup-migration@example.com")
	base := time.Now().UTC().Add(-3 * time.Hour)

	seed := func(id string, ip string, active time.Time, deviceType, deviceName string) {
		sess := &model.UserSession{
			ID:           id,
			UserID:       u.ID,
			TokenHash:    "hash-" + id,
			ExpiresAt:    time.Now().UTC().Add(24 * time.Hour),
			DeviceType:   deviceType,
			DeviceName:   deviceName,
			IP:           ip,
			LastActiveAt: active,
			CreatedAt:    active,
		}
		if err := db.Create(sess).Error; err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
	}
	// 同一设备同一 IP 的 3 条历史重复行（oldest/mid/newest）。
	seed("sess-old", "112.231.1.17", base, "windows-desktop", "DESKTOP-RKTOG4T")
	seed("sess-mid", "112.231.1.17", base.Add(time.Hour), "windows-desktop", "DESKTOP-RKTOG4T")
	seed("sess-new", "112.231.1.17", base.Add(2*time.Hour), "windows-desktop", "DESKTOP-RKTOG4T")
	// 不同 IP / 不同设备名的行不应被合并。
	seed("sess-other-ip", "10.0.0.9", base, "windows-desktop", "DESKTOP-RKTOG4T")
	seed("sess-other-name", "112.231.1.17", base, "windows-desktop", "OTHER-PC")
	// 老客户端未上报设备名的行不参与合并（device_type/device_name 必须非空）。
	seed("sess-no-name-1", "112.231.1.17", base, "windows-desktop", "")
	seed("sess-no-name-2", "112.231.1.17", base.Add(time.Minute), "windows-desktop", "")

	for _, stmt := range dedupeStatementsFromMigration(t) {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("execute dedupe statement failed: %v\nSQL: %s", err, stmt)
		}
	}

	sessions := sessionsFor(t, db, u.ID)
	if len(sessions) != 5 {
		t.Fatalf("expected 5 rows after dedupe (3 duplicates -> 1), got %d", len(sessions))
	}
	kept := 0
	var survivor *model.UserSession
	for i := range sessions {
		if sessions[i].DeviceName == "DESKTOP-RKTOG4T" && sessions[i].IP == "112.231.1.17" {
			kept++
			survivor = &sessions[i]
		}
	}
	if kept != 1 {
		t.Fatalf("expected exactly 1 surviving duplicate group row, got %d", kept)
	}
	want := base.Add(2 * time.Hour)
	if !survivor.LastActiveAt.Equal(want) {
		t.Errorf("survivor must carry the max last_active_at %v, got %v", want, survivor.LastActiveAt)
	}
}

func TestListSessionsExposesDeviceID(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	u := newDeviceTestUser(t, db, "dedup-list@example.com")

	resp := loginWithDeviceID(t, svc, "dedup-list@example.com", "windows-desktop", "DEV-PC", dedupDeviceID)
	current := sessionIDFromToken(t, resp.RefreshToken)
	list, err := svc.ListSessions(u.ID, current)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].DeviceID != dedupDeviceID {
		t.Errorf("expected deviceId %q in session list, got %q", dedupDeviceID, list[0].DeviceID)
	}
	if !list[0].IsCurrent {
		t.Error("session created by this login must be flagged current")
	}
}
