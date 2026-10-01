package server

import (
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// TestSubscribeSyncSnapshotIncludesActiveScreenShare 是缺陷回归：OnSubscribeSync
// 曾用不存在的列 bind_channel_id 查询 ScreenShareSession（000008 迁移已把该列改名
// 为 channel_id）。查询必然报错，而调用点只在 err == nil 时写入
// screenshareState，于是静默退化成 active=false —— 新订阅者进频道看不到「有人
// 正在共享屏幕」的提示，且没有任何日志。
func TestSubscribeSyncSnapshotIncludesActiveScreenShare(t *testing.T) {
	gormDB := testutil.MustSetupTestDB()
	db := &database.DB{DB: gormDB}
	log := testutil.TestLogger()

	hub := realtime.NewHub(log)
	wireHubCallbacks(hub, db, nil, nil, nil, log)
	if hub.OnSubscribeSync == nil {
		t.Fatal("wireHubCallbacks did not register OnSubscribeSync")
	}

	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: idgen.NextString()}
	if err := gormDB.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "voice", Type: "voice"}
	if err := gormDB.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sharer := &model.User{
		ID: idgen.NextString(), Username: "sharer", Email: "sharer@example.com",
		PasswordHash: "x", Role: "MEMBER", IsActive: true,
	}
	if err := gormDB.Create(sharer).Error; err != nil {
		t.Fatalf("create sharer: %v", err)
	}

	// 另一个频道的活跃共享（用于验证不会串频道）
	otherCh := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "other", Type: "voice"}
	if err := gormDB.Create(otherCh).Error; err != nil {
		t.Fatalf("create other channel: %v", err)
	}

	t.Run("active share on this channel is reported", func(t *testing.T) {
		session := &model.ScreenShareSession{
			ID: idgen.NextString(), UserID: sharer.ID, SpaceID: space.ID,
			ChannelID: ch.ID, ShareType: "window", Active: true,
			StartedAt: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
		}
		if err := gormDB.Create(session).Error; err != nil {
			t.Fatalf("create screen share session: %v", err)
		}
		snapshot := hub.OnSubscribeSync(sharer.ID, ch.ID)

		state, ok := snapshot["screenshareState"].(map[string]interface{})
		if !ok {
			t.Fatalf("screenshareState missing or wrong type: %#v", snapshot["screenshareState"])
		}
		if state["active"] != true {
			t.Fatalf("screenshareState.active = %v, want true (snapshot=%#v)", state["active"], state)
		}
		if state["userId"] != sharer.ID {
			t.Errorf("screenshareState.userId = %v, want %s", state["userId"], sharer.ID)
		}
		if state["username"] != "sharer" {
			t.Errorf("screenshareState.username = %v, want sharer", state["username"])
		}
		if state["shareType"] != "window" {
			t.Errorf("screenshareState.shareType = %v, want window", state["shareType"])
		}
		if state["startedAt"] != "2026-09-14T10:00:00Z" {
			t.Errorf("screenshareState.startedAt = %v, want 2026-09-14T10:00:00Z", state["startedAt"])
		}
	})

	t.Run("no share on this channel reports inactive", func(t *testing.T) {
		snapshot := hub.OnSubscribeSync(sharer.ID, otherCh.ID)

		state, ok := snapshot["screenshareState"].(map[string]interface{})
		if !ok {
			t.Fatalf("screenshareState missing or wrong type: %#v", snapshot["screenshareState"])
		}
		if state["active"] != false {
			t.Fatalf("screenshareState.active = %v, want false", state["active"])
		}
	})
}

// TestAuthorizeWhiteboardWiring 是 M11 修复的装配层回归：AuthorizeWhiteboard
// 必须按「白板存在 + 请求者是白板所属空间的成员（或全局 OWNER）」放行，
// 任一条件不满足（含白板不存在、用户不存在、查询失败）一律拒绝。
// hub 侧的 fail-closed（回调未装配时拒绝）见 internal/realtime/hub_test.go
// 的 TestHubAuthorizeWhiteboard；本测试验证的是装配后的真实判定口径。
func TestAuthorizeWhiteboardWiring(t *testing.T) {
	gormDB := testutil.MustSetupTestDB()
	db := &database.DB{DB: gormDB}
	log := testutil.TestLogger()

	hub := realtime.NewHub(log)
	wireHubCallbacks(hub, db, nil, nil, nil, log)
	if hub.AuthorizeWhiteboard == nil {
		t.Fatal("wireHubCallbacks did not register AuthorizeWhiteboard")
	}

	spaceA := &model.Space{ID: idgen.NextString(), Name: "SpaceA", OwnerID: idgen.NextString()}
	if err := gormDB.Create(spaceA).Error; err != nil {
		t.Fatalf("create spaceA: %v", err)
	}
	spaceB := &model.Space{ID: idgen.NextString(), Name: "SpaceB", OwnerID: idgen.NextString()}
	if err := gormDB.Create(spaceB).Error; err != nil {
		t.Fatalf("create spaceB: %v", err)
	}

	wbA := &model.Whiteboard{
		ID: idgen.NextString(), SpaceID: spaceA.ID,
		Name: "board-a", CreatedBy: spaceA.OwnerID,
	}
	if err := gormDB.Create(wbA).Error; err != nil {
		t.Fatalf("create whiteboard: %v", err)
	}

	newUser := func(name, role string) *model.User {
		u := &model.User{
			ID: idgen.NextString(), Username: name, Email: name + "@example.com",
			PasswordHash: "x", Role: role, IsActive: true,
		}
		if err := gormDB.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	member := newUser("wbmember", "MEMBER") // SpaceA 成员
	if err := gormDB.Create(&model.Membership{
		ID: idgen.NextString(), UserID: member.ID, SpaceID: spaceA.ID, Role: "MEMBER",
	}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	outsider := newUser("wboutsider", "MEMBER") // SpaceB 成员（跨空间）
	if err := gormDB.Create(&model.Membership{
		ID: idgen.NextString(), UserID: outsider.ID, SpaceID: spaceB.ID, Role: "MEMBER",
	}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	globalOwner := newUser("wbglobalowner", "OWNER") // 全局 OWNER，非两空间成员

	t.Run("same-space member is allowed", func(t *testing.T) {
		if !hub.AuthorizeWhiteboard(member.ID, wbA.ID) {
			t.Error("expected same-space member to be allowed")
		}
	})

	t.Run("cross-space non-member is denied", func(t *testing.T) {
		if hub.AuthorizeWhiteboard(outsider.ID, wbA.ID) {
			t.Error("expected cross-space non-member to be denied")
		}
	})

	t.Run("nonexistent whiteboard is denied", func(t *testing.T) {
		if hub.AuthorizeWhiteboard(member.ID, "wb_nonexistent") {
			t.Error("expected nonexistent whiteboard to be denied")
		}
	})

	t.Run("nonexistent user is denied", func(t *testing.T) {
		if hub.AuthorizeWhiteboard("user_ghost", wbA.ID) {
			t.Error("expected nonexistent user to be denied")
		}
	})

	t.Run("global owner is allowed", func(t *testing.T) {
		if !hub.AuthorizeWhiteboard(globalOwner.ID, wbA.ID) {
			t.Error("expected global OWNER to be allowed")
		}
	})
}
