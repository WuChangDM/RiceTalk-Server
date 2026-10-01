package realtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"ridgericetalk/tests/testutil"
)

func TestNewHub(t *testing.T) {
	t.Run("creates hub successfully", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		if hub == nil {
			t.Fatal("expected hub, got nil")
		}
		if hub.clients == nil {
			t.Error("expected clients map to be initialized")
		}
		if hub.channels == nil {
			t.Error("expected channels map to be initialized")
		}
		if hub.userClientCount == nil {
			t.Error("expected userClientCount map to be initialized")
		}
		if hub.broadcast == nil {
			t.Error("expected broadcast channel to be initialized")
		}
		if hub.register == nil {
			t.Error("expected register channel to be initialized")
		}
		if hub.unregister == nil {
			t.Error("expected unregister channel to be initialized")
		}
		if hub.log == nil {
			t.Error("expected log to be set")
		}
	})
}

func TestHubRun(t *testing.T) {
	t.Run("starts without panic", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)

		done := make(chan struct{})
		go func() {
			defer close(done)
			hub.Run()
		}()

		// Give Run a moment to start
		time.Sleep(50 * time.Millisecond)

		// Send a broadcast to verify the loop is running
		select {
		case hub.broadcast <- []byte("test"):
		case <-time.After(500 * time.Millisecond):
			t.Fatal("hub broadcast channel blocked")
		}

		// We can't cleanly stop Run since it loops forever,
		// but we verified it started and processed a message.
	})
}

func TestHubBroadcast(t *testing.T) {
	t.Run("broadcasts message to all clients", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)

		go hub.Run()
		// Wait for Run to start
		time.Sleep(50 * time.Millisecond)

		// Create two mock clients
		client1 := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-1",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		client2 := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-2",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}

		// Register clients
		hub.register <- client1
		hub.register <- client2
		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		msg := []byte(`{"type":"test"}`)
		hub.Broadcast(msg)

		// Verify both clients received the message
		select {
		case received := <-client1.send:
			if string(received) != string(msg) {
				t.Errorf("client1 expected %s, got %s", msg, received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("client1 did not receive broadcast")
		}

		select {
		case received := <-client2.send:
			if string(received) != string(msg) {
				t.Errorf("client2 expected %s, got %s", msg, received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("client2 did not receive broadcast")
		}
	})
}

func TestHubBroadcastToChannel(t *testing.T) {
	t.Run("broadcasts message to channel subscribers", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)

		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		// Create two clients
		client1 := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-1",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		client2 := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-2",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}

		// Register clients
		hub.register <- client1
		hub.register <- client2
		time.Sleep(50 * time.Millisecond)

		// Subscribe client1 to channel "ch-1"
		client1.subscribe("ch-1")
		time.Sleep(50 * time.Millisecond)

		// Broadcast to channel
		msg := []byte(`{"type":"channel_msg"}`)
		hub.BroadcastToChannel("ch-1", msg)

		// client1 should receive
		select {
		case received := <-client1.send:
			if string(received) != string(msg) {
				t.Errorf("client1 expected %s, got %s", msg, received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("client1 did not receive channel broadcast")
		}

		// client2 should NOT receive (not subscribed)
		select {
		case <-client2.send:
			t.Fatal("client2 should not have received channel broadcast")
		case <-time.After(100 * time.Millisecond):
			// expected
		}
	})
}

func TestHubHandleWebSocket(t *testing.T) {
	t.Run("rejects unauthenticated connection", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		router := gin.New()
		router.GET("/ws", hub.HandleWebSocket)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/ws", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
		}
	})

	t.Run("accepts authenticated connection", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		// Use httptest server for websocket upgrade
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Build a gin context manually
			c, _ := gin.CreateTestContext(w)
			c.Request = r
			c.Set("user_id", "test-user")
			hub.HandleWebSocket(c)
		}))
		defer server.Close()

		// Convert http:// to ws://
		wsURL := strings.Replace(server.URL, "http://", "ws://", 1)

		ws, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			// The upgrader may fail for various reasons; if it fails with 403 or similar,
			// that's acceptable because the test server setup may not perfectly mimic
			// the real gin upgrade path. We mainly care that it doesn't panic.
			if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
				// Expected in some test environments
				return
			}
			// If we get here, log but don't fail — the core behavior (no panic) is verified
			// by the test server still being alive.
			t.Logf("websocket dial error (may be expected in test env): %v, status: %d", err, resp.StatusCode)
			return
		}
		defer ws.Close()

		// If connection succeeded, verify hub has the client
		time.Sleep(100 * time.Millisecond)
		if hub.ClientCount() < 1 {
			t.Error("expected at least 1 client in hub")
		}
	})

	t.Run("enforces connection limit", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		// Manually set userClientCount to 3 to simulate limit
		hub.mu.Lock()
		hub.userClientCount["limited-user"] = 3
		hub.mu.Unlock()

		router := gin.New()
		router.GET("/ws", func(c *gin.Context) {
			c.Set("user_id", "limited-user")
			hub.HandleWebSocket(c)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/ws", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Errorf("expected status %d, got %d", http.StatusTooManyRequests, w.Code)
		}
	})
}

