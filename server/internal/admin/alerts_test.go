// alerts_test.go — DES-20261001-01 §12.4「A11 管理后台监控告警」测试。
//
// 覆盖：
//   - TestEvaluatorDebounce          防抖语义（连续 3 周期才 open / 恢复置
//     resolved / 同类活跃行不重复插入），表驱动多种检查序列；
//   - TestEvaluatorMuteSuppressesWS  muted_until 期间同 type 不推 WS、表照记；
//   - TestEvaluatorLiveKitGrace      livekit 检查：nil manager / 宽限窗语义
//     （宽限窗字段语义在 livekitmgr 包内测：TestManagerInGraceWindow）；
//   - TestTTSWorkerAlertCheck        tts_worker 状态分类（崩溃 warn / 未配置忽略）;
//   - TestAdminAlertsWSWhitelist     isRelevant 白名单放行 admin_alert 系、拦截其他；
//   - TestAlertsSnapshotOnConnect    连接建立先推活跃告警快照；
//   - TestGetAlerts / TestMuteAlert  REST：列表参数、静默写入 muted_until + 审计。
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/livekitmgr"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"

	"gorm.io/gorm"
)

// ptrTime 测试辅助：time.Time → *time.Time。
func ptrTime(t time.Time) *time.Time { return &t }

// alertTestEval 构造一个不启动 goroutine 的评估器（测试直接调 evaluateAll）。
func alertTestEval(t *testing.T, db *gorm.DB, checks map[string]AlertCheckFunc, bc *[]string) *AlertEvaluator {
	t.Helper()
	log := testutil.TestLogger()
	cfg := AlertEvaluatorConfig{
		DB:  db,
		Log: log,
		Checks: func() map[string]AlertCheckFunc {
			if checks != nil {
				return checks
			}
			return map[string]AlertCheckFunc{} // 空 checks：默认表不注入（避免真实探测）
		}(),
		DebounceThreshold: DefaultDebounceThreshold,
		Now: func() time.Time {
			return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		},
	}
	if bc != nil {
		cfg.Broadcaster = func(action string, _ *model.AdminAlert) {
			*bc = append(*bc, action)
		}
	}
	return NewAlertEvaluator(cfg)
}

func countOpenAlerts(t *testing.T, db *gorm.DB, alertType string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AdminAlert{}).Where("type = ? AND resolved_at IS NULL", alertType).Count(&n).Error; err != nil {
		t.Fatalf("count open alerts: %v", err)
	}
	return n
}

func getOpenAlert(t *testing.T, db *gorm.DB, alertType string) model.AdminAlert {
	t.Helper()
	var row model.AdminAlert
	if err := db.Where("type = ? AND resolved_at IS NULL", alertType).First(&row).Error; err != nil {
		t.Fatalf("expected open alert of type %s: %v", alertType, err)
	}
	return row
}

func firingCheck(msg string) AlertCheckFunc {
	return func(context.Context) CheckOutcome {
		return CheckOutcome{Firing: true, Severity: AlertSeverityAlert, Message: msg}
	}
}

func okCheck() AlertCheckFunc {
	return func(context.Context) CheckOutcome { return CheckOutcome{} }
}

