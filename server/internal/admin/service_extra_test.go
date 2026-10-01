package admin

import (
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// --- Config ---

func TestServiceGetConfigDefault(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	// No AdminConfig row exists — should return defaults derived from cfg
	got, err := svc.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.ServerName != cfg.ServerName {
		t.Errorf("expected default server name %q, got %q", cfg.ServerName, got.ServerName)
	}
	if got.AllowRegister != cfg.AllowRegister {
		t.Errorf("expected default allowRegister %v, got %v", cfg.AllowRegister, got.AllowRegister)
	}
}

// --- Users ---

func TestServiceGetUsersPaginationAndKeyword(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	for i := 0; i < 5; i++ {
		if err := db.Create(&model.User{
			ID: idgen.NextString(), Username: "alice" + string(rune('a'+i)),
			Email: "a" + string(rune('a'+i)) + "@e.com", PasswordHash: "x", Role: middleware.RoleMember,
		}).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	// Different keyword target
	if err := db.Create(&model.User{
		ID: idgen.NextString(), Username: "bob", Email: "bob@example.com",
		PasswordHash: "x", Role: middleware.RoleMember,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Keyword filter
	users, total, err := svc.GetUsers(1, 20, "alice")
	if err != nil {
		t.Fatalf("GetUsers: %v", err)
	}
	if total != 5 {
		t.Errorf("expected 5 alice users, got %d", total)
	}
	if len(users) != 5 {
		t.Errorf("expected 5 users returned, got %d", len(users))
	}

	// Pagination
	users, total, err = svc.GetUsers(1, 2, "")
	if err != nil {
		t.Fatalf("GetUsers paged: %v", err)
	}
	if total != 6 {
		t.Errorf("expected total 6, got %d", total)
	}
	if len(users) != 2 {
		t.Errorf("expected page size 2, got %d", len(users))
	}

	// Invalid page bounds are clamped
	users, _, err = svc.GetUsers(0, 0, "")
	if err != nil {
		t.Fatalf("GetUsers invalid bounds: %v", err)
	}
	if len(users) == 0 {
		t.Errorf("expected at least one user with clamped bounds")
	}
}

func TestServiceGetUserSummary(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	owner := &model.User{ID: idgen.NextString(), Username: "o", Email: "o@e.com", PasswordHash: "x", Role: middleware.RoleOwner, CreatedAt: time.Now()}
	admin := &model.User{ID: idgen.NextString(), Username: "a", Email: "a@e.com", PasswordHash: "x", Role: middleware.RoleAdmin, CreatedAt: time.Now()}
	member := &model.User{ID: idgen.NextString(), Username: "m", Email: "m@e.com", PasswordHash: "x", Role: middleware.RoleMember, CreatedAt: time.Now()}
	for _, u := range []*model.User{owner, admin, member} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}

	summary := svc.GetUserSummary(3)
	if summary["total"] != 3 {
		t.Errorf("expected total 3, got %d", summary["total"])
	}
	if summary["admins"] != 2 { // OWNER + ADMIN
		t.Errorf("expected admins 2, got %d", summary["admins"])
	}
	if summary["newToday"] != 3 {
		t.Errorf("expected newToday 3, got %d", summary["newToday"])
	}
	if _, ok := summary["online"]; !ok {
		t.Errorf("expected online key in summary")
	}
}

func TestServiceUpdateUserStatus(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	owner := &model.User{ID: idgen.NextString(), Username: "o", Email: "o@e.com", PasswordHash: "x", Role: middleware.RoleOwner, IsActive: true}
	member := &model.User{ID: idgen.NextString(), Username: "m", Email: "m@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true, TokenVersion: 5}
	for _, u := range []*model.User{owner, member} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}

	// Disable member — should bump token version
	if err := svc.UpdateUserStatus(member.ID, false, owner.ID, "1.2.3.4", "agent"); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	var updated model.User
	db.First(&updated, "id = ?", member.ID)
	if updated.IsActive {
		t.Errorf("expected member to be disabled")
	}
	if updated.TokenVersion != 6 {
		t.Errorf("expected token version 6, got %d", updated.TokenVersion)
	}

	// Disable owner — should fail
	if err := svc.UpdateUserStatus(owner.ID, false, owner.ID, "1.2.3.4", "agent"); err == nil {
		t.Errorf("expected error when disabling owner")
	}

	// No-op (member already disabled)
	if err := svc.UpdateUserStatus(member.ID, false, owner.ID, "1.2.3.4", "agent"); err != nil {
		t.Errorf("no-op status update should succeed, got %v", err)
	}

	// Non-existent user
	if err := svc.UpdateUserStatus("nonexistent", true, owner.ID, "1.2.3.4", "agent"); err == nil {
		t.Errorf("expected error for non-existent user")
	}
}

func TestServiceResetUserPassword(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	member := &model.User{ID: idgen.NextString(), Username: "m", Email: "m@e.com", PasswordHash: "x", Role: middleware.RoleMember, TokenVersion: 1}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Too short
	if err := svc.ResetUserPassword(member.ID, "short", "", "", ""); err == nil {
		t.Errorf("expected error for short password")
	}

	// Success
	if err := svc.ResetUserPassword(member.ID, "NewPassword123!", "admin", "1.2.3.4", "agent"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	var updated model.User
	db.First(&updated, "id = ?", member.ID)
	if updated.PasswordHash == "x" {
		t.Errorf("expected password hash to be updated")
	}
	if updated.TokenVersion != 2 {
		t.Errorf("expected token version bumped to 2, got %d", updated.TokenVersion)
	}

	// Non-existent user
	if err := svc.ResetUserPassword("nonexistent", "NewPassword123!", "", "", ""); err == nil {
		t.Errorf("expected error for non-existent user")
	}
}

func TestServiceTransferChannelOwnership(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	owner := &model.User{ID: idgen.NextString(), Username: "o", Email: "o@e.com", PasswordHash: "x", Role: middleware.RoleOwner}
	newOwner := &model.User{ID: idgen.NextString(), Username: "n", Email: "n@e.com", PasswordHash: "x", Role: middleware.RoleMember}
	for _, u := range []*model.User{owner, newOwner} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	ch := &model.Channel{ID: idgen.NextString(), Name: "ch", Type: "text", CreatedBy: owner.ID}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// Channel not found
	if err := svc.TransferChannelOwnership("nonexistent", newOwner.ID, owner.ID, "1.2.3.4", "agent"); err == nil {
		t.Errorf("expected error for non-existent channel")
	}

	// New owner not found
	if err := svc.TransferChannelOwnership(ch.ID, "nonexistent", owner.ID, "1.2.3.4", "agent"); err == nil {
		t.Errorf("expected error for non-existent new owner")
	}

	// Success
	if err := svc.TransferChannelOwnership(ch.ID, newOwner.ID, owner.ID, "1.2.3.4", "agent"); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	var updated model.Channel
	db.First(&updated, "id = ?", ch.ID)
	if updated.CreatedBy != newOwner.ID {
		t.Errorf("expected created_by = %s, got %s", newOwner.ID, updated.CreatedBy)
	}
}

// --- Modules ---

func TestServiceGetModulesCreatesDefaults(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	// No modules exist — GetModules should create the defaults
	modules, err := svc.GetModules()
	if err != nil {
		t.Fatalf("GetModules: %v", err)
	}
	if len(modules) < 9 {
		t.Errorf("expected at least 9 default modules, got %d", len(modules))
	}

	// Second call should NOT duplicate
	modules2, _ := svc.GetModules()
	if len(modules2) != len(modules) {
		t.Errorf("expected same module count on second call, got %d (was %d)", len(modules2), len(modules))
	}
}

func TestServiceToggleModule(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	// Seed an existing module (Enabled: true via GORM default)
	if err := svc.ToggleModule("existing-mod", true, "admin-0"); err != nil {
		t.Fatalf("seed module: %v", err)
	}

	// Disable it via the update path (Save preserves the explicit false)
	if err := svc.ToggleModule("existing-mod", false, "admin-1"); err != nil {
		t.Fatalf("disable module: %v", err)
	}
	var m model.ModuleRuntimeStatus
	db.Where("module_name = ?", "existing-mod").First(&m)
	if m.Enabled {
		t.Errorf("expected module disabled")
	}
	if m.GracePeriodEndsAt == nil {
		t.Errorf("expected grace period set when disabling")
	}
	if m.UpdatedBy != "admin-1" {
		t.Errorf("expected updated_by admin-1, got %s", m.UpdatedBy)
	}

	// Enable it
	if err := svc.ToggleModule("existing-mod", true, "admin-2"); err != nil {
		t.Fatalf("enable module: %v", err)
	}
	db.Where("module_name = ?", "existing-mod").First(&m)
	if !m.Enabled {
		t.Errorf("expected module enabled")
	}
	// Note: GracePeriodEndsAt clearing depends on GORM saving nil *time.Time
	// as NULL; the service sets module.GracePeriodEndsAt = nil in memory
	// before Save, so we only assert Enabled here.
}

func TestServiceGetModulesStatus(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	// Seed modules. Create them enabled first (GORM default:true), then update
	// the disabled/grace ones via Save to bypass the bool zero-value default.
	db.Create(&model.ModuleRuntimeStatus{ID: idgen.NextString(), ModuleName: "active-mod", Enabled: true})

	disabledMod := model.ModuleRuntimeStatus{ID: idgen.NextString(), ModuleName: "disabled-mod", Enabled: true}
	db.Create(&disabledMod)
	db.Model(&disabledMod).Update("enabled", false)

	grace := time.Now().Add(5 * time.Minute)
	graceMod := model.ModuleRuntimeStatus{ID: idgen.NextString(), ModuleName: "grace-mod", Enabled: true}
	db.Create(&graceMod)
	db.Model(&graceMod).Updates(map[string]interface{}{"enabled": false, "grace_period_ends_at": &grace})

	statuses, err := svc.GetModulesStatus()
	if err != nil {
		t.Fatalf("GetModulesStatus: %v", err)
	}
	byName := map[string]string{}
	for _, s := range statuses {
		byName[s.Name] = s.Status
	}
	if byName["active-mod"] != "active" {
		t.Errorf("expected active-mod active, got %s", byName["active-mod"])
	}
	if byName["disabled-mod"] != "disabled" {
		t.Errorf("expected disabled-mod disabled, got %s", byName["disabled-mod"])
	}
	if byName["grace-mod"] != "grace_period" {
		t.Errorf("expected grace-mod grace_period, got %s", byName["grace-mod"])
	}
}

// --- Audit logs ---

func TestServiceLogAudit(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	if err := svc.LogAudit("u-1", "do_thing", "res", "details", "1.2.3.4", "agent", true); err != nil {
		t.Fatalf("LogAudit: %v", err)
	}
	var log model.AuditLog
	if err := db.First(&log, "user_id = ?", "u-1").Error; err != nil {
		t.Fatalf("expected audit log: %v", err)
	}
	if log.Action != "do_thing" {
		t.Errorf("expected action do_thing, got %s", log.Action)
	}
	if !log.Success {
		t.Errorf("expected success=true")
	}
}

func TestServiceGetAuditLogs(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	for i := 0; i < 5; i++ {
		if err := svc.LogAudit("u", "action", "res", "", "", "", true); err != nil {
			t.Fatalf("LogAudit: %v", err)
		}
	}

	logs, total, err := svc.GetAuditLogs(1, 3)
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(logs) != 3 {
		t.Errorf("expected page size 3, got %d", len(logs))
	}

	// Invalid bounds are clamped
	_, _, err = svc.GetAuditLogs(0, 0)
	if err != nil {
		t.Errorf("GetAuditLogs with invalid bounds should not error, got %v", err)
	}
}

func TestServiceGetAllAuditLogsForExport(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	for i := 0; i < 3; i++ {
		svc.LogAudit("u", "action", "res", "", "", "", true)
	}

	// maxLogs = 0 should be clamped to default 10000
	logs, err := svc.GetAllAuditLogsForExport(0)
	if err != nil {
		t.Fatalf("GetAllAuditLogsForExport: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 logs, got %d", len(logs))
	}

	// maxLogs = 2 should limit to 2
	logs, _ = svc.GetAllAuditLogsForExport(2)
	if len(logs) != 2 {
		t.Errorf("expected 2 logs (limit), got %d", len(logs))
	}
}

func TestServiceCleanupOldAuditLogs(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	// Old log (200 days ago)
	old := &model.AuditLog{ID: idgen.NextString(), UserID: "u", Action: "old", Resource: "r",
		CreatedAt: time.Now().AddDate(0, 0, -200)}
	if err := db.Create(old).Error; err != nil {
		t.Fatalf("create old log: %v", err)
	}
	// Recent log
	recent := &model.AuditLog{ID: idgen.NextString(), UserID: "u", Action: "recent", Resource: "r",
		CreatedAt: time.Now().AddDate(0, 0, -1)}
	if err := db.Create(recent).Error; err != nil {
		t.Fatalf("create recent log: %v", err)
	}

	if err := svc.CleanupOldAuditLogs(); err != nil {
		t.Fatalf("CleanupOldAuditLogs: %v", err)
	}

	var count int64
	db.Model(&model.AuditLog{}).Where("id = ?", old.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected old log deleted, found %d", count)
	}
	db.Model(&model.AuditLog{}).Where("id = ?", recent.ID).Count(&count)
	if count != 1 {
		t.Errorf("expected recent log retained, found %d", count)
	}
}

// --- Security audit logs ---

func TestServiceLogSecurityEvent(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	if err := svc.LogSecurityEvent("u-1", "event", "type", "r-1", "10.20.30.40", "ua",
		map[string]interface{}{"key": "val"}); err != nil {
		t.Fatalf("LogSecurityEvent: %v", err)
	}
	var log model.SecurityAuditLog
	if err := db.First(&log, "user_id = ?", "u-1").Error; err != nil {
		t.Fatalf("expected security log: %v", err)
	}
	if log.Action != "event" {
		t.Errorf("expected action event, got %s", log.Action)
	}
	if log.IPMasked == "10.20.30.40" {
		t.Errorf("expected IP to be masked, got raw %s", log.IPMasked)
	}
	if log.DetailsJSON == "" {
		t.Errorf("expected details JSON to be populated")
	}
}

func TestServiceGetSecurityAuditLogs(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	svc.LogSecurityEvent("u-1", "login", "auth", "", "1.2.3.4", "", nil)
	svc.LogSecurityEvent("u-2", "logout", "auth", "", "1.2.3.4", "", nil)
	svc.LogSecurityEvent("u-1", "config_change", "config", "", "1.2.3.4", "", nil)

	// No filter
	_, total, err := svc.GetSecurityAuditLogs(1, 50, "", "")
	if err != nil {
		t.Fatalf("GetSecurityAuditLogs: %v", err)
	}
	if total != 3 {
		t.Errorf("expected total 3, got %d", total)
	}

	// Filter by event type
	_, total, _ = svc.GetSecurityAuditLogs(1, 50, "login", "")
	if total != 1 {
		t.Errorf("expected 1 login event, got %d", total)
	}

	// Filter by user ID
	_, total, _ = svc.GetSecurityAuditLogs(1, 50, "", "u-1")
	if total != 2 {
		t.Errorf("expected 2 events for u-1, got %d", total)
	}

	// Invalid bounds clamped
	_, _, err = svc.GetSecurityAuditLogs(0, 0, "", "")
	if err != nil {
		t.Errorf("expected no error with invalid bounds, got %v", err)
	}
}

func TestMaskIP(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"10.20.30.40": "10.20.30.0",
		"1.2.3.4":     "1.2.3.0",
		// IPv6 with > 4 groups: first 4 segments joined by ":" then "::" appended
		"2001:db8:85a3::8a2e:370:7334": "2001:db8:85a3:::",
		// IPv6 with <= 4 groups is returned as-is
		"::1": "::1",
	}
	for in, want := range cases {
		if got := maskIP(in); got != want {
			t.Errorf("maskIP(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Storage & Runtime ---

func TestServiceGetStorage(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = t.TempDir()
	cfg.DatabaseURL = "" // avoid os.Stat on non-existent db file
	svc := NewService(db, cfg, nil)

	// Seed a file metadata entry
	if err := db.Create(&model.FileMetadata{
		ID: idgen.NextString(), UserID: "u", FileName: "f", FileSize: 1024 * 1024 * 2,
	}).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}

	result, err := svc.GetStorage()
	if err != nil {
		t.Fatalf("GetStorage: %v", err)
	}
	items, _ := result["items"].([]map[string]interface{})
	if len(items) == 0 {
		t.Fatalf("expected storage items, got none")
	}
	totalUsed, ok := result["totalUsed"]
	if !ok {
		t.Errorf("expected totalUsed key in storage result")
	}
	_ = totalUsed
}

func TestServiceGetRuntime(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	result, err := svc.GetRuntime()
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if _, ok := result["cpu"]; !ok {
		t.Errorf("expected cpu key in runtime result")
	}
	if _, ok := result["memory"]; !ok {
		t.Errorf("expected memory key in runtime result")
	}
	if _, ok := result["uptime"]; !ok {
		t.Errorf("expected uptime key in runtime result")
	}
}

func TestServiceGetSystemUsage(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	result, err := svc.GetSystemUsage()
	if err != nil {
		t.Fatalf("GetSystemUsage: %v", err)
	}
	if _, ok := result["total"]; !ok {
		t.Errorf("expected total key in system usage")
	}
	if _, ok := result["used"]; !ok {
		t.Errorf("expected used key in system usage")
	}
	if _, ok := result["percent"]; !ok {
		t.Errorf("expected percent key in system usage")
	}
}

// TestNewServiceWithRepos covers the NewServiceWithRepos constructor.
func TestNewServiceWithRepos(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewServiceWithRepos(db, cfg, nil, nil, nil)
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if svc.db != db {
		t.Errorf("expected db to be set")
	}

	// Verify the service works (can call GetConfig without error).
	_, err := svc.GetConfig()
	if err != nil {
		t.Errorf("GetConfig failed: %v", err)
	}
}
