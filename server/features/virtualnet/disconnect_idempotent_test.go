package virtualnet

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

// 1.9.5 task-136：Disconnect 必须是「条件更新 + RowsAffected 判定」，
// 并发重复调用只生效一次、只广播一次；ReportIP 在会话已断开后必须拒绝且不改 DB。
// 旧实现是「先 First 查 connected → 再无条件 Update」，并发下两个请求都会
// 通过检查并各自广播一次（规则 3：禁止读改写）。

func seedVirtualNetConnected(t *testing.T) (*Handler, *model.User, *model.Space, *model.VirtualNetSession) {
	t.Helper()
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "vn-u", Email: "vn-u@example.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "VN", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	session := &model.VirtualNetSession{
		ID: idgen.NextString(), SpaceID: space.ID, UserID: user.ID,
		NodeID: "rrt-" + user.ID, Status: "connected", IP: "10.126.126.9",
	}
	if err := db.Create(session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := db.Create(&model.VirtualNetNode{
		ID: idgen.NextString(), SpaceID: space.ID, SessionID: session.ID,
		Name: "rrt-" + user.ID, IP: session.IP, Status: "active",
	}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	return handler, user, space, session
}

func TestDisconnectConcurrentIsIdempotentAndBroadcastsOnce(t *testing.T) {
	handler, user, space, session := seedVirtualNetConnected(t)
	db := handler.db

	// 单写者 SQLite：避免 "database is locked" 干扰「并发只生效一次」的断言。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	// 订阅广播，统计 virtualnet_user_disconnected 事件条数。
	go handler.hub.Run()
	events, unsubscribe := handler.hub.SubscribeAdmin()
	defer unsubscribe()

	r := gin.New()
	r.POST("/api/virtualnet/disconnect", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.Disconnect(c)
	})

	const n = 6
	type result struct {
		code int
		body string
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/virtualnet/disconnect", nil)
			r.ServeHTTP(w, req)
			results[idx] = result{code: w.Code, body: w.Body.String()}
		}(i)
	}
	wg.Wait()

	disconnected, already := 0, 0
	for _, res := range results {
		if res.code != http.StatusOK {
			t.Fatalf("并发 Disconnect 必须全部 200，实际 code=%d body=%s", res.code, res.body)
		}
		if strings.Contains(res.body, "already disconnected") {
			already++
		} else if strings.Contains(res.body, "disconnected from virtual network") {
			disconnected++
		} else {
			t.Fatalf("意外的响应体：%s", res.body)
		}
	}
	if disconnected != 1 || already != n-1 {
		t.Fatalf("应恰好 1 次真实断开 + %d 次幂等，实际 disconnected=%d already=%d", n-1, disconnected, already)
	}

	// DB 状态：会话断开、节点 inactive。
	var gotSession model.VirtualNetSession
	if err := db.First(&gotSession, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if gotSession.Status != "disconnected" {
		t.Fatalf("session status 应为 disconnected，实际 %q", gotSession.Status)
	}
	var nodes []model.VirtualNetNode
	if err := db.Where("session_id = ?", session.ID).Find(&nodes).Error; err != nil {
		t.Fatalf("load nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Status != "inactive" {
		t.Fatalf("node 应为 1 条 inactive，实际 %+v", nodes)
	}

	// 广播：只允许 1 条 virtualnet_user_disconnected（其余请求命中 0 行直接返回）。
	var broadcasts int
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case raw := <-events:
			var msg struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if msg.Type == "virtualnet_user_disconnected" {
				broadcasts++
			}
		case <-deadline:
			if broadcasts != 1 {
				t.Fatalf("virtualnet_user_disconnected 应只广播 1 次，实际 %d 次", broadcasts)
			}
			return
		}
	}
}

func TestReportIPAfterDisconnectRejected(t *testing.T) {
	handler, user, space, session := seedVirtualNetConnected(t)
	db := handler.db

	r := gin.New()
	r.POST("/api/virtualnet/report-ip", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.ReportIP(c)
	})

	// 先把会话断开（模拟并发 Disconnect 抢在前面）。
	if err := db.Model(&model.VirtualNetSession{}).Where("id = ?", session.ID).Update("status", "disconnected").Error; err != nil {
		t.Fatalf("disconnect session: %v", err)
	}

	body := bytes.NewBufferString(`{"ip":"10.126.126.77"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/virtualnet/report-ip", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("断开后上报 IP 应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("VIRTUALNET_NOT_CONNECTED")) {
		t.Fatalf("应返回 VIRTUALNET_NOT_CONNECTED，实际 %s", w.Body.String())
	}

	// DB 中的 ip 必须保持原值（不得被断开后的上报覆盖）。
	var got model.VirtualNetSession
	if err := db.First(&got, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.IP != session.IP {
		t.Fatalf("断开后不得改写 ip：期望 %q，实际 %q", session.IP, got.IP)
	}
}
