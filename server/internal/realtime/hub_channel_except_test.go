package realtime

// A9 二期（DES-20261029-01 §7.3 R1）：BroadcastToChannelExcept 的定向排除语义。
// 线程未读增量事件必须「发给频道订阅者，但跳过作者本人」；既有
// BroadcastToChannel 行为必须保持不变。

import (
	"testing"
	"time"

	"ridgericetalk/tests/testutil"
)

func TestBroadcastToChannelExceptSkipsExcludedUser(t *testing.T) {
	hub := NewHub(testutil.TestLogger())
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	newClient := func(userID string) *Client {
		c := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      userID,
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- c
		time.Sleep(20 * time.Millisecond)
		c.subscribe("ch-thread")
		time.Sleep(20 * time.Millisecond)
		return c
	}
	author := newClient("user-author")
	reader := newClient("user-reader")

	msg := []byte(`{"type":"thread_unread_changed","payload":{"delta":1}}`)
	hub.BroadcastToChannelExcept("ch-thread", "user-author", msg)

	// 订阅者照收。
	select {
	case got := <-reader.send:
		if string(got) != string(msg) {
			t.Errorf("reader got %s, want %s", got, msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("reader did not receive channel-except broadcast")
	}

	// 被排除者（作者本人）不收。
	select {
	case got := <-author.send:
		t.Fatalf("author must be skipped but received: %s", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestBroadcastToChannelExceptEmptyExcludeDoesNotSkip(t *testing.T) {
	hub := NewHub(testutil.TestLogger())
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	c := &Client{
		hub:         hub,
		send:        make(chan []byte, 256),
		userID:      "user-1",
		channels:    make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}
	hub.register <- c
	time.Sleep(20 * time.Millisecond)
	c.subscribe("ch-empty")
	time.Sleep(20 * time.Millisecond)

	msg := []byte(`{"type":"thread_unread_changed"}`)
	hub.BroadcastToChannelExcept("ch-empty", "", msg)

	select {
	case got := <-c.send:
		if string(got) != string(msg) {
			t.Errorf("got %s, want %s", got, msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("empty excludeUserID must not skip any subscriber")
	}
}

// TestBroadcastToChannelUnchanged 守护既有 BroadcastToChannel 语义：
// 即使频道内存在与 excludeUserID 无关的调用，普通广播也必须送达所有订阅者。
func TestBroadcastToChannelUnchanged(t *testing.T) {
	hub := NewHub(testutil.TestLogger())
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	c := &Client{
		hub:         hub,
		send:        make(chan []byte, 256),
		userID:      "user-1",
		channels:    make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}
	hub.register <- c
	time.Sleep(20 * time.Millisecond)
	c.subscribe("ch-plain")
	time.Sleep(20 * time.Millisecond)

	msg := []byte(`{"type":"message"}`)
	hub.BroadcastToChannel("ch-plain", msg)

	select {
	case got := <-c.send:
		if string(got) != string(msg) {
			t.Errorf("got %s, want %s", got, msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("plain BroadcastToChannel must still deliver")
	}
}