// TestEvaluatorDebounce 表驱动防抖语义。
// 变异锚点：若有人把防抖阈值 3 改成 1（破坏防抖语义），第一个 case 立即红。
func TestEvaluatorDebounce(t *testing.T) {
	type step struct {
		firing bool
	}
	cases := []struct {
		name string
		// 每个元素是一个评估周期（firing 与否）。
		cycles       []step
		wantOpenAt   int  // 首次出现活跃行的周期下标（0-based）；-1 = 从不
		wantResolve  bool // 序列结束时活跃行应已恢复
		wantMaxRows  int64
		wantEvents   []string // 广播 action 序列
	}{
		{
			name:        "单次超标不产生告警（防抖）",
			cycles:      []step{{true}, {false}},
			wantOpenAt:  -1,
			wantResolve: false,
			wantMaxRows: 0,
			wantEvents:  nil,
		},
		{
			name:        "连续 2 周期仍不 open",
			cycles:      []step{{true}, {true}, {false}},
			wantOpenAt:  -1,
			wantResolve: false,
			wantMaxRows: 0,
			wantEvents:  nil,
		},
		{
			name:        "连续 3 周期第 3 周期 open，随后恢复置 resolved",
			cycles:      []step{{true}, {true}, {true}, {false}},
			wantOpenAt:  2,
			wantResolve: true,
			wantMaxRows: 1,
			wantEvents:  []string{"open", "resolved"},
		},
		{
			name:        "同类未 resolve 不重复插入，持续超标只 update",
			cycles:      []step{{true}, {true}, {true}, {true}, {true}},
			wantOpenAt:  2,
			wantResolve: false,
			wantMaxRows: 1,
			wantEvents:  []string{"open", "update", "update"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.MustSetupTestDB()
			firing := false
			checks := map[string]AlertCheckFunc{
				AlertTypeDisk: func(context.Context) CheckOutcome {
					if firing {
						return CheckOutcome{Firing: true, Severity: AlertSeverityAlert, Message: "磁盘超阈值"}
					}
					return CheckOutcome{}
				},
			}
			var events []string
			ev := alertTestEval(t, db, checks, &events)
			ctx := context.Background()

			for i, c := range tc.cycles {
				firing = c.firing
				ev.evaluateAll(ctx)
				open := countOpenAlerts(t, db, AlertTypeDisk)
				// open 行数断言只针对超标周期；恢复周期（firing=false）之后
				// open 归零是预期行为（resolved），不做断言。
				if c.firing && tc.wantOpenAt >= 0 && i >= tc.wantOpenAt && open != 1 {
					t.Fatalf("cycle %d: open rows = %d, want 1", i, open)
				}
				if c.firing && tc.wantOpenAt < 0 && open != 0 {
					t.Fatalf("cycle %d: open rows = %d, want 0 (防抖未达标不应建行)", i, open)
				}
			}

			var total int64
			if err := db.Model(&model.AdminAlert{}).Where("type = ?", AlertTypeDisk).Count(&total).Error; err != nil {
				t.Fatalf("count all: %v", err)
			}
			if total > tc.wantMaxRows {
				t.Fatalf("total rows = %d, want <= %d（同类不得重复插入）", total, tc.wantMaxRows)
			}

			if tc.wantEvents != nil {
				if strings.Join(events, ",") != strings.Join(tc.wantEvents, ",") {
					t.Fatalf("events = %v, want %v", events, tc.wantEvents)
				}
			} else if len(events) != 0 {
				t.Fatalf("events = %v, want none", events)
			}

			row := model.AdminAlert{}
			err := db.Where("type = ?", AlertTypeDisk).Order("last_seen_at DESC").First(&row).Error
			if tc.wantResolve {
				if err != nil {
					t.Fatalf("expected an alert row to verify resolution: %v", err)
				}
				if row.ResolvedAt == nil {
					t.Fatalf("expected alert resolved after recovery, got open row")
				}
			}
		})
	}
}

// TestEvaluatorMuteSuppressesWS 静默期间：表照记，但同 type 不推 WS；
// 静默过期后恢复推送。对照：未静默时推送可达 admin 订阅者。
func TestEvaluatorMuteSuppressesWS(t *testing.T) {
	db := testutil.MustSetupTestDB()
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	go hub.Run()

	checks := map[string]AlertCheckFunc{AlertTypeDisk: firingCheck("disk full")}
	ev := NewAlertEvaluator(AlertEvaluatorConfig{
		DB: db, Hub: hub, Log: log, Checks: checks,
		Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	})
	ctx := context.Background()

	// 打开告警（3 周期），订阅者应收到 open + update 事件。
	sub, unsub := hub.SubscribeAdmin()
	defer unsub()
	ev.evaluateAll(ctx)
	ev.evaluateAll(ctx)
	ev.evaluateAll(ctx)
	expectWSMessage(t, sub, 2*time.Second, WSEventAdminAlert, "open")

	// 静默该 type（写 muted_until 到当前活跃行）。
	row := getOpenAlert(t, db, AlertTypeDisk)
	mutedUntil := time.Now().Add(24 * time.Hour)
	row.MutedUntil = &mutedUntil
	if err := db.Save(&row).Error; err != nil {
		t.Fatalf("mute alert: %v", err)
	}
	if !AlertTypeMuted(db, row.Type, time.Now()) {
		t.Fatalf("alert type should be muted now")
	}

	// 静默期间继续超标：活跃行仍被更新（表照记，message 刷新），
	// 但订阅者收不到任何 admin_alert 消息。
	// （评估器 Now 为注入的固定时钟，last_seen 不变化；用 message 刷新证明落库。）
	ev.checks[AlertTypeDisk] = func(context.Context) CheckOutcome {
		return CheckOutcome{Firing: true, Severity: AlertSeverityAlert, Message: "disk still full (muted)"}
	}
	ev.evaluateAll(ctx)
	after := getOpenAlert(t, db, AlertTypeDisk)
	if after.Message != "disk still full (muted)" {
		t.Fatalf("muted alert must still be recorded: message = %q", after.Message)
	}
	expectNoWSMessage(t, sub, 300*time.Millisecond)

	// 恢复也照样落库（resolved），但不推 WS。
	// （换一个永不过期的假检查让磁盘恢复。）
	ev.checks[AlertTypeDisk] = okCheck()
	ev.evaluateAll(ctx)
	var resolved model.AdminAlert
	if err := db.Where("type = ?", AlertTypeDisk).First(&resolved).Error; err != nil {
		t.Fatalf("load alert: %v", err)
	}
	if resolved.ResolvedAt == nil {
		t.Fatalf("muted alert must still resolve in DB")
	}
	expectNoWSMessage(t, sub, 300*time.Millisecond)
}

