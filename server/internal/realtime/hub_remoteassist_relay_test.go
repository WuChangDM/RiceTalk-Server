package realtime

// A6-S1 / A7-S1（DES-20261001-01 §7.2 / §8.1）WS 路由测试：
//   - TestClipboardRouting：remote_assist_clipboard（控制端 push/pull）与
//     remote_assist_clipboard_data（被控端回传）的路由、校验丢弃、32KB
//     上限、5 msg/s 独立限速桶（不占全局桶）、M10 会话超时后丢弃、
//     校验钩子未装配 fail-closed。
//   - TestAssistChatRouting：remote_assist_chat 双方可发 + 服务端注入
//     from/at、第三者丢弃、超长丢弃、10 msg/s 独立限速桶、会话结束后
//     丢弃、未装配 fail-closed。
//
// realtime 包不能反向依赖 remoteassist（会循环导入），校验钩子按真实契约
// 用 stub 模拟：存在 + authorized + M10 时限 + 参与者判定；校验器自身的
// DB 语义由 remoteassist 包的单元测试覆盖。模式沿用
// hub_screenshare_security_test.go（mock Client + handleClientMessage）。

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/tests/testutil"
)

// pull 缺省 reqId 时服务端生成兜底（A6-S1），生产环境由 app.go 初始化
// idgen；本包其他测试未初始化，这里按 ws_callbacks_test.go 先例补齐。
func init() {
	_ = idgen.Init(1, 1)
}

// fakeAssistSession 模拟远程协助会话的校验面（不查库）。
type fakeAssistSession struct {
	requesterID string
	targetID    string
	authorized  bool
	// deadline 模拟 M10：非零且已过 → 校验器报「已达上限」。
	deadline time.Time
}

// expired 报告会话是否已超 M10 时限。
func (s *fakeAssistSession) expired() bool {
	return !s.deadline.IsZero() && time.Now().After(s.deadline)
}

// wireAssistValidators 按真实校验器契约装配三个钩子的 stub。
func wireAssistValidators(hub *Hub, sessions map[string]*fakeAssistSession) {
	hub.OnRemoteAssistControl = func(sessionID, requesterID, targetID string) error {
		s, ok := sessions[sessionID]
		if !ok {
			return errors.New("session not found")
		}
		if !s.authorized {
			return errors.New("session not authorized")
		}
		if s.expired() {
			return errors.New("session reached the maximum duration")
		}
		if requesterID != s.requesterID || targetID != s.targetID {
			return errors.New("requester/target mismatch")
		}
		return nil
	}
	hub.OnRemoteAssistFromTarget = func(sessionID, senderID string) (string, error) {
		s, ok := sessions[sessionID]
		if !ok {
			return "", errors.New("session not found")
		}
		if !s.authorized {
			return "", errors.New("session not authorized")
		}
		if s.expired() {
			return "", errors.New("session reached the maximum duration")
		}
		if senderID != s.targetID {
			return "", errors.New("sender is not the session target")
		}
		return s.requesterID, nil
	}
	hub.OnRemoteAssistChatParticipant = func(sessionID, senderID string) (string, error) {
		s, ok := sessions[sessionID]
		if !ok {
			return "", errors.New("session not found")
		}
		if !s.authorized {
			return "", errors.New("session not authorized")
		}
		if s.expired() {
			return "", errors.New("session reached the maximum duration")
		}
		switch senderID {
		case s.requesterID:
			return s.targetID, nil
		case s.targetID:
			return s.requesterID, nil
		default:
			return "", errors.New("sender is not a session participant")
		}
	}
}

// clipboardRelayFixture 三个客户端（requester/target/third）+ 一个已授权
// 会话的公共脚手架。
type clipboardRelayFixture struct {
	hub       *Hub
	requester *Client
	target    *Client
	third     *Client
	sessions  map[string]*fakeAssistSession
}

func newClipboardRelayFixture(t *testing.T) *clipboardRelayFixture {
	t.Helper()
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	f := &clipboardRelayFixture{
		hub:       hub,
		requester: newSIDClient(hub, "ra-requester", "sess-r"),
		target:    newSIDClient(hub, "ra-target", "sess-t"),
		third:     newSIDClient(hub, "ra-third", "sess-x"),
		sessions: map[string]*fakeAssistSession{
			"ra-sess-1": {requesterID: "ra-requester", targetID: "ra-target", authorized: true},
		},
	}
	wireAssistValidators(hub, f.sessions)
	time.Sleep(50 * time.Millisecond)
	for _, c := range []*Client{f.requester, f.target, f.third} {
		drainClient(c)
	}
	return f
}

