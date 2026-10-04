package realtime

// DES-20261001-01 测试：
//   - A4-S2：security_alert 定向广播按 sessionID 过滤（新会话连接不收、
//     无 sessionID 的旧连接照收）；
//   - A5-S1：screenshare_annotate 仅共享者可发（他人/未知频道静默丢弃）、
//     服务端注入 by、坐标 clamp、停止/断连代发 {tool:"clear"}、
//     20 msg/s 独立限速桶（不消耗全局桶）。
//
// 模式沿用 hub_test.go：mock Client 直连 register channel +
// handleClientMessage，不经真实 WebSocket 升级。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ridgericetalk/tests/testutil"
)

// newSIDClient 构造一个带 userID/sessionID 的 mock 客户端并注册到 hub。
func newSIDClient(hub *Hub, userID, sessionID string) *Client {
	client := &Client{
		hub:         hub,
		send:        make(chan []byte, 256),
		userID:      userID,
		sessionID:   sessionID,
		channels:    make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}
	hub.register <- client
	time.Sleep(50 * time.Millisecond)
	return client
}

// annotationEvent 是 screenshare_annotation 服务端转发事件的解码形状。
type annotationEvent struct {
	Type    string `json:"type"`
	Payload struct {
		ChannelID string       `json:"channelId"`
		Tool      string       `json:"tool"`
		Points    [][2]float64 `json:"points"`
		Color     string       `json:"color"`
		SegID     string       `json:"segId"`
		By        string       `json:"by"`
	} `json:"payload"`
}

// expectAnnotation 从 client.send 读一条消息并断言为指定 tool 的标注事件。
func expectAnnotation(t *testing.T, c *Client, wantTool, wantBy string) annotationEvent {
	t.Helper()
	select {
	case raw := <-c.send:
		var ev annotationEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("invalid annotation event: %v (raw=%s)", err, raw)
		}
		if ev.Type != "screenshare_annotation" {
			t.Fatalf("expected type screenshare_annotation, got %s (raw=%s)", ev.Type, raw)
		}
		if wantTool != "" && ev.Payload.Tool != wantTool {
			t.Errorf("payload.tool = %s, want %s", ev.Payload.Tool, wantTool)
		}
		if wantBy != "" && ev.Payload.By != wantBy {
			t.Errorf("payload.by = %s, want %s (server must inject the sharer id)", ev.Payload.By, wantBy)
		}
		return ev
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("client %s did not receive screenshare_annotation event", c.userID)
		return annotationEvent{}
	}
}