// TestEvaluatorLiveKitGrace livekit 检查分支：nil manager 跳过；
// 端口不通时告警。宽限窗「窗内不告警」的语义由 livekitmgr 包内
// TestManagerInGraceWindow 直接对 InGraceWindow() 验证（graceDeadline 是
// 包私有字段，跨包无法注入），本用例锁定 admin 侧的调用契约。
func TestEvaluatorLiveKitGrace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LiveKitPort = 1 // 保留端口，dial 必败

	// nil manager（未托管 LiveKit 的部署）：跳过不告警。
	if out := livekitAlertCheck(cfg, nil); out.Firing {
		t.Fatalf("nil manager must skip livekit check, got %+v", out)
	}

	// 端口不通且非宽限窗：alert（未启动的托管 manager，Running=false 分支）。
	lk := livekitmgr.New(testutil.TestLogger())
	out := livekitAlertCheck(cfg, lk)
	if !out.Firing || out.Severity != AlertSeverityAlert {
		t.Fatalf("dead port must fire alert, got %+v", out)
	}
	if !strings.Contains(out.Message, "127.0.0.1:1") {
		t.Fatalf("message should mention the probed addr, got %q", out.Message)
	}
	if !strings.Contains(out.Message, "托管子进程未运行") {
		t.Fatalf("message should hint the manager state, got %q", out.Message)
	}
}

