package user

import (
	"strings"
	"testing"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"gorm.io/gorm"
)

func init() {
	_ = idgen.Init(1, 1)
}

func createTestUser(db *gorm.DB, username, email string) *model.User {
	u := &model.User{
		ID:           idgen.NextString(),
		Username:     username,
		Email:        email,
		PasswordHash: "",
		Role:         "MEMBER",
	}
	if db != nil {
		_ = db.Create(u).Error
	}
	return u
}

func TestGetUser(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		user := createTestUser(db, "testuser", "test@example.com")

		found, err := svc.GetUser(user.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found == nil {
			t.Fatal("expected user, got nil")
		}
		if found.ID != user.ID {
			t.Errorf("expected user ID %s, got %s", user.ID, found.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		_, err := svc.GetUser("999999999")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestGetUsers(t *testing.T) {
	t.Run("pagination", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		for i := 0; i < 25; i++ {
			createTestUser(db, "user"+idgen.NextString(), "user"+idgen.NextString()+"@example.com")
		}

		users, total, err := svc.GetUsers(1, 10)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if total != 25 {
			t.Errorf("expected total 25, got %d", total)
		}
		if len(users) != 10 {
			t.Errorf("expected 10 users, got %d", len(users))
		}

		users2, _, err := svc.GetUsers(2, 10)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(users2) != 10 {
			t.Errorf("expected 10 users on page 2, got %d", len(users2))
		}
	})

	t.Run("default page size", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		for i := 0; i < 25; i++ {
			createTestUser(db, "user"+idgen.NextString(), "user"+idgen.NextString()+"@example.com")
		}

		users, total, err := svc.GetUsers(1, 0)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if total != 25 {
			t.Errorf("expected total 25, got %d", total)
		}
		if len(users) != 20 {
			t.Errorf("expected default page size 20, got %d", len(users))
		}
	})
}

func TestUpdateProfile(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		user := createTestUser(db, "testuser", "test@example.com")

		err := svc.UpdateProfile(user.ID, map[string]interface{}{
			"display_name":  "New Name",
			"custom_status": "Busy",
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		updated, err := svc.GetUser(user.ID)
		if err != nil {
			t.Fatalf("expected no error getting updated user, got %v", err)
		}
		if updated.DisplayName != "New Name" {
			t.Errorf("expected display name 'New Name', got %s", updated.DisplayName)
		}
		if updated.CustomStatus != "Busy" {
			t.Errorf("expected custom status 'Busy', got %s", updated.CustomStatus)
		}
	})

	t.Run("sanitize display name", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		user := createTestUser(db, "testuser", "test@example.com")

		longName := strings.Repeat("a", 100)
		err := svc.UpdateProfile(user.ID, map[string]interface{}{
			"display_name": longName,
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		updated, err := svc.GetUser(user.ID)
		if err != nil {
			t.Fatalf("expected no error getting updated user, got %v", err)
		}
		if len(updated.DisplayName) > 64 {
			t.Errorf("expected display name to be truncated to 64, got length %d", len(updated.DisplayName))
		}
	})

	t.Run("display name unique within space", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db)

		space := &model.Space{ID: idgen.NextString(), Name: "test-space", OwnerID: idgen.NextString()}
		if err := db.Create(space).Error; err != nil {
			t.Fatalf("create space: %v", err)
		}
		u1 := createTestUser(db, "u1", "u1@example.com")
		u2 := createTestUser(db, "u2", "u2@example.com")
		for _, u := range []*model.User{u1, u2} {
			if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: u.ID, SpaceID: space.ID, Role: "MEMBER"}).Error; err != nil {
				t.Fatalf("create membership: %v", err)
			}
		}

		if err := svc.UpdateProfile(u2.ID, map[string]interface{}{"display_name": "Bob"}); err != nil {
			t.Fatalf("setup u2 display name: %v", err)
		}

		// 大小写不敏感：改名为 "bob" 与同空间 u2 的 "Bob" 冲突
		err := svc.UpdateProfile(u1.ID, map[string]interface{}{"display_name": "bob"})
		if err == nil {
			t.Fatal("expected conflict error for duplicate display name")
		}
		appErr, ok := err.(*errors.AppError)
		if !ok || appErr.Code != errors.AUTH_DISPLAY_NAME_EXISTS {
			t.Errorf("expected AUTH_DISPLAY_NAME_EXISTS, got %v", err)
		}

		// 改成自己的当前名（不同空间/自己除外）允许
		if err := svc.UpdateProfile(u2.ID, map[string]interface{}{"display_name": "Bob"}); err != nil {
			t.Errorf("expected setting own current display name to succeed, got %v", err)
		}
		// 无 membership 的用户不受空间内唯一约束影响
		u3 := createTestUser(db, "u3", "u3@example.com")
		if err := svc.UpdateProfile(u3.ID, map[string]interface{}{"display_name": "bob"}); err != nil {
			t.Errorf("expected user without space membership to succeed, got %v", err)
		}
	})
}