// TestClientSessionIDFilter 覆盖 A4-S2：BroadcastToUserExcept 跳过
// sessionID 匹配的连接；无 sessionID 的旧连接不跳过（宁多发不漏发）；
// 既有 BroadcastToUser（无 except）行为不变。
func TestClientSessionIDFilter(t *testing.T) {
	setup := func(t *testing.T) (*Hub, *Client, *Client) {
		t.Helper()
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		// 同一用户两条连接：旧连接（无 sessionID 声明）与新登录连接。
		oldConn := newSIDClient(hub, "alert-user", "")
		newConn := newSIDClient(hub, "alert-user", "sess-new-login")
		return hub, oldConn, newConn
	}

	t.Run("alert reaches legacy connection but skips the new session", func(t *testing.T) {
		hub, oldConn, newConn := setup(t)

		hub.BroadcastToUserExcept("alert-user", "sess-new-login", []byte(`{"type":"security_alert"}`))

		select {
		case raw := <-oldConn.send:
			if string(raw) != `{"type":"security_alert"}` {
				t.Errorf("legacy connection got %s", raw)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("legacy connection (no sessionID) must receive the alert")
		}

		time.Sleep(50 * time.Millisecond)
		select {
		case raw := <-newConn.send:
			t.Fatalf("new-session connection must be skipped, got %s", raw)
		default:
		}
	})

	t.Run("empty except behaves like BroadcastToUser", func(t *testing.T) {
		hub, oldConn, newConn := setup(t)

		hub.BroadcastToUserExcept("alert-user", "", []byte(`{"type":"presence"}`))
		for _, c := range []*Client{oldConn, newConn} {
			select {
			case <-c.send:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("client %s should receive when except is empty", c.userID)
			}
		}

		// 既有原语：无过滤。
		hub.BroadcastToUser("alert-user", []byte(`{"type":"dm"}`))
		for _, c := range []*Client{oldConn, newConn} {
			select {
			case <-c.send:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("client %s should receive plain BroadcastToUser", c.userID)
			}
		}
	})
}

// TestAnnotateSharerOnly 覆盖 A5-S1（DES-20261001-01 §6.2）：
// 共享者放行 + 服务端注入 by / 他人静默丢弃 / 未知频道丢弃 / 非法 payload
// 丢弃 / 停止与断连后代发 clear / 20 msg/s 独立限速桶。
func TestAnnotateSharerOnly(t *testing.T) {
	annotateMsg := func(channelID, tool, color string, points string) *Message {
		raw := `{"channelId":"` + channelID + `","tool":"` + tool + `","color":"` + color + `"`
		if points != "" {
			raw += `,"points":` + points
		}
		raw += `}`
		return &Message{Type: "screenshare_annotate", Payload: []byte(raw)}
	}

	setup := func(t *testing.T) (*Hub, *Client, *Client, *Client) {
		t.Helper()
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		sharer := newSIDClient(hub, "sharer-A", "sess-A")
		viewer := newSIDClient(hub, "viewer-B", "sess-B")
		other := newSIDClient(hub, "other-C", "sess-C")
		for _, c := range []*Client{sharer, viewer} {
			c.subscribe("ch-ss")
		}
		// other 只订阅不共享（频道成员，可看不可画）。
		other.subscribe("ch-ss")
		time.Sleep(50 * time.Millisecond)

		// 共享者开始共享；随后排空各端队列（started 的原状态广播不在
		// 各子测试的断言范围内）。
		hub.handleClientMessage(sharer, &Message{
			Type:    "screenshare_started",
			Payload: []byte(`{"channelId":"ch-ss"}`),
		})
		time.Sleep(50 * time.Millisecond)
		for _, c := range []*Client{sharer, viewer, other} {
			drainClient(c)
		}
		return hub, sharer, viewer, other
	}

	t.Run("sharer annotation is relayed with server-injected by", func(t *testing.T) {
		hub, sharer, viewer, other := setup(t)
		_ = hub

		hub.handleClientMessage(sharer, annotateMsg("ch-ss", "pen", "#f44336", `[[1.7,-0.3],[0.5,0.5]]`))

		// 共享者收到（预览回显），by 注入共享者 id，坐标 clamp 到 [0,1]。
		ev := expectAnnotation(t, sharer, "pen", "sharer-A")
		if len(ev.Payload.Points) != 2 {
			t.Fatalf("expected 2 points, got %d", len(ev.Payload.Points))
		}
		if ev.Payload.Points[0][0] != 1 || ev.Payload.Points[0][1] != 0 {
			t.Errorf("points not clamped: %v", ev.Payload.Points[0])
		}
		if ev.Payload.Color != "#f44336" {
			t.Errorf("payload.color = %s", ev.Payload.Color)
		}
		// 观看端收到同样事件。
		expectAnnotation(t, viewer, "pen", "sharer-A")
		// 事件照常到达频道全体订阅者（other 也是订阅者，可见标注）。
		expectAnnotation(t, other, "pen", "sharer-A")
	})

	t.Run("non-sharer is silently dropped", func(t *testing.T) {
		hub, sharer, viewer, other := setup(t)

		hub.handleClientMessage(other, annotateMsg("ch-ss", "pen", "#f44336", `[[0.1,0.2]]`))

		assertNoMessage(t, sharer, "sharer")
		assertNoMessage(t, viewer, "viewer")
		assertNoMessage(t, other, "non-sharer sender itself")
	})

	t.Run("unknown channel is silently dropped", func(t *testing.T) {
		hub, sharer, viewer, _ := setup(t)

		hub.handleClientMessage(sharer, annotateMsg("ch-unknown", "pen", "#f44336", `[[0.1,0.2]]`))

		assertNoMessage(t, sharer, "sharer")
		assertNoMessage(t, viewer, "viewer")
	})

	t.Run("invalid tool, color and oversized points are dropped", func(t *testing.T) {
		hub, sharer, viewer, _ := setup(t)

		hub.handleClientMessage(sharer, annotateMsg("ch-ss", "spray", "#f44336", `[[0.1,0.2]]`))
		hub.handleClientMessage(sharer, annotateMsg("ch-ss", "pen", "red", `[[0.1,0.2]]`))
		many := "[[0.1,0.2]]"
		for i := 0; i < 64; i++ { // 65 个点，超过上限
			many = many[:len(many)-1] + `,[0.1,0.2]]`
		}
		hub.handleClientMessage(sharer, annotateMsg("ch-ss", "pen", "#f44336", many))

		assertNoMessage(t, viewer, "viewer")
		assertNoMessage(t, sharer, "sharer")
	})

	t.Run("stopped broadcasts state and server-sent clear", func(t *testing.T) {
		hub, sharer, viewer, _ := setup(t)

		hub.handleClientMessage(sharer, &Message{
			Type:    "screenshare_stopped",
			Payload: []byte(`{"channelId":"ch-ss"}`),
		})
		time.Sleep(50 * time.Millisecond)

		// viewer 应恰好收到两条消息：原状态广播（行为保持不变）+ 服务端
		// 代发的 {tool:"clear"}。两者投递顺序不作为契约，归类断言。
		var sawStopped, sawClear bool
		for i := 0; i < 2; i++ {
			select {
			case raw := <-viewer.send:
				switch {
				case strings.Contains(string(raw), "screenshare_stopped"):
					sawStopped = true
				case strings.Contains(string(raw), `"tool":"clear"`):
					var ev annotationEvent
					if err := json.Unmarshal(raw, &ev); err != nil {
						t.Fatalf("decode clear event: %v (raw=%s)", err, raw)
					}
					if ev.Payload.By != "sharer-A" {
						t.Errorf("clear event by = %s, want sharer-A", ev.Payload.By)
					}
					sawClear = true
				default:
					t.Fatalf("unexpected message: %s", raw)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("viewer only received %d of 2 expected messages", i)
			}
		}
		if !sawStopped {
			t.Error("expected the original screenshare_stopped relay to be preserved")
		}
		if !sawClear {
			t.Error("expected the server-sent clear annotation")
		}
		// 缓存已清空：此后共享者的标注被静默丢弃。
		hub.handleClientMessage(sharer, annotateMsg("ch-ss", "pen", "#f44336", `[[0.1,0.2]]`))
		assertNoMessage(t, viewer, "viewer after stop")
		_ = hub
	})

	t.Run("disconnect cleanup sends clear", func(t *testing.T) {
		hub, sharer, viewer, _ := setup(t)

		// 模拟共享者断连：走 unregister 清理路径。
		hub.unregister <- sharer
		time.Sleep(50 * time.Millisecond)

		expectAnnotation(t, viewer, "clear", "sharer-A")
		if _, ok := hub.channelSharerOf("ch-ss"); ok {
			t.Error("expected channel sharer cache entry to be removed on disconnect")
		}
	})

	t.Run("dedicated rate limit bucket of 20 per second", func(t *testing.T) {
		_, _, other, _ := setup(t)

		// 独立桶：前 20 条放行，第 21 条被丢（静默）。
		for i := 0; i < 20; i++ {
			if !other.allowsMessage("screenshare_annotate") {
				t.Fatalf("message %d should pass the dedicated bucket", i+1)
			}
		}
		if other.allowsMessage("screenshare_annotate") {
			t.Error("message 21 should be dropped by the 20 msg/s dedicated bucket")
		}

		// 独立桶是「替代」语义：screenshare_annotate 耗尽不消耗全局桶，
		// 低频事件 typing 不被挤死。
		if !other.allowsMessage("typing") {
			t.Error("typing must not be affected by the screenshare_annotate bucket")
		}
		// 对照：全局桶依然按自身容量工作（10 msg/s）。
		for i := 0; i < 9; i++ { // typing 已耗 1 个，再耗 9 个
			if !other.allowsMessage("typing") {
				t.Fatalf("global bucket message %d should pass", i+2)
			}
		}
		if other.allowsMessage("typing") {
			t.Error("global bucket (10 msg/s) should be exhausted now")
		}
	})
}