// expectWSMessage 等待一条指定 type 的 WS 广播并断言其 action 字段。
func expectWSMessage(t *testing.T, sub <-chan []byte, timeout time.Duration, wantType, wantAction string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-sub:
			var peek struct {
				Type    string `json:"type"`
				Payload struct {
					Action string `json:"action"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(msg, &peek); err != nil {
				t.Fatalf("unmarshal broadcast: %v", err)
			}
			if peek.Type != wantType {
				continue // 其他类型广播，继续等
			}
			if wantAction != "" && peek.Payload.Action != wantAction {
				t.Fatalf("action = %q, want %q", peek.Payload.Action, wantAction)
			}
			return
		case <-deadline:
			t.Fatalf("timeout waiting for %s broadcast (action=%q)", wantType, wantAction)
		}
	}
}

// expectNoWSMessage 断言 timeout 内没有任何广播到达。
func expectNoWSMessage(t *testing.T, sub <-chan []byte, timeout time.Duration) {
	t.Helper()
	select {
	case msg := <-sub:
		t.Fatalf("unexpected broadcast: %s", string(msg))
	case <-time.After(timeout):
	}
}

// TestTTSWorkerAlertCheck tts_worker 状态分类。
func TestTTSWorkerAlertCheck(t *testing.T) {
	cases := []struct {
		name      string
		ready     bool
		errText   string
		wantFire  bool
		wantWarn  bool // fire 时 severity 应为 warn
	}{
		{"就绪", true, "", false, false},
		{"无错误但未就绪", false, "not initialized", false, false},
		{"模型未配置（正常降级）", false, "TTS model not found; TTS disabled", false, false},
		{"二进制缺失（未部署）", false, "tts worker binary not found at /x; TTS disabled, engine not initialized", false, false},
		{"worker 崩溃", false, "tts worker exited unexpectedly (exit status 2); engine not initialized", true, true},
		{"spawn 失败", false, "tts worker failed to start (fork/exec ...); TTS disabled, engine not initialized", true, true},
		{"未知错误保守告警", false, "something weird happened", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ttsWorkerAlertCheck(func() (bool, string, string) { return tc.ready, "dir", tc.errText })
			if out.Firing != tc.wantFire {
				t.Fatalf("firing = %v, want %v (out=%+v)", out.Firing, tc.wantFire, out)
			}
			if tc.wantFire && out.Severity != AlertSeverityWarn {
				t.Fatalf("severity = %q, want warn", out.Severity)
			}
		})
	}
}

// TestAdminAlertsWSWhitelist isRelevant 白名单：admin_alert / admin_alert_snapshot
// 放行，module_status_changed 保持放行，presence 等用户面事件仍拦截。
func TestAdminAlertsWSWhitelist(t *testing.T) {
	cases := []struct {
		eventType string
		want      bool
	}{
		{"module_status_changed", true},
		{WSEventAdminAlert, true},
		{WSEventAdminAlertSnapshot, true},
		{"presence_update", false},
		{"welcome", false},
		{"channel_message", false},
		{"", false},
	}
	for _, tc := range cases {
		msg, err := json.Marshal(map[string]string{"type": tc.eventType})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if got := isAdminRelevantBroadcast(msg); got != tc.want {
			t.Fatalf("isAdminRelevantBroadcast(%q) = %v, want %v", tc.eventType, got, tc.want)
		}
	}
	// 非 JSON 消息必须拦截。
	if isAdminRelevantBroadcast([]byte("not-json")) {
		t.Fatalf("non-JSON message must be rejected")
	}
}

// TestAlertsSnapshotOnConnect 连接建立即收到活跃告警快照；
// 之后评估器广播的 admin_alert 事件可达；用户面事件被过滤。
func TestAlertsSnapshotOnConnect(t *testing.T) {
	env := setupWSHandler(t)

	// 预置一条活跃告警 + 一条已恢复告警（快照只应包含活跃那条）。
	active := model.AdminAlert{
		ID: idgen.GenerateID(idgen.PrefixAlert), Type: AlertTypeDisk,
		Severity: AlertSeverityAlert, Message: "磁盘 91%",
		FirstSeenAt: time.Now().Add(-time.Hour), LastSeenAt: time.Now(),
	}
	resolved := model.AdminAlert{
		ID: idgen.GenerateID(idgen.PrefixAlert), Type: AlertTypeHealth,
		Severity: AlertSeverityAlert, Message: "health fail",
		FirstSeenAt: time.Now().Add(-2 * time.Hour), LastSeenAt: time.Now().Add(-time.Hour),
		ResolvedAt: ptrTime(time.Now().Add(-30 * time.Minute)),
	}
	for _, r := range []*model.AdminAlert{&active, &resolved} {
		if err := env.db.Create(r).Error; err != nil {
			t.Fatalf("seed alert: %v", err)
		}
	}

	ws, resp, err := websocket.DefaultDialer.Dial(env.wsURL("?token="+env.ownerToken), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial admin ws failed (status=%d): %v", status, err)
	}
	defer ws.Close()

	// 第一条消息必须是 admin_alert_snapshot，且只含活跃告警。
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap struct {
		Type    string `json:"type"`
		Payload struct {
			Alerts []map[string]interface{} `json:"alerts"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.Type != WSEventAdminAlertSnapshot {
		t.Fatalf("first message type = %q, want %q", snap.Type, WSEventAdminAlertSnapshot)
	}
	if len(snap.Payload.Alerts) != 1 {
		t.Fatalf("snapshot alerts = %d, want 1 (resolved 必须排除)", len(snap.Payload.Alerts))
	}
	if snap.Payload.Alerts[0]["type"] != AlertTypeDisk {
		t.Fatalf("snapshot alert type = %v, want %s", snap.Payload.Alerts[0]["type"], AlertTypeDisk)
	}

	// 评估器式广播（admin_alert）必须穿透白名单到达客户端。
	ev, err := json.Marshal(map[string]interface{}{
		"type":    WSEventAdminAlert,
		"payload": AlertEventPayload("open", &resolved),
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	env.hub.Broadcast(ev)
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, raw, err = ws.ReadMessage()
		if err != nil {
			t.Fatalf("read admin_alert: %v", err)
		}
		var peek struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &peek)
		if peek.Type == WSEventAdminAlert {
			break
		}
	}

	// 用户面事件（presence_update）必须被拦截：广播后 300ms 内客户端
	// 不应收到任何消息。
	env.hub.Broadcast([]byte(`{"type":"presence_update","payload":{}}`))
	ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err = ws.ReadMessage(); err == nil {
		t.Fatalf("presence_update must not reach admin WS clients")
	}
}

// TestGetAlerts REST 列表：默认活跃+最近 resolved；includeResolved=false 只活跃。
func TestGetAlerts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"
	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner, IsActive: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	token, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	now := time.Now()
	rows := []*model.AdminAlert{
		{ID: "alert_a1", Type: AlertTypeDisk, Severity: AlertSeverityAlert, Message: "m1", FirstSeenAt: now, LastSeenAt: now},
		{ID: "alert_r1", Type: AlertTypeHealth, Severity: AlertSeverityAlert, Message: "m2", FirstSeenAt: now, LastSeenAt: now, ResolvedAt: ptrTime(now)},
		{ID: "alert_r2", Type: AlertTypeDatabase, Severity: AlertSeverityWarn, Message: "m3", FirstSeenAt: now, LastSeenAt: now, ResolvedAt: ptrTime(now)},
	}
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	do := func(query string) (int, map[string]interface{}) {
		req, _ := http.NewRequest("GET", "/api/admin/alerts"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var body map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	// 默认：活跃 + resolved。
	code, body := do("")
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, body)
	}
	data := body["data"].(map[string]interface{})
	if len(data["active"].([]interface{})) != 1 {
		t.Fatalf("active = %v, want 1", data["active"])
	}
	if len(data["resolved"].([]interface{})) != 2 {
		t.Fatalf("resolved = %v, want 2", data["resolved"])
	}

	// includeResolved=false：只活跃。
	code, body = do("?includeResolved=false")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	data = body["data"].(map[string]interface{})
	if len(data["active"].([]interface{})) != 1 || len(data["resolved"].([]interface{})) != 0 {
		t.Fatalf("includeResolved=false: active=%v resolved=%v", data["active"], data["resolved"])
	}

	// limit=1：resolved 截断为最近 1 条。
	code, body = do("?limit=1")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	data = body["data"].(map[string]interface{})
	if len(data["resolved"].([]interface{})) != 1 {
		t.Fatalf("limit=1 resolved = %v, want 1", data["resolved"])
	}

	// 非法 limit → 400。
	if code, _ = do("?limit=abc"); code != http.StatusBadRequest {
		t.Fatalf("limit=abc status = %d, want 400", code)
	}
}

// TestMuteAlert REST 静默：写 muted_until、记审计、参数校验与 404。
func TestMuteAlert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"
	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner, IsActive: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	token, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	row := model.AdminAlert{
		ID: idgen.GenerateID(idgen.PrefixAlert), Type: AlertTypeLiveKit,
		Severity: AlertSeverityAlert, Message: "lk down",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	// 模拟 AuthRequired 已解析出的 user_id（GetUserID 从 context 读取）。
	router.Use(func(c *gin.Context) { c.Set("user_id", owner.ID); c.Next() })
	handler.RegisterRoutes(router.Group("/api"))

	muteReq := func(alertID, body string) *httptest.ResponseRecorder {
		req, _ := http.NewRequest("POST", "/api/admin/alerts/"+alertID+"/mute", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 默认 hours（缺省 24）。
	w := muteReq(row.ID, `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var updated model.AdminAlert
	if err := db.First(&updated, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if updated.MutedUntil == nil {
		t.Fatalf("muted_until must be set")
	}
	if dur := time.Until(*updated.MutedUntil); dur < 23*time.Hour || dur > 25*time.Hour {
		t.Fatalf("muted_until = %v, want ~24h from now", updated.MutedUntil)
	}
	if !AlertTypeMuted(db, updated.Type, time.Now()) {
		t.Fatalf("type must be muted after mute call")
	}

	// 审计已记录。
	var audit model.AuditLog
	if err := db.Where("action = ?", "alert_mute").First(&audit).Error; err != nil {
		t.Fatalf("audit log missing: %v", err)
	}
	if audit.UserID != owner.ID || audit.Resource != row.ID {
		t.Fatalf("audit = %+v", audit)
	}

	// 显式 hours。
	if w = muteReq(row.ID, `{"hours":48}`); w.Code != http.StatusOK {
		t.Fatalf("hours=48 status = %d", w.Code)
	}
	if err := db.First(&updated, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if dur := time.Until(*updated.MutedUntil); dur < 47*time.Hour || dur > 49*time.Hour {
		t.Fatalf("muted_until = %v, want ~48h", updated.MutedUntil)
	}

	// 非法 hours → 400。
	if w = muteReq(row.ID, `{"hours":0}`); w.Code != http.StatusBadRequest {
		t.Fatalf("hours=0 status = %d, want 400", w.Code)
	}
	if w = muteReq(row.ID, `{"hours":1000}`); w.Code != http.StatusBadRequest {
		t.Fatalf("hours=1000 status = %d, want 400", w.Code)
	}

	// 不存在的 id → 404。
	if w = muteReq("alert_nope", `{"hours":1}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing id status = %d, want 404", w.Code)
	}
}