// recvRaw 从 client 队列取一条消息（500ms 超时）。
func recvRaw(t *testing.T, c *Client) []byte {
	t.Helper()
	select {
	case raw := <-c.send:
		return raw
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("client %s did not receive any message", c.userID)
		return nil
	}
}

// TestClipboardRouting 覆盖 A6-S1（DES-20261001-01 §7.2）。
func TestClipboardRouting(t *testing.T) {
	clipMsg := func(op, reqID, text string) *Message {
		raw := `{"sessionId":"ra-sess-1","targetId":"ra-target","op":"` + op + `"`
		if reqID != "" {
			raw += `,"reqId":"` + reqID + `"`
		}
		if text != "" {
			raw += `,"text":` + jsonQuote(text)
		}
		raw += `}`
		return &Message{Type: "remote_assist_clipboard", Payload: []byte(raw)}
	}
	dataMsg := func(reqID, text string) *Message {
		raw := `{"sessionId":"ra-sess-1","targetId":"ra-target","reqId":` + jsonQuote(reqID) +
			`,"text":` + jsonQuote(text) + `}`
		return &Message{Type: "remote_assist_clipboard_data", Payload: []byte(raw)}
	}

	t.Run("push is routed to the target with server-built shape", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, clipMsg("push", "", "hello clipboard"))

		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"sessionId"`
				Text      string `json:"text"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(recvRaw(t, f.target), &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.Type != "remote_assist_clipboard_push" {
			t.Errorf("type = %s, want remote_assist_clipboard_push", ev.Type)
		}
		if ev.Payload.SessionID != "ra-sess-1" || ev.Payload.Text != "hello clipboard" {
			t.Errorf("payload = %+v", ev.Payload)
		}
		// 控制端自己不收推送（服务端构造的单向流）。
		assertNoMessage(t, f.requester, "requester (no echo)")
		assertNoMessage(t, f.third, "third party")
	})

	t.Run("pull is routed with the client reqId passthrough", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, clipMsg("pull", "req-abc-1", ""))

		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"sessionId"`
				ReqID     string `json:"reqId"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(recvRaw(t, f.target), &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.Type != "remote_assist_clipboard_pull" {
			t.Errorf("type = %s, want remote_assist_clipboard_pull", ev.Type)
		}
		if ev.Payload.ReqID != "req-abc-1" {
			t.Errorf("reqId = %s, want passthrough req-abc-1", ev.Payload.ReqID)
		}
	})

	t.Run("pull without reqId gets a server-generated one", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, clipMsg("pull", "", ""))

		var ev struct {
			Payload struct {
				ReqID string `json:"reqId"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(recvRaw(t, f.target), &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.Payload.ReqID == "" {
			t.Error("server must generate a reqId when the client omits it")
		}
	})

	t.Run("data from the target is routed to the requester", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.target, dataMsg("req-abc-1", "remote content"))

		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"sessionId"`
				ReqID     string `json:"reqId"`
				Text      string `json:"text"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(recvRaw(t, f.requester), &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.Type != "remote_assist_clipboard_data" {
			t.Errorf("type = %s, want remote_assist_clipboard_data", ev.Type)
		}
		if ev.Payload.ReqID != "req-abc-1" || ev.Payload.Text != "remote content" {
			t.Errorf("payload = %+v", ev.Payload)
		}
		// 被控端自己不收回传。
		assertNoMessage(t, f.target, "target (no echo)")
		assertNoMessage(t, f.third, "third party")
	})

	t.Run("clipboard from a non-requester is silently dropped", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		// target 冒充控制端（角色反串）与第三者各发一次。
		f.hub.handleClientMessage(f.target, clipMsg("push", "", "impersonated"))
		f.hub.handleClientMessage(f.third, clipMsg("pull", "req-x", ""))

		assertNoMessage(t, f.target, "target")
		assertNoMessage(t, f.requester, "requester")
		assertNoMessage(t, f.third, "third")
	})

	t.Run("data from a non-target is silently dropped", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		// requester 自己与第三者冒充被控端回传。
		f.hub.handleClientMessage(f.requester, dataMsg("req-abc-1", "forged"))
		f.hub.handleClientMessage(f.third, dataMsg("req-abc-1", "forged"))

		assertNoMessage(t, f.requester, "requester")
		assertNoMessage(t, f.target, "target")
		assertNoMessage(t, f.third, "third")
	})

	t.Run("unknown session is dropped", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, &Message{
			Type:    "remote_assist_clipboard",
			Payload: []byte(`{"sessionId":"ra-sess-404","targetId":"ra-target","op":"push","text":"hi"}`),
		})
		f.hub.handleClientMessage(f.target, &Message{
			Type:    "remote_assist_clipboard_data",
			Payload: []byte(`{"sessionId":"ra-sess-404","reqId":"r","text":"hi"}`),
		})

		assertNoMessage(t, f.target, "target")
		assertNoMessage(t, f.requester, "requester")
	})

	t.Run("text over 32KB is dropped for push and data", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		oversized := strings.Repeat("a", 32*1024+1)
		f.hub.handleClientMessage(f.requester, clipMsg("push", "", oversized))
		f.hub.handleClientMessage(f.target, dataMsg("req-abc-1", oversized))

		assertNoMessage(t, f.target, "target (oversized push)")
		assertNoMessage(t, f.requester, "requester (oversized data)")

		// 边界：恰好 32KB 放行。
		f.hub.handleClientMessage(f.requester, clipMsg("push", "", strings.Repeat("b", 32*1024)))
		recvRaw(t, f.target)
	})

	t.Run("empty push text is dropped", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, clipMsg("push", "", ""))

		assertNoMessage(t, f.target, "target")
	})

	t.Run("dedicated rate limit bucket of 5 per second", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		// 独立桶：前 5 条放行，第 6 条被丢（静默）。
		for i := 0; i < 5; i++ {
			if !f.requester.allowsMessage("remote_assist_clipboard") {
				t.Fatalf("message %d should pass the dedicated bucket", i+1)
			}
		}
		if f.requester.allowsMessage("remote_assist_clipboard") {
			t.Error("message 6 should be dropped by the 5 msg/s dedicated bucket")
		}
		// 被控端回传是独立的一条类型，同样 5 msg/s。
		for i := 0; i < 5; i++ {
			if !f.target.allowsMessage("remote_assist_clipboard_data") {
				t.Fatalf("data message %d should pass", i+1)
			}
		}
		if f.target.allowsMessage("remote_assist_clipboard_data") {
			t.Error("data message 6 should be dropped by its own 5 msg/s bucket")
		}

		// 独立桶「替代」语义：剪贴板耗尽不消耗全局桶，typing 不被挤死。
		if !f.requester.allowsMessage("typing") {
			t.Error("typing must not be affected by the clipboard bucket")
		}
	})

	t.Run("messages are dropped after the M10 duration limit", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		// 会话超 M10：惰性判定口径下校验器直接拒绝。
		f.sessions["ra-sess-1"].deadline = time.Now().Add(-time.Minute)

		f.hub.handleClientMessage(f.requester, clipMsg("push", "", "stale push"))
		f.hub.handleClientMessage(f.requester, clipMsg("pull", "req-stale", ""))
		f.hub.handleClientMessage(f.target, dataMsg("req-stale", "stale data"))

		assertNoMessage(t, f.target, "target after expiry")
		assertNoMessage(t, f.requester, "requester after expiry")
	})

	t.Run("drops when validators are not wired (fail closed)", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)
		requester := newSIDClient(hub, "ra-requester", "sess-r")
		target := newSIDClient(hub, "ra-target", "sess-t")
		time.Sleep(50 * time.Millisecond)
		drainClient(requester)
		drainClient(target)

		// 三个钩子全部保持 nil。
		hub.handleClientMessage(requester, clipMsg("push", "", "hi"))
		hub.handleClientMessage(requester, clipMsg("pull", "req-1", ""))
		hub.handleClientMessage(target, dataMsg("req-1", "hi"))

		assertNoMessage(t, target, "target (unwired)")
		assertNoMessage(t, requester, "requester (unwired)")
	})
}

// TestAssistChatRouting 覆盖 A7-S1（DES-20261001-01 §8.1）。
func TestAssistChatRouting(t *testing.T) {
	chatMsg := func(sessionID, text string) *Message {
		return &Message{
			Type:    "remote_assist_chat",
			Payload: []byte(`{"sessionId":"` + sessionID + `","targetId":"ra-target","text":` + jsonQuote(text) + `}`),
		}
	}

	// expectChat 断言收到一条 remote_assist_chat 且校验 from/sessionId/text。
	expectChat := func(t *testing.T, c *Client, wantFrom, wantText string) {
		t.Helper()
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"sessionId"`
				From      string `json:"from"`
				Text      string `json:"text"`
				At        int64  `json:"at"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(recvRaw(t, c), &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.Type != "remote_assist_chat" {
			t.Fatalf("type = %s, want remote_assist_chat", ev.Type)
		}
		if ev.Payload.From != wantFrom {
			t.Errorf("from = %s, want server-injected %s", ev.Payload.From, wantFrom)
		}
		if ev.Payload.SessionID != "ra-sess-1" {
			t.Errorf("sessionId = %s", ev.Payload.SessionID)
		}
		if wantText != "" && ev.Payload.Text != wantText {
			t.Errorf("text = %s, want %s", ev.Payload.Text, wantText)
		}
		if ev.Payload.At < time.Now().Add(-5*time.Second).UnixMilli() ||
			ev.Payload.At > time.Now().Add(time.Second).UnixMilli() {
			t.Errorf("at = %d, want a current millisecond timestamp", ev.Payload.At)
		}
	}

	t.Run("requester message reaches both parties with injected from", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-1", "hello from controller"))

		expectChat(t, f.requester, "ra-requester", "hello from controller")
		expectChat(t, f.target, "ra-requester", "hello from controller")
		assertNoMessage(t, f.third, "third party")
	})

	t.Run("target message reaches both parties with injected from", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.target, chatMsg("ra-sess-1", "hello from target"))

		expectChat(t, f.target, "ra-target", "hello from target")
		expectChat(t, f.requester, "ra-target", "hello from target")
		assertNoMessage(t, f.third, "third party")
	})

	t.Run("third party is silently dropped", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.third, chatMsg("ra-sess-1", "intruding"))

		assertNoMessage(t, f.requester, "requester")
		assertNoMessage(t, f.target, "target")
		assertNoMessage(t, f.third, "third")
	})

	t.Run("unknown session is dropped", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-404", "hi"))

		assertNoMessage(t, f.requester, "requester")
		assertNoMessage(t, f.target, "target")
	})

	t.Run("oversized text is dropped (2000 runes and 8KB guards)", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		// 2001 个 rune（多字节字符，同时 4004 字节未超 8KB —— rune 闸门生效）。
		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-1", strings.Repeat("汉", 2001)))
		// 8KB+1 字节（ASCII，仅 8193 rune —— 字节闸门先于 rune 触发）。
		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-1", strings.Repeat("a", 8*1024+1)))
		// 空文本。
		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-1", ""))

		assertNoMessage(t, f.requester, "requester")
		assertNoMessage(t, f.target, "target")

		// 边界：恰好 2000 rune 放行。
		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-1", strings.Repeat("汉", 2000)))
		expectChat(t, f.target, "ra-requester", "")
		expectChat(t, f.requester, "ra-requester", "")
	})

	t.Run("dedicated rate limit bucket of 10 per second", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		for i := 0; i < 10; i++ {
			if !f.requester.allowsMessage("remote_assist_chat") {
				t.Fatalf("message %d should pass the dedicated bucket", i+1)
			}
		}
		if f.requester.allowsMessage("remote_assist_chat") {
			t.Error("message 11 should be dropped by the 10 msg/s dedicated bucket")
		}
		// 聊天耗尽不影响全局桶（typing 可用）。
		if !f.requester.allowsMessage("typing") {
			t.Error("typing must not be affected by the chat bucket")
		}
	})

	t.Run("messages are dropped after the session ends (M10)", func(t *testing.T) {
		f := newClipboardRelayFixture(t)

		f.sessions["ra-sess-1"].deadline = time.Now().Add(-time.Minute)

		f.hub.handleClientMessage(f.requester, chatMsg("ra-sess-1", "stale"))
		f.hub.handleClientMessage(f.target, chatMsg("ra-sess-1", "stale too"))

		assertNoMessage(t, f.requester, "requester after expiry")
		assertNoMessage(t, f.target, "target after expiry")
	})

	t.Run("drops when the validator is not wired (fail closed)", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)
		requester := newSIDClient(hub, "ra-requester", "sess-r")
		target := newSIDClient(hub, "ra-target", "sess-t")
		time.Sleep(50 * time.Millisecond)
		drainClient(requester)
		drainClient(target)

		hub.handleClientMessage(requester, chatMsg("ra-sess-1", "hi"))

		assertNoMessage(t, requester, "requester (unwired)")
		assertNoMessage(t, target, "target (unwired)")
	})
}

// jsonQuote 把字符串安全地编码为 JSON 字符串字面量（测试辅助，避免手拼
// 转义出错）。
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
