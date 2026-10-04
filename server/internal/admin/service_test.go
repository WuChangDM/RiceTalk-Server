package admin

import (
	"testing"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func TestServiceUpdateConfig_Allowlist(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	// Unknown field should be rejected
	err := svc.UpdateConfig(map[string]interface{}{
		"server_name": "ok",
		"hacked":      true,
	})
	if err == nil {
		t.Fatal("expected error for unknown config field")
	}
	appErr, ok := err.(*errors.AppError)
	if !ok || appErr.Code != errors.SYSTEM_BAD_REQUEST {
		t.Fatalf("expected SYSTEM_BAD_REQUEST, got %v", err)
	}

	// Known fields should succeed
	if err := svc.UpdateConfig(map[string]interface{}{
		"server_name":    "RidgeRiceTalk Test",
		"allow_register": false,
		"max_users":      42,
	}); err != nil {
		t.Fatalf("expected update to succeed, got %v", err)
	}

	c, err := svc.GetConfigRaw()
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if c.ServerName != "RidgeRiceTalk Test" {
		t.Errorf("ServerName = %q, want RidgeRiceTalk Test", c.ServerName)
	}
	if c.AllowRegister != false {
		t.Errorf("AllowRegister = %v, want false", c.AllowRegister)
	}
	if c.MaxUsers != 42 {
		t.Errorf("MaxUsers = %d, want 42", c.MaxUsers)
	}
}

func TestServiceUpdateUserRole(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", Role: "OWNER", PasswordHash: "x"}
	admin := &model.User{ID: idgen.NextString(), Username: "admin", Email: "admin@example.com", Role: "ADMIN", PasswordHash: "x"}
	member := &model.User{ID: idgen.NextString(), Username: "member", Email: "member@example.com", Role: "MEMBER", PasswordHash: "x"}
	for _, u := range []*model.User{owner, admin, member} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}

	// Promote member to admin
	if err := svc.UpdateUserRole(member.ID, "ADMIN", owner.ID, "127.0.0.1", "test-agent"); err != nil {
		t.Fatalf("update role: %v", err)
	}
	var updated model.User
	if err := db.First(&updated, "id = ?", member.ID).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	if updated.Role != "ADMIN" {
		t.Errorf("role = %q, want ADMIN", updated.Role)
	}

	// Invalid role
	if err := svc.UpdateUserRole(member.ID, "HACKER", owner.ID, "127.0.0.1", "test-agent"); err == nil {
		t.Fatal("expected error for invalid role")
	}

	// Cannot demote last owner
	if err := svc.UpdateUserRole(owner.ID, "ADMIN", owner.ID, "127.0.0.1", "test-agent"); err == nil {
		t.Fatal("expected error when demoting last owner")
	}
}
