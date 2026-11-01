package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// 1.9.5 task-138（规则 2 / 越权）：GET /api/users/space/members 只返回**当前空间**成员，
// 且带 limit（默认 200、上限 1000）。本文件覆盖三件事：
//  1. 跨空间隔离：别的空间用户不能出现在成员列表里；
//  2. 缺 space_id 时拒绝（不再退化成「返回全库用户」）；
//  3. limit 生效且不会因为超大值报错。

func newMembersRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	h := NewHandler(db, config.DefaultConfig(), nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := func(c *gin.Context) {
		c.Set("user_id", "user-a")
		c.Set("username", "user-a")
		c.Set("role", "MEMBER")
		if sid := c.GetHeader("X-Test-Space"); sid != "" {
			c.Set("space_id", sid)
		}
		c.Next()
	}
	r.GET("/api/users/space/members", auth, h.GetMembers)
	return r, db
}

func seedMember(t *testing.T, db *gorm.DB, spaceID, username string) *model.User {
	t.Helper()
	u := createTestUser(db, username, username+"@example.com")
	if err := db.Create(&model.Membership{
		ID:      "m-" + spaceID + "-" + u.ID,
		UserID:  u.ID,
		SpaceID: spaceID,
		Role:    "MEMBER",
	}).Error; err != nil {
		t.Fatalf("建 membership 失败: %v", err)
	}
	return u
}

type memberItem struct {
	UserID      string `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

func getMembers(t *testing.T, r *gin.Engine, spaceID string, query string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/users/space/members" + query
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if spaceID != "" {
		req.Header.Set("X-Test-Space", spaceID)
	}
	r.ServeHTTP(w, req)
	return w
}

func decodeMembers(t *testing.T, w *httptest.ResponseRecorder) []memberItem {
	t.Helper()
	var resp struct {
		Code string       `json:"code"`
		Data []memberItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, w.Body.String())
	}
	if resp.Code != "OK" {
		t.Fatalf("期望 OK，实际 %s, body=%s", resp.Code, w.Body.String())
	}
	return resp.Data
}

// TestGetMembersFiltersBySpace 只返回当前空间成员，其它空间用户不可见。
func TestGetMembersFiltersBySpace(t *testing.T) {
	r, db := newMembersRouter(t)

	seedMember(t, db, "space-1", "alice")
	seedMember(t, db, "space-1", "bob")
	other := seedMember(t, db, "space-2", "carol")

	members := decodeMembers(t, getMembers(t, r, "space-1", ""))
	if len(members) != 2 {
		t.Fatalf("space-1 应有 2 名成员，实际 %d：%+v", len(members), members)
	}
	for _, m := range members {
		if m.UserID == other.ID {
			t.Fatalf("其它空间用户 %s 不应出现在 space-1 成员列表", other.Username)
		}
	}

	members = decodeMembers(t, getMembers(t, r, "space-2", ""))
	if len(members) != 1 || members[0].UserID != other.ID {
		t.Fatalf("space-2 应只有 carol，实际 %+v", members)
	}
}

// TestGetMembersRequiresSpaceID 缺 space_id 时返回 400，绝不退化成「全库用户」。
func TestGetMembersRequiresSpaceID(t *testing.T) {
	r, db := newMembersRouter(t)
	seedMember(t, db, "space-1", "alice")

	w := getMembers(t, r, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 space_id 时应返回 400，实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestGetMembersHonorsLimit limit 生效（默认值以外的显式限制）。
func TestGetMembersHonorsLimit(t *testing.T) {
	r, db := newMembersRouter(t)
	for _, name := range []string{"u1", "u2", "u3", "u4", "u5"} {
		seedMember(t, db, "space-1", name)
	}

	members := decodeMembers(t, getMembers(t, r, "space-1", "?limit=2"))
	if len(members) != 2 {
		t.Fatalf("limit=2 应只返回 2 名成员，实际 %d", len(members))
	}
}

// TestGetMembersCapsOversizedLimit 超大 limit 不报错（内部截断到 1000）。
func TestGetMembersCapsOversizedLimit(t *testing.T) {
	r, db := newMembersRouter(t)
	for _, name := range []string{"u1", "u2", "u3"} {
		seedMember(t, db, "space-1", name)
	}

	members := decodeMembers(t, getMembers(t, r, "space-1", "?limit=999999"))
	if len(members) != 3 {
		t.Fatalf("超大 limit 应返回全部 3 名成员，实际 %d", len(members))
	}
}