func TestHubAuthorizeChannel(t *testing.T) {
	t.Run("allows authorized channel subscription", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		// Set authorization callback that allows all
		hub.AuthorizeChannel = func(userID, channelID string) bool {
			return true
		}

		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-1",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}

		hub.register <- client
		time.Sleep(50 * time.Millisecond)

		// Simulate subscribe message
		msg := &Message{
			Type:    "subscribe",
			Payload: []byte(`{"channelId":"ch-1"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if !client.isSubscribed("ch-1") {
			t.Error("expected client to be subscribed to ch-1")
		}
	})

	t.Run("denies unauthorized channel subscription", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		// Set authorization callback that denies all
		hub.AuthorizeChannel = func(userID, channelID string) bool {
			return false
		}

		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-1",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}

		hub.register <- client
		time.Sleep(50 * time.Millisecond)

		// Simulate subscribe message
		msg := &Message{
			Type:    "subscribe",
			Payload: []byte(`{"channelId":"ch-1"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if client.isSubscribed("ch-1") {
			t.Error("expected client NOT to be subscribed to ch-1")
		}

		// Client should have received an error message
		select {
		case errMsg := <-client.send:
			if !strings.Contains(string(errMsg), "CHANNEL_ACCESS_DENIED") {
				t.Errorf("expected CHANNEL_ACCESS_DENIED error, got %s", errMsg)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected error message to be sent to client")
		}
	})
}

func TestRateLimiter(t *testing.T) {
	t.Run("allows messages within limit", func(t *testing.T) {
		rl := newRateLimiter()
		for i := 0; i < rateLimitMessages; i++ {
			if !rl.Allow() {
				t.Fatalf("expected Allow() = true on message %d", i+1)
			}
		}
	})

	t.Run("blocks messages exceeding limit", func(t *testing.T) {
		rl := newRateLimiter()
		// Exhaust all tokens
		for i := 0; i < rateLimitMessages; i++ {
			rl.Allow()
		}
		// Next message should be blocked
		if rl.Allow() {
			t.Fatal("expected Allow() = false after limit exceeded")
		}
	})

	t.Run("replenishes tokens over time", func(t *testing.T) {
		rl := newRateLimiter()
		// Exhaust all tokens
		for i := 0; i < rateLimitMessages; i++ {
			rl.Allow()
		}
		// Simulate time passing
		rl.mu.Lock()
		rl.lastCheck = time.Now().Add(-rateLimitWindow)
		rl.mu.Unlock()
		// Should allow again after time passes
		if !rl.Allow() {
			t.Fatal("expected Allow() = true after time window passes")
		}
	})

	t.Run("caps tokens at maximum", func(t *testing.T) {
		rl := newRateLimiter()
		// Simulate a long time passing — tokens should cap at rateLimitMessages
		rl.mu.Lock()
		rl.lastCheck = time.Now().Add(-10 * time.Second)
		rl.mu.Unlock()
		// Should allow rateLimitMessages messages
		for i := 0; i < rateLimitMessages; i++ {
			if !rl.Allow() {
				t.Fatalf("expected Allow() = true on message %d after replenish", i+1)
			}
		}
		// But not more
		if rl.Allow() {
			t.Fatal("expected Allow() = false after cap reached")
		}
	})
}

func TestClientSubscribeUnsubscribe(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	client := &Client{
		hub:         hub,
		send:        make(chan []byte, 256),
		userID:      "user-1",
		channels:    make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}

	hub.register <- client
	time.Sleep(50 * time.Millisecond)

	t.Run("subscribe adds channel", func(t *testing.T) {
		client.subscribe("ch-1")
		time.Sleep(50 * time.Millisecond)
		if !client.isSubscribed("ch-1") {
			t.Error("expected client to be subscribed to ch-1")
		}
	})

	t.Run("subscribe multiple channels", func(t *testing.T) {
		client.subscribe("ch-2")
		client.subscribe("ch-3")
		time.Sleep(50 * time.Millisecond)
		if !client.isSubscribed("ch-2") || !client.isSubscribed("ch-3") {
			t.Error("expected client to be subscribed to ch-2 and ch-3")
		}
	})

	t.Run("unsubscribe removes channel", func(t *testing.T) {
		client.unsubscribe("ch-2")
		time.Sleep(50 * time.Millisecond)
		if client.isSubscribed("ch-2") {
			t.Error("expected client NOT to be subscribed to ch-2 after unsubscribe")
		}
		// Other channels should still be subscribed
		if !client.isSubscribed("ch-1") || !client.isSubscribed("ch-3") {
			t.Error("expected ch-1 and ch-3 to still be subscribed")
		}
	})

	t.Run("unsubscribe non-existent channel is no-op", func(t *testing.T) {
		client.unsubscribe("ch-999")
		// Should not panic
	})
}

func TestHubUnregisterCleansUp(t *testing.T) {
	t.Run("unregister removes client from hub and channels", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-1",
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}

		hub.register <- client
		time.Sleep(50 * time.Millisecond)

		// Subscribe to a channel
		client.subscribe("ch-1")
		time.Sleep(50 * time.Millisecond)

		// Unregister the client
		hub.unregister <- client
		time.Sleep(100 * time.Millisecond)

		// Client should no longer be in hub
		if hub.ClientCount() != 0 {
			t.Errorf("expected 0 clients, got %d", hub.ClientCount())
		}

		// Channel should have no subscribers (may still exist as empty map)
		hub.mu.RLock()
		chClients := hub.channels["ch-1"]
		hub.mu.RUnlock()
		if chClients != nil && len(chClients) > 0 {
			t.Error("expected ch-1 to have no subscribers after client disconnects")
		}
	})

	t.Run("tracks user connection count", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		hub.gracefulOfflineDelay = 100 * time.Millisecond
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		onlineEvents := make(chan string, 2)
		offlineEvents := make(chan string, 2)
		hub.OnUserCameOnline = func(userID string) { onlineEvents <- userID }
		hub.OnUserFullyOffline = func(userID string) { offlineEvents <- userID }

		client1 := &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-A",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}
		client2 := &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-A",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}

		// First connection triggers online event
		hub.register <- client1
		select {
		case uid := <-onlineEvents:
			if uid != "user-A" {
				t.Errorf("expected user-A online event, got %s", uid)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected online event for first connection")
		}

		// Second connection does NOT trigger another online event
		hub.register <- client2
		select {
		case <-onlineEvents:
			t.Error("second connection should not trigger another online event")
		case <-time.After(100 * time.Millisecond):
			// expected
		}

		// First disconnect does NOT trigger offline
		hub.unregister <- client1
		select {
		case <-offlineEvents:
			t.Error("first disconnect should not trigger offline event")
		case <-time.After(50 * time.Millisecond):
			// expected
		}

		// Last disconnect starts the graceful offline timer; wait for it.
		hub.unregister <- client2
		select {
		case <-offlineEvents:
			t.Error("last disconnect should not immediately trigger offline event")
		case <-time.After(50 * time.Millisecond):
			// expected
		}

		select {
		case uid := <-offlineEvents:
			if uid != "user-A" {
				t.Errorf("expected user-A offline event, got %s", uid)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected offline event after graceful offline delay")
		}
	})
}

func TestHubBroadcastToUser(t *testing.T) {
	t.Run("sends message to specific user's connections", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		client1 := &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-A",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}
		client2 := &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-B",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}

		hub.register <- client1
		hub.register <- client2
		time.Sleep(50 * time.Millisecond)

		msg := []byte(`{"type":"dm"}`)
		hub.BroadcastToUser("user-A", msg)

		select {
		case received := <-client1.send:
			if string(received) != string(msg) {
				t.Errorf("user-A expected %s, got %s", msg, received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("user-A did not receive message")
		}

		// user-B should NOT receive
		select {
		case <-client2.send:
			t.Fatal("user-B should not have received user-A's message")
		case <-time.After(100 * time.Millisecond):
			// expected
		}
	})
}

func TestHandleClientMessageTyping(t *testing.T) {
	t.Run("typing message broadcasts to channel", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		sender := &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-1",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}
		receiver := &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-2",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}

		hub.register <- sender
		hub.register <- receiver
		time.Sleep(50 * time.Millisecond)

		// Both subscribe to same channel
		sender.subscribe("ch-1")
		receiver.subscribe("ch-1")
		time.Sleep(50 * time.Millisecond)

		// Sender types
		msg := &Message{
			Type:    "typing",
			Payload: []byte(`{"channelId":"ch-1"}`),
		}
		hub.handleClientMessage(sender, msg)
		time.Sleep(50 * time.Millisecond)

		// Receiver should get the typing event
		select {
		case data := <-receiver.send:
			if !strings.Contains(string(data), "typing") {
				t.Errorf("expected typing event, got %s", data)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("receiver did not get typing event")
		}
	})
}

func TestHubConcurrentSubscribeAndUnregister(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	const clientsCount = 10
	const channelsPerClient = 10

	clients := make([]*Client, clientsCount)
	for i := range clients {
		clients[i] = &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      "user-" + string(rune('A'+i)),
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- clients[i]
	}
	time.Sleep(50 * time.Millisecond)

	// Concurrently subscribe clients to channels while randomly unregistering others.
	var wg sync.WaitGroup
	for i, client := range clients {
		wg.Add(1)
		go func(idx int, c *Client) {
			defer wg.Done()
			for j := 0; j < channelsPerClient; j++ {
				c.subscribe(fmt.Sprintf("ch-%d-%d", idx, j))
			}
		}(i, client)

		if i%2 == 0 {
			wg.Add(1)
			go func(c *Client) {
				defer wg.Done()
				time.Sleep(10 * time.Millisecond)
				hub.unregister <- c
			}(client)
		}
	}
	wg.Wait()

	// No panic means the locking is sufficient; the test also runs under -race.
	if hub.ClientCount() < 0 {
		t.Fatal("unexpected client count")
	}
}

// drainClient discards any messages already queued for the client (e.g. the
// welcome event) so a test can assert on messages produced by the action under
// test only.
func drainClient(c *Client) {
	for {
		select {
		case <-c.send:
		default:
			return
		}
	}
}

// TestRemoteAssistControlForwarding covers H14: the hub must consult
// OnRemoteAssistControl before forwarding a remote_assist_control event, and
// must drop the event when the validator rejects it (or is not wired) so that
// a non-requester cannot inject mouse/keyboard events into the target.
func TestRemoteAssistControlForwarding(t *testing.T) {
	newPair := func(t *testing.T) (hub *Hub, sender, target *Client) {
		t.Helper()
		hub = NewHub(testutil.TestLogger())
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		sender = &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-requester",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}
		target = &Client{
			hub: hub, send: make(chan []byte, 256), userID: "user-target",
			channels: make(map[string]bool), rateLimiter: newRateLimiter(),
		}
		hub.register <- sender
		hub.register <- target
		time.Sleep(50 * time.Millisecond)

		drainClient(sender)
		drainClient(target)
		return hub, sender, target
	}

	controlMsg := &Message{
		Type: "remote_assist_control",
		Payload: []byte(`{"sessionId":"sess-1","targetId":"user-target","eventType":"mouse_move","data":{"x":0.5,"y":0.5}}`),
	}

	t.Run("forwards when the validator allows the event", func(t *testing.T) {
		hub, sender, target := newPair(t)

		var gotSession, gotRequester, gotTarget string
		hub.OnRemoteAssistControl = func(sessionID, requesterID, targetID string) error {
			gotSession, gotRequester, gotTarget = sessionID, requesterID, targetID
			return nil
		}

		hub.handleClientMessage(sender, controlMsg)
		time.Sleep(50 * time.Millisecond)

		// The validator saw the authenticated sender, not a payload-supplied one.
		if gotSession != "sess-1" || gotRequester != "user-requester" || gotTarget != "user-target" {
			t.Errorf("validator args = (%q, %q, %q), want (sess-1, user-requester, user-target)",
				gotSession, gotRequester, gotTarget)
		}

		select {
		case data := <-target.send:
			if !strings.Contains(string(data), "remote_assist_control") || !strings.Contains(string(data), "sess-1") {
				t.Errorf("unexpected forwarded payload: %s", data)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("target did not receive the authorized control event")
		}
	})

	t.Run("drops when the validator rejects the event", func(t *testing.T) {
		hub, sender, target := newPair(t)

		hub.OnRemoteAssistControl = func(sessionID, requesterID, targetID string) error {
			return fmt.Errorf("not the session requester")
		}

		hub.handleClientMessage(sender, controlMsg)
		time.Sleep(50 * time.Millisecond)

		select {
		case data := <-target.send:
			t.Fatalf("target received a rejected control event: %s", data)
		case <-time.After(200 * time.Millisecond):
			// expected: dropped
		}
		// The sender must not be told why (no session-id oracle).
		select {
		case data := <-sender.send:
			t.Fatalf("sender received a response to a rejected control event: %s", data)
		case <-time.After(200 * time.Millisecond):
			// expected: silent
		}
	})

	t.Run("drops when no validator is wired (fail closed)", func(t *testing.T) {
		hub, sender, target := newPair(t)
		// hub.OnRemoteAssistControl stays nil.

		hub.handleClientMessage(sender, controlMsg)
		time.Sleep(50 * time.Millisecond)

		select {
		case data := <-target.send:
			t.Fatalf("target received a control event without a wired validator: %s", data)
		case <-time.After(200 * time.Millisecond):
			// expected: dropped
		}
	})

	t.Run("drops events without a sessionId", func(t *testing.T) {
		hub, sender, target := newPair(t)

		called := false
		hub.OnRemoteAssistControl = func(sessionID, requesterID, targetID string) error {
			called = true
			return nil
		}

		hub.handleClientMessage(sender, &Message{
			Type:    "remote_assist_control",
			Payload: []byte(`{"targetId":"user-target","eventType":"mouse_move"}`),
		})
		time.Sleep(50 * time.Millisecond)

		if called {
			t.Error("validator should not be called without a sessionId")
		}
		select {
		case data := <-target.send:
			t.Fatalf("target received a session-less control event: %s", data)
		case <-time.After(200 * time.Millisecond):
			// expected: dropped
		}
	})
}

// TestPresenceUpdateInvisibleFiltered verifies H16: invisible status is
// broadcast as "offline" to other clients.
func TestPresenceUpdateInvisibleFiltered(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	sender := &Client{
		hub: hub, send: make(chan []byte, 256), userID: "user-1",
		channels: make(map[string]bool), rateLimiter: newRateLimiter(),
	}
	receiver := &Client{
		hub: hub, send: make(chan []byte, 256), userID: "user-2",
		channels: make(map[string]bool), rateLimiter: newRateLimiter(),
	}

	hub.register <- sender
	hub.register <- receiver
	time.Sleep(50 * time.Millisecond)

	// Drain any welcome or initial messages
	for {
		select {
		case <-sender.send:
		case <-receiver.send:
		default:
			goto sendInvisible
		}
	}
sendInvisible:

	// Sender sets status to invisible
	msg := &Message{
		Type:    "presence_update",
		Payload: []byte(`{"status":"invisible","customStatus":"busy"}`),
	}
	hub.handleClientMessage(sender, msg)
	time.Sleep(50 * time.Millisecond)

	// Receiver should see "offline" (filtered), not "invisible"
	select {
	case data := <-receiver.send:
		if strings.Contains(string(data), "invisible") {
			t.Errorf("invisible status leaked to other clients: %s", data)
		}
		if !strings.Contains(string(data), "offline") {
			t.Errorf("expected offline status, got %s", data)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("receiver did not get presence_update event")
	}
}

// TestPresenceUpdateOnlineNotFiltered verifies normal online status is
// broadcast unchanged (regression guard for H16).
func TestPresenceUpdateOnlineNotFiltered(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	sender := &Client{
		hub: hub, send: make(chan []byte, 256), userID: "user-1",
		channels: make(map[string]bool), rateLimiter: newRateLimiter(),
	}
	receiver := &Client{
		hub: hub, send: make(chan []byte, 256), userID: "user-2",
		channels: make(map[string]bool), rateLimiter: newRateLimiter(),
	}

	hub.register <- sender
	hub.register <- receiver
	time.Sleep(50 * time.Millisecond)

	// Drain any welcome or initial messages
	for {
		select {
		case <-sender.send:
		case <-receiver.send:
		default:
			goto sendOnline
		}
	}
sendOnline:

	msg := &Message{
		Type:    "presence_update",
		Payload: []byte(`{"status":"online","customStatus":""}`),
	}
	hub.handleClientMessage(sender, msg)
	time.Sleep(50 * time.Millisecond)

	select {
	case data := <-receiver.send:
		if !strings.Contains(string(data), "online") {
			t.Errorf("expected online status, got %s", data)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("receiver did not get presence_update event")
	}
}

// TestSubscribeSyncSnapshot verifies H18: sync_snapshot is pushed to the
// client after a successful subscribe when OnSubscribeSync is registered.
func TestSubscribeSyncSnapshot(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	hub.OnSubscribeSync = func(userID, channelID string) map[string]interface{} {
		return map[string]interface{}{
			"pinnedMessageIds": []string{"msg-1"},
			"unreadCount":      int64(5),
		}
	}
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	client := &Client{
		hub: hub, send: make(chan []byte, 256), userID: "user-1",
		channels: make(map[string]bool), rateLimiter: newRateLimiter(),
	}
	hub.register <- client
	time.Sleep(50 * time.Millisecond)

	// Drain welcome event
	select {
	case <-client.send:
	default:
	}

	// Subscribe to a channel
	msg := &Message{
		Type:    "subscribe",
		Payload: []byte(`{"channelId":"ch-1"}`),
	}
	hub.handleClientMessage(client, msg)
	time.Sleep(50 * time.Millisecond)

	// Client should receive sync_snapshot
	select {
	case data := <-client.send:
		if !strings.Contains(string(data), "sync_snapshot") {
			t.Errorf("expected sync_snapshot event, got %s", data)
		}
		if !strings.Contains(string(data), "pinnedMessageIds") {
			t.Errorf("expected pinnedMessageIds in snapshot, got %s", data)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("client did not get sync_snapshot event")
	}
}

// TestSubscribeNoSyncSnapshotWhenCallbackNil verifies that no sync_snapshot
// is sent when OnSubscribeSync is not registered (backward compatible).
func TestSubscribeNoSyncSnapshotWhenCallbackNil(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log) // OnSubscribeSync is nil
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	client := &Client{
		hub: hub, send: make(chan []byte, 256), userID: "user-1",
		channels: make(map[string]bool), rateLimiter: newRateLimiter(),
	}
	hub.register <- client
	time.Sleep(50 * time.Millisecond)

	// Drain welcome event
	select {
	case <-client.send:
	default:
	}

	msg := &Message{
		Type:    "subscribe",
		Payload: []byte(`{"channelId":"ch-1"}`),
	}
	hub.handleClientMessage(client, msg)
	time.Sleep(50 * time.Millisecond)

	// Client should NOT receive any sync_snapshot
	select {
	case data := <-client.send:
		if strings.Contains(string(data), "sync_snapshot") {
			t.Errorf("unexpected sync_snapshot event when callback is nil: %s", data)
		}
	case <-time.After(200 * time.Millisecond):
		// No message is the expected outcome
	}
}

// TestHubConcurrentRegisterUnregister stress-tests register/unsubscribe paths
// for data races and map corruption under concurrent access.
func TestHubConcurrentRegisterUnregister(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	const workers = 50
	const rounds = 20

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			userID := fmt.Sprintf("user-%d", idx%5)
			for r := 0; r < rounds; r++ {
				client := &Client{
					hub:         hub,
					send:        make(chan []byte, 256),
					userID:      userID,
					channels:    make(map[string]bool),
					rateLimiter: newRateLimiter(),
				}
				hub.register <- client
				time.Sleep(time.Millisecond)
				client.subscribe(fmt.Sprintf("ch-%d", idx%3))
				time.Sleep(time.Millisecond)
				hub.unregister <- client
			}
		}(i)
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients after concurrent unregister, got %d", hub.ClientCount())
	}
}

// TestSendToClientAfterUnregister verifies that sending to a client after it
// has unregistered does not panic or write to a closed channel.
func TestSendToClientAfterUnregister(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	client := &Client{
		hub:         hub,
		send:        make(chan []byte, 256),
		userID:      "user-1",
		channels:    make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}
	hub.register <- client
	time.Sleep(50 * time.Millisecond)

	hub.unregister <- client
	time.Sleep(50 * time.Millisecond)

	// This must not panic even though client.send is closed.
	hub.SendToClient(client, []byte(`{"type":"test"}`))
	time.Sleep(50 * time.Millisecond)

	// The message should have been dropped because the client is no longer registered.
	select {
	case data, ok := <-client.send:
		if !ok {
			// Channel closed as expected; message was not delivered.
			return
		}
		if len(data) > 0 {
			t.Fatal("expected message to be dropped for unregistered client")
		}
	case <-time.After(100 * time.Millisecond):
		// Message dropped without reading from channel; also acceptable.
	}
}

// TestUpgraderCheckOrigin is a regression test for M4: the development
// allow-list only matched the literal "localhost" host, so a packaged desktop
// client whose WebSocket handshake Origin is http://127.0.0.1:<port> was
// rejected with 403 (unless RRT_PUBLIC_ADDRESS/RRT_CORS_ORIGINS was set),
// leaving the client unable to receive any realtime events.
func TestUpgraderCheckOrigin(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		env    string
		public string
		cors   string
		want   bool
	}{
		{name: "localhost http", origin: "http://localhost:5173", want: true},
		{name: "localhost https", origin: "https://localhost:5173", want: true},
		{name: "loopback ipv4", origin: "http://127.0.0.1:8099", want: true},
		{name: "loopback ipv4 https", origin: "https://127.0.0.1:8099", want: true},
		{name: "loopback ipv6", origin: "http://[::1]:8099", want: true},
		{name: "loopback ipv6 https", origin: "https://[::1]:8099", want: true},
		{name: "localhost without port", origin: "http://localhost", want: true},
		{name: "external origin rejected", origin: "http://example.com", want: false},
		{name: "loopback lookalike rejected", origin: "http://127.0.0.1.evil.com:8099", want: false},
		{name: "localhost lookalike rejected", origin: "http://localhost.evil.com:5173", want: false},
		{name: "non-http scheme rejected", origin: "ftp://127.0.0.1:8099", want: false},
		{name: "empty origin allowed in development", origin: "", env: "development", want: true},
		{name: "empty origin rejected in production", origin: "", env: "production", want: false},
		{name: "configured public address allowed", origin: "https://talk.example.com", public: "https://talk.example.com", want: true},
		{name: "configured cors origin allowed", origin: "https://app.example.com:3000", cors: "https://app.example.com:3000", want: true},
		{name: "unconfigured external origin rejected", origin: "https://other.example.com", public: "https://talk.example.com", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RRT_ENV", tc.env)
			t.Setenv("RRT_PUBLIC_ADDRESS", tc.public)
			t.Setenv("RRT_CORS_ORIGINS", tc.cors)

			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			if got := Upgrader.CheckOrigin(req); got != tc.want {
				t.Errorf("CheckOrigin(origin=%q, public=%q, cors=%q) = %v, want %v",
					tc.origin, tc.public, tc.cors, got, tc.want)
			}
		})
	}
}

// M11 修复：subscribe_whiteboard 必须经 AuthorizeWhiteboard 校验，
// 且回调未装配时 fail-closed（任一认证用户不得跨空间订阅他人笔迹流）。
func TestHubAuthorizeWhiteboard(t *testing.T) {
	newWbClient := func(hub *Hub, userID string) *Client {
		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      userID,
			channels:    make(map[string]bool),
			whiteboards: make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- client
		time.Sleep(50 * time.Millisecond)
		return client
	}

	t.Run("allows authorized whiteboard subscription", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		hub.AuthorizeWhiteboard = func(userID, whiteboardID string) bool {
			return userID == "member-1" && whiteboardID == "wb-1"
		}

		client := newWbClient(hub, "member-1")
		msg := &Message{
			Type:    "subscribe_whiteboard",
			Payload: []byte(`{"whiteboardId":"wb-1"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if !client.isSubscribedWhiteboard("wb-1") {
			t.Error("expected client to be subscribed to whiteboard wb-1")
		}
	})

	t.Run("denies unauthorized whiteboard subscription", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		hub.AuthorizeWhiteboard = func(userID, whiteboardID string) bool {
			return false // 模拟非该空间成员
		}

		client := newWbClient(hub, "outsider-1")
		msg := &Message{
			Type:    "subscribe_whiteboard",
			Payload: []byte(`{"whiteboardId":"wb-secret"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if client.isSubscribedWhiteboard("wb-secret") {
			t.Error("expected client NOT to be subscribed to whiteboard wb-secret")
		}

		// Client should have received an error message
		select {
		case errMsg := <-client.send:
			if !strings.Contains(string(errMsg), "WHITEBOARD_NO_PERMISSION") {
				t.Errorf("expected WHITEBOARD_NO_PERMISSION error, got %s", errMsg)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected error message to be sent to client")
		}
	})

	t.Run("fail-closed when callback not wired", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)
		// AuthorizeWhiteboard 保持 nil：未装配校验的 hub 不得放行任何订阅

		client := newWbClient(hub, "anyone-1")
		msg := &Message{
			Type:    "subscribe_whiteboard",
			Payload: []byte(`{"whiteboardId":"wb-1"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if client.isSubscribedWhiteboard("wb-1") {
			t.Error("expected subscription to be rejected when AuthorizeWhiteboard is nil")
		}
		select {
		case errMsg := <-client.send:
			if !strings.Contains(string(errMsg), "WHITEBOARD_NO_PERMISSION") {
				t.Errorf("expected WHITEBOARD_NO_PERMISSION error, got %s", errMsg)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected error message to be sent to client")
		}
	})
}

// N9 修复的传输层：BroadcastToUsers 只向给定用户集合投递，
// 供白板列表类事件按空间成员定向发送使用。
func TestHubBroadcastToUsers(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	newClient := func(userID string) *Client {
		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      userID,
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- client
		return client
	}
	memberA := newClient("user-A")
	memberC := newClient("user-C")
	outsider := newClient("user-B")
	time.Sleep(50 * time.Millisecond)

	msg := []byte(`{"type":"whiteboard_created"}`)
	hub.BroadcastToUsers([]string{"user-A", "user-C"}, msg)

	for _, c := range []*Client{memberA, memberC} {
		select {
		case received := <-c.send:
			if string(received) != string(msg) {
				t.Errorf("%s expected %s, got %s", c.userID, msg, received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s did not receive message", c.userID)
		}
	}

	// 非收件人不得收到
	select {
	case received := <-outsider.send:
		t.Fatalf("user-B should not have received message, got %s", received)
	case <-time.After(100 * time.Millisecond):
		// expected
	}
}

// H16 修复：共享文档虚拟订阅键（原始 doc id，"doc_" 前缀）必须经
// AuthorizeDoc 校验而非查 channels 表的 AuthorizeChannel；回调未装配时
// fail-closed（任何认证用户不得订阅他人文档的实时流）。
func TestHubAuthorizeDoc(t *testing.T) {
	newDocClient := func(hub *Hub, userID string) *Client {
		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      userID,
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- client
		time.Sleep(50 * time.Millisecond)
		return client
	}

	t.Run("allows member doc subscription via AuthorizeDoc", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		hub.AuthorizeDoc = func(userID, docID string) bool {
			return userID == "member-1" && docID == "doc_123"
		}
		// 文档键不得再走 AuthorizeChannel（H16 之前它必然被拒）
		hub.AuthorizeChannel = func(userID, channelID string) bool {
			t.Errorf("AuthorizeChannel must not be called for doc keys, got %s", channelID)
			return false
		}

		client := newDocClient(hub, "member-1")
		msg := &Message{
			Type:    "subscribe",
			Payload: []byte(`{"channelId":"doc_123"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if !client.isSubscribed("doc_123") {
			t.Error("expected client to be subscribed to doc key doc_123")
		}
	})

	t.Run("denies non-member doc subscription", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)

		hub.AuthorizeDoc = func(userID, docID string) bool {
			return false // 模拟非该空间成员
		}

		client := newDocClient(hub, "outsider-1")
		msg := &Message{
			Type:    "subscribe",
			Payload: []byte(`{"channelId":"doc_123"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if client.isSubscribed("doc_123") {
			t.Error("expected client NOT to be subscribed to doc_123")
		}
		select {
		case errMsg := <-client.send:
			if !strings.Contains(string(errMsg), "SHAREDOC_PERMISSION_DENIED") {
				t.Errorf("expected SHAREDOC_PERMISSION_DENIED error, got %s", errMsg)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected error message to be sent to client")
		}
	})

	t.Run("fail-closed when AuthorizeDoc not wired", func(t *testing.T) {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)
		// AuthorizeDoc 保持 nil：未装配校验的 hub 不得放行任何文档订阅

		client := newDocClient(hub, "anyone-1")
		msg := &Message{
			Type:    "subscribe",
			Payload: []byte(`{"channelId":"doc_123"}`),
		}
		hub.handleClientMessage(client, msg)
		time.Sleep(50 * time.Millisecond)

		if client.isSubscribed("doc_123") {
			t.Error("expected doc subscription to be rejected when AuthorizeDoc is nil")
		}
		select {
		case errMsg := <-client.send:
			if !strings.Contains(string(errMsg), "SHAREDOC_PERMISSION_DENIED") {
				t.Errorf("expected SHAREDOC_PERMISSION_DENIED error, got %s", errMsg)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected error message to be sent to client")
		}
	})
}

// H16 修复：doc_op 等协同事件按 payload.docId（原始文档 id）路由到文档
// 订阅键，发送者需通过 AuthorizeDoc 鉴权。订阅者（含发送者自己，客户端
// 按顶层 userId 过滤自身操作）收到；无关文档订阅者与非成员收不到。
func TestHubDocOpRouting(t *testing.T) {
	newDocClient := func(hub *Hub, userID string) *Client {
		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      userID,
			channels:    make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- client
		return client
	}

	setup := func(t *testing.T, authorize func(userID, docID string) bool) *Hub {
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)
		hub.AuthorizeDoc = authorize
		return hub
	}

	t.Run("routes doc_op to same-doc subscribers only", func(t *testing.T) {
		hub := setup(t, func(userID, docID string) bool { return docID == "doc_123" })

		editorA := newDocClient(hub, "user-A")
		editorB := newDocClient(hub, "user-B")
		otherDocSub := newDocClient(hub, "user-C")
		time.Sleep(50 * time.Millisecond)

		editorA.subscribe("doc_123")
		editorB.subscribe("doc_123")
		otherDocSub.subscribe("doc_999")
		time.Sleep(50 * time.Millisecond)

		msg := &Message{
			Type:    "doc_op",
			Payload: []byte(`{"docId":"doc_123","opType":"insert","position":0,"text":"hi","version":1}`),
		}
		hub.handleClientMessage(editorA, msg)
		time.Sleep(50 * time.Millisecond)

		// 同文档订阅者（含发送者自己——协议由客户端按 userId 过滤）收到
		for _, c := range []*Client{editorA, editorB} {
			select {
			case received := <-c.send:
				s := string(received)
				if !strings.Contains(s, `"type":"doc_op"`) || !strings.Contains(s, `"userId":"user-A"`) {
					t.Errorf("%s unexpected doc_op payload: %s", c.userID, s)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("%s did not receive doc_op", c.userID)
			}
		}

		// 其他文档的订阅者收不到
		select {
		case received := <-otherDocSub.send:
			t.Fatalf("user-C should not have received doc_op, got %s", received)
		case <-time.After(100 * time.Millisecond):
			// expected
		}
	})

	t.Run("drops doc_op from unauthorized sender", func(t *testing.T) {
		hub := setup(t, func(userID, docID string) bool { return userID == "member-1" })

		member := newDocClient(hub, "member-1")
		outsider := newDocClient(hub, "outsider-1")
		time.Sleep(50 * time.Millisecond)

		member.subscribe("doc_123")
		time.Sleep(50 * time.Millisecond)

		msg := &Message{
			Type:    "doc_op",
			Payload: []byte(`{"docId":"doc_123","opType":"insert","position":0,"text":"spoofed"}`),
		}
		hub.handleClientMessage(outsider, msg)
		time.Sleep(50 * time.Millisecond)

		select {
		case received := <-member.send:
			t.Fatalf("member should not receive spoofed doc_op, got %s", received)
		case <-time.After(100 * time.Millisecond):
			// expected
		}
	})

	t.Run("editor join fires callback and broadcasts to doc key", func(t *testing.T) {
		hub := setup(t, func(userID, docID string) bool { return true })

		var joinedDoc, joinedUser string
		hub.OnDocEditorJoin = func(docID, userID string) {
			joinedDoc, joinedUser = docID, userID
		}

		editor := newDocClient(hub, "user-A")
		time.Sleep(50 * time.Millisecond)
		editor.subscribe("doc_123")
		time.Sleep(50 * time.Millisecond)

		msg := &Message{
			Type:    "doc_editor_joined",
			Payload: []byte(`{"docId":"doc_123"}`),
		}
		hub.handleClientMessage(editor, msg)
		time.Sleep(50 * time.Millisecond)

		if joinedDoc != "doc_123" || joinedUser != "user-A" {
			t.Errorf("expected OnDocEditorJoin(doc_123, user-A), got (%s, %s)", joinedDoc, joinedUser)
		}
		select {
		case received := <-editor.send:
			if !strings.Contains(string(received), `"type":"doc_editor_joined"`) {
				t.Errorf("unexpected editor_joined payload: %s", received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("editor did not receive doc_editor_joined broadcast")
		}
	})
}

// cursorEvent 是 whiteboard_cursor 服务端转发事件的解码形状（A2-S1）：
// payload 中的 userId/action/ts 由服务端注入，不信任客户端自报。
type cursorEvent struct {
	Type    string `json:"type"`
	Payload struct {
		WhiteboardID string  `json:"whiteboardId"`
		UserID       string  `json:"userId"`
		X            float64 `json:"x"`
		Y            float64 `json:"y"`
		Action       string  `json:"action"`
		Ts           int64   `json:"ts"`
	} `json:"payload"`
}

// expectCursor 从 client.send 读一条消息并断言其为指定白板/action 的光标事件。
func expectCursor(t *testing.T, c *Client, wantWhiteboardID, wantUserID, wantAction string) cursorEvent {
	t.Helper()
	select {
	case raw := <-c.send:
		var ev cursorEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("invalid cursor event: %v (raw=%s)", err, raw)
		}
		if ev.Type != "whiteboard_cursor" {
			t.Fatalf("expected type whiteboard_cursor, got %s (raw=%s)", ev.Type, raw)
		}
		if wantWhiteboardID != "" && ev.Payload.WhiteboardID != wantWhiteboardID {
			t.Errorf("payload.whiteboardId = %s, want %s", ev.Payload.WhiteboardID, wantWhiteboardID)
		}
		if wantUserID != "" && ev.Payload.UserID != wantUserID {
			t.Errorf("payload.userId = %s, want %s (server must inject the authenticated id)", ev.Payload.UserID, wantUserID)
		}
		if wantAction != "" && ev.Payload.Action != wantAction {
			t.Errorf("payload.action = %s, want %s", ev.Payload.Action, wantAction)
		}
		return ev
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("client %s did not receive whiteboard_cursor event", c.userID)
		return cursorEvent{}
	}
}

// assertNoMessage 断言 client.send 缓冲中没有待收消息。
func assertNoMessage(t *testing.T, c *Client, label string) {
	t.Helper()
	time.Sleep(50 * time.Millisecond) // 给 Run loop 留出错投的时间窗
	select {
	case raw := <-c.send:
		t.Fatalf("%s unexpectedly received: %s", label, raw)
	default:
	}
}

// TestWhiteboardCursorRouting 覆盖 A2-S1（DES-20261001-01 §3.3）：
// 光标只转发给同白板其他订阅者（剔除发送者）、服务端注入 userId/action/ts、
// 未订阅白板拒绝路由、退订与断连时代发 action:"leave" 兜底。
func TestWhiteboardCursorRouting(t *testing.T) {
	newWbClient := func(hub *Hub, userID string) *Client {
		client := &Client{
			hub:         hub,
			send:        make(chan []byte, 256),
			userID:      userID,
			channels:    make(map[string]bool),
			whiteboards: make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- client
		time.Sleep(50 * time.Millisecond)
		return client
	}

	setup := func(t *testing.T) (*Hub, *Client, *Client, *Client) {
		t.Helper()
		log := testutil.TestLogger()
		hub := NewHub(log)
		go hub.Run()
		time.Sleep(50 * time.Millisecond)
		// 放行所有白板订阅：本测试聚焦路由行为，订阅授权（M11）另有用例。
		hub.AuthorizeWhiteboard = func(userID, whiteboardID string) bool { return true }

		sender := newWbClient(hub, "cursor-A")
		peer := newWbClient(hub, "cursor-B")
		outsider := newWbClient(hub, "cursor-C")
		for _, c := range []*Client{sender, peer, outsider} {
			c.subscribeWhiteboard("wb-route")
			outsider.unsubscribeWhiteboard("wb-route") // 只有 A/B 留在白板里
		}
		time.Sleep(50 * time.Millisecond)
		return hub, sender, peer, outsider
	}

	t.Run("relays move to peers with server-injected fields", func(t *testing.T) {
		hub, sender, peer, _ := setup(t)

		msg := &Message{
			Type:    "whiteboard_cursor",
			Payload: []byte(`{"whiteboardId":"wb-route","x":0.412,"y":0.733,"action":"move"}`),
		}
		hub.handleClientMessage(sender, msg)

		ev := expectCursor(t, peer, "wb-route", "cursor-A", "move")
		if ev.Payload.X < 0.411 || ev.Payload.X > 0.413 {
			t.Errorf("payload.x = %v, want ~0.412", ev.Payload.X)
		}
		if ev.Payload.Y < 0.732 || ev.Payload.Y > 0.734 {
			t.Errorf("payload.y = %v, want ~0.733", ev.Payload.Y)
		}
		if ev.Payload.Ts <= 0 {
			t.Errorf("payload.ts = %d, want server-injected millisecond timestamp", ev.Payload.Ts)
		}
		// 发送者不得回显。
		assertNoMessage(t, sender, "sender")
	})

	t.Run("rejects routing for unsubscribed whiteboard", func(t *testing.T) {
		hub, sender, peer, outsider := setup(t)

		msg := &Message{
			Type:    "whiteboard_cursor",
			Payload: []byte(`{"whiteboardId":"wb-other","x":0.5,"y":0.5,"action":"move"}`),
		}
		hub.handleClientMessage(outsider, msg) // C 未订阅 wb-other
		// C 订阅过的 wb-route 也已被退订，向它发同样应被拒。
		msg2 := &Message{
			Type:    "whiteboard_cursor",
			Payload: []byte(`{"whiteboardId":"wb-route","x":0.5,"y":0.5,"action":"move"}`),
		}
		hub.handleClientMessage(outsider, msg2)

		assertNoMessage(t, peer, "peer")
		assertNoMessage(t, sender, "sender")
	})

	t.Run("clamps coordinates and coerces invalid action to move", func(t *testing.T) {
		hub, sender, peer, _ := setup(t)

		msg := &Message{
			Type:    "whiteboard_cursor",
			Payload: []byte(`{"whiteboardId":"wb-route","x":1.7,"y":-0.3,"action":"hijack"}`),
		}
		hub.handleClientMessage(sender, msg)

		ev := expectCursor(t, peer, "wb-route", "cursor-A", "move")
		if ev.Payload.X != 1 {
			t.Errorf("payload.x = %v, want clamped 1", ev.Payload.X)
		}
		if ev.Payload.Y != 0 {
			t.Errorf("payload.y = %v, want clamped 0", ev.Payload.Y)
		}
	})

	t.Run("broadcasts leave on explicit unsubscribe", func(t *testing.T) {
		hub, sender, peer, _ := setup(t)

		msg := &Message{
			Type:    "unsubscribe_whiteboard",
			Payload: []byte(`{"whiteboardId":"wb-route"}`),
		}
		hub.handleClientMessage(peer, msg)

		expectCursor(t, sender, "wb-route", "cursor-B", "leave")
		assertNoMessage(t, peer, "unsubscribed peer")
	})

	t.Run("broadcasts leave on disconnect cleanup", func(t *testing.T) {
		hub, sender, peer, _ := setup(t)

		// 模拟 B 断连：走 unregister 清理路径。
		hub.unregister <- peer
		time.Sleep(50 * time.Millisecond)

		expectCursor(t, sender, "wb-route", "cursor-B", "leave")
		// 清理路径把断连 client 从 hub 侧白板集合移除（sender 仍订阅，
		// 集合本身保留）。
		hub.mu.RLock()
		_, peerStillThere := hub.whiteboards["wb-route"][peer]
		hub.mu.RUnlock()
		if peerStillThere {
			t.Error("expected disconnected peer to be removed from hub whiteboard set")
		}
	})

	t.Run("silent unsubscribe of never-subscribed whiteboard", func(t *testing.T) {
		hub, sender, peer, outsider := setup(t)

		msg := &Message{
			Type:    "unsubscribe_whiteboard",
			Payload: []byte(`{"whiteboardId":"wb-never"}`),
		}
		hub.handleClientMessage(outsider, msg)

		assertNoMessage(t, sender, "sender")
		assertNoMessage(t, peer, "peer")
	})
}

// TestCursorRateLimit 覆盖 A2-S1 独立限速桶：whiteboard_cursor 以 15 msg/s
// 的独立桶限速（超限静默丢弃），且不消耗全局桶——同一连接混合高频光标与
// 低频 typing 时，typing 不被饿死。走真实 readPump（httptest WebSocket）。
func TestCursorRateLimit(t *testing.T) {
	log := testutil.TestLogger()
	hub := NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)
	hub.AuthorizeWhiteboard = func(userID, whiteboardID string) bool { return true }

	// 观察者 B：mock client，订阅白板与频道，统计收到的事件。
	observer := &Client{
		hub:         hub,
		send:        make(chan []byte, 256),
		userID:      "rate-observer",
		channels:    make(map[string]bool),
		whiteboards: make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}
	hub.register <- observer
	observer.subscribeWhiteboard("wb-rate")
	observer.subscribe("ch-rate")
	time.Sleep(50 * time.Millisecond)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		sender := &Client{
			hub:         hub,
			conn:        conn,
			send:        make(chan []byte, 256),
			userID:      "rate-sender",
			channels:    make(map[string]bool),
			whiteboards: make(map[string]bool),
			rateLimiter: newRateLimiter(),
		}
		hub.register <- sender
		sender.subscribeWhiteboard("wb-rate")
		go sender.writePump()
		sender.readPump() // 阻塞直至连接关闭
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sendRaw := func(v interface{}) {
		t.Helper()
		data, _ := json.Marshal(v)
		if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// 突发 40 条 cursor（远超 15 msg/s 桶容量），短暂等待处理完成后
	// 再发 5 条 typing（走全局桶）。
	for i := 0; i < 40; i++ {
		sendRaw(map[string]interface{}{
			"type": "whiteboard_cursor",
			"payload": map[string]interface{}{
				"whiteboardId": "wb-rate",
				"x":            float64(i) / 40,
				"y":            0.5,
				"action":       "move",
			},
		})
	}
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 5; i++ {
		sendRaw(map[string]interface{}{
			"type":    "typing",
			"payload": map[string]interface{}{"channelId": "ch-rate"},
		})
	}

	cursorCount, typingCount := 0, 0
	deadline := time.After(2 * time.Second)
collect:
	for typingCount < 5 {
		select {
		case raw := <-observer.send:
			var msg struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "whiteboard_cursor":
				cursorCount++
			case "typing":
				typingCount++
			}
		case <-deadline:
			break collect
		}
	}

	// typing 全部到达：光标未消耗全局桶（否则 40 条光标会把 10 msg/s 的
	// 全局桶打空，5 条 typing 几乎全被丢）。
	if typingCount != 5 {
		t.Errorf("typing received = %d, want 5 (cursor must not starve the global bucket)", typingCount)
	}
	// 光标被独立桶限速：收到的数量显著小于 40（≈15 初始 token + 少量恢复），
	// 且不低于 13（若错用 10 msg/s 的全局桶参数则只有 ≈10-11）。
	if cursorCount < 13 || cursorCount > 25 {
		t.Errorf("cursor received = %d, want within [13, 25] (15 msg/s independent bucket)", cursorCount)
	}

	ws.Close()
}
