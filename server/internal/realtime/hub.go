package realtime

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	coreerrors "ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	applogger "ridgericetalk/internal/logger"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second
	// Time allowed to read the next pong message from the peer.
	// Per realtime design doc §2.4: heartbeat interval 30s, timeout 90s.
	pongWait = 90 * time.Second
	// Send pings to peer with this period (must be less than pongWait)
	pingPeriod = 30 * time.Second
	// Maximum message size allowed from peer (64KB)
	maxMessageSize = 64 * 1024
	// Rate limit: 10 messages per second (global per-client bucket)
	rateLimitMessages = 10
	rateLimitWindow   = time.Second
)

// rateLimitConfig 描述一条消息类型的独立限速桶参数（A2-S1 引入，按类型
// 泛化：后续高频事件在此表登记即可获得同机制的独立桶，无需改动读泵）。
type rateLimitConfig struct {
	messages int
	window   time.Duration
}

// typeRateLimits 配置了独立限速桶的消息类型。语义为「替代」而非「叠加」：
// 命中的类型只受自己的桶约束，不消耗全局桶。原因：全局桶 10 msg/s 低于
// 光标 15 msg/s，若光标仍需过全局桶则独立桶形同虚设，且高频光标会把
// typing/message 等低频事件挤死（饿死）。未命中本表的类型保持原有全局
// 桶行为不变。
var typeRateLimits = map[string]rateLimitConfig{
	"whiteboard_cursor":    {messages: 15, window: time.Second},
	"screenshare_annotate": {messages: 20, window: time.Second}, // A5-S1：笔迹分段批量（客户端 20Hz 打包）
	// A6-S1（DES-20261001-01 §7.2）：远程协助剪贴板。控制端 push/pull 与被控端
	// 回传分桶同限 5 msg/s——剪贴板由用户显式操作（按钮/快捷键）触发，5/s 足够
	// 且防脚本刷文本；A7-S1（§8.1）：会话内聊天 10 msg/s（打字节奏上限）。
	"remote_assist_clipboard":      {messages: 5, window: time.Second},
	"remote_assist_clipboard_data": {messages: 5, window: time.Second},
	"remote_assist_chat":           {messages: 10, window: time.Second},
}

// isLocalDevOrigin reports whether the WebSocket Origin header points at the
// local development machine. Packaged desktop clients load the renderer from
// file:// and send their target server as the Origin (e.g.
// http://127.0.0.1:8099), so the loopback addresses must be accepted alongside
// localhost. The IPv6 loopback is written as "[::1]" in a URL.
func isLocalDevOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// Upgrader is the shared WebSocket upgrader used by both the main /ws route
// and the admin /admin/ws route. Exported so the admin handler (in the admin
// package) can reuse the same upgrader configuration instead of inventing a
// new one. Callers that need to negotiate a subprotocol must copy this value
// before mutating the Subprotocols slice (see HandleWebSocket).
var Upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		env := os.Getenv("RRT_ENV")
		if origin == "" {
			// N18：桌面客户端（Electron file:// 渲染层）的 WebSocket 握手不带 Origin
			// 头，无法通过浏览器同源校验，导致生产模式下实时链路（消息/typing/presence）
			// 全部断链（部署演练 T26 双客户端实测发现）。空 Origin 只能出自非浏览器
			// 客户端——浏览器跨站 WS 必带 Origin 头，不存在「无 Origin 的网页」；
			// 因此对携带鉴权凭据（token 查询参数 / Authorization / Sec-WebSocket-Protocol）
			// 的空 Origin 请求放行，CSRF 风险由随后的 token 鉴权兜底。
			if r.URL.Query().Get("token") != "" || r.URL.Query().Get("access_token") != "" ||
				r.Header.Get("Authorization") != "" || r.Header.Get("Sec-WebSocket-Protocol") != "" {
				return true
			}
			// Reject empty Origin in production
			return env != "production"
		}
		// Allow localhost in development
		if isLocalDevOrigin(origin) {
			return true
		}
		// Production: check against configured public address and allowed origins
		publicAddr := os.Getenv("RRT_PUBLIC_ADDRESS")
		allowed := []string{}
		if publicAddr != "" {
			allowed = append(allowed, publicAddr)
		}
		if cors := os.Getenv("RRT_CORS_ORIGINS"); cors != "" {
			for _, o := range strings.Split(cors, ",") {
				o = strings.TrimSpace(o)
				if o != "" {
					allowed = append(allowed, o)
				}
			}
		}
		if config.IsAllowedOrigin(origin, allowed) {
			return true
		}
		// Fallback: keep legacy behavior for backward compatibility
		if publicAddr != "" && strings.HasPrefix(origin, publicAddr) {
			return true
		}
		return false
	},
}

// Message represents a WebSocket message
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	UserID  string          `json:"-"`
	Channel string          `json:"-"`
}

// Client is a WebSocket connection wrapper
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	userID string
	// sessionID 是该连接 access token 的 session_id 声明（A4-S2）：升级时
	// 由 HandleWebSocket 从 gin 上下文取回（HTTP 鉴权处已解析），连接生命
	// 期内只读。旧客户端 token 无此声明时为空串——定向广播按「宁多发不
	// 漏发」原则不做跳过。
	sessionID string
	// sharingChannel 记录本连接当前正在共享屏幕的频道 ID（A5-S1）。空串=
	// 未共享。一个连接同时至多共享一个频道：重复 started 覆盖旧值。写入只
	// 发生在 handleClientMessage（readPump goroutine），清理发生在 unregister
	// （hub Run 循环），两端都经 hub.sharersMu 串行化，本字段自身无需独立锁。
	sharingChannel string
	channels       map[string]bool
	rooms          map[string]bool // minigame room subscriptions
	whiteboards    map[string]bool // whiteboard collaboration subscriptions
	rateLimiter    *RateLimiter
	// typeLimiters 保存本连接各消息类型的独立限速桶（A2-S1），键为消息
	// 类型，按 typeRateLimits 懒初始化。只在所属连接的 readPump goroutine
	// 中读写，无需加锁。
	typeLimiters map[string]*RateLimiter
	mu           sync.RWMutex
	closeOnce    sync.Once
}

// RateLimiter implements a simple token bucket
type RateLimiter struct {
	tokens    int
	capacity  int           // bucket size == refill-per-window
	window    time.Duration // refill window
	lastCheck time.Time
	mu        sync.Mutex
}

func newRateLimiter() *RateLimiter {
	return newRateLimiterWith(rateLimitMessages, rateLimitWindow)
}

// newRateLimiterWith 创建一条按类型配置的独立限速桶（A2-S1）。
func newRateLimiterWith(messages int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		tokens:    messages,
		capacity:  messages,
		window:    window,
		lastCheck: time.Now(),
	}
}

func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastCheck)
	rl.lastCheck = now

	// Replenish tokens
	rl.tokens += int(elapsed / rl.window * time.Duration(rl.capacity))
	if rl.tokens > rl.capacity {
		rl.tokens = rl.capacity
	}

	if rl.tokens > 0 {
		rl.tokens--
		return true
	}
	return false
}

func (c *Client) subscribe(channelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels[channelID] = true
	c.hub.registerChannel <- channelSubscription{client: c, channel: channelID}
}

func (c *Client) unsubscribe(channelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.channels, channelID)
	c.hub.unregisterChannel <- channelSubscription{client: c, channel: channelID}
}

func (c *Client) isSubscribed(channelID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channels[channelID]
}

func (c *Client) subscribeRoom(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rooms[roomID] = true
	c.hub.registerRoom <- roomSubscription{client: c, room: roomID}
}

func (c *Client) unsubscribeRoom(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rooms, roomID)
	c.hub.unregisterRoom <- roomSubscription{client: c, room: roomID}
}

func (c *Client) isSubscribedRoom(roomID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rooms[roomID]
}

func (c *Client) subscribeWhiteboard(whiteboardID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.whiteboards[whiteboardID] = true
	c.hub.registerWhiteboard <- whiteboardSubscription{client: c, whiteboardID: whiteboardID}
}

func (c *Client) unsubscribeWhiteboard(whiteboardID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.whiteboards, whiteboardID)
	c.hub.unregisterWhiteboard <- whiteboardSubscription{client: c, whiteboardID: whiteboardID}
}

func (c *Client) isSubscribedWhiteboard(whiteboardID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.whiteboards[whiteboardID]
}

// allowsMessage 报告一条待处理消息是否被限速放行（A2-S1）。配置在
// typeRateLimits 中的类型走自己的独立桶（超限返回 false，由调用方静默
// 丢弃，不消耗全局桶）；其余类型走原有的全局桶。只在 readPump goroutine
// 中调用。
func (c *Client) allowsMessage(msgType string) bool {
	if cfg, ok := typeRateLimits[msgType]; ok {
		if c.typeLimiters == nil {
			c.typeLimiters = make(map[string]*RateLimiter)
		}
		limiter := c.typeLimiters[msgType]
		if limiter == nil {
			limiter = newRateLimiterWith(cfg.messages, cfg.window)
			c.typeLimiters[msgType] = limiter
		}
		return limiter.Allow()
	}
	return c.rateLimiter.Allow()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.log.Error("websocket unexpected close", "error", err, "user_id", c.userID)
			}
			break
		}

		// Parse message（解析提前到限速之前：独立限速桶按消息类型分桶，
		// 必须先知道类型才能选桶。对合法消息的全局桶语义不变——每条合法
		// 消息仍恰好消耗一个全局桶 token；仅未配置独立桶的类型才消耗全局桶）。
		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			c.hub.log.Warn("websocket invalid message", "error", err, "user_id", c.userID)
			continue
		}

		// Rate limit check: 命中独立桶的类型超限时静默丢弃（A2-S1），
		// 其余类型保持原有全局桶警告+丢弃行为。
		if !c.allowsMessage(msg.Type) {
			if _, isTyped := typeRateLimits[msg.Type]; !isTyped {
				c.hub.log.Warn("websocket rate limit exceeded", "user_id", c.userID)
			}
			continue
		}

		// Handle client messages
		c.hub.handleClientMessage(c, &msg)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.conn.WriteMessage(websocket.TextMessage, message)

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type channelSubscription struct {
	client  *Client
	channel string
}

type roomSubscription struct {
	client *Client
	room   string
}

type whiteboardSubscription struct {
	client      *Client
	whiteboardID string
}

type channelMessage struct {
	target string // "channel", "user", "room", "channel_nonblock", "whiteboard"
	id     string // channelID, userID, roomID, or whiteboardID
	data   []byte
	// exclude 非空时从投递集合中剔除该 client（A2-S1：whiteboard_cursor
	// 不回显发送者）。仅 whiteboard 目标消费此字段；其余 target 忽略。
	exclude *Client
	// exceptSessionID 非空时，sessionID 与之相同的连接被跳过（A4-S2：
	// security_alert 不打扰触发它的那条新登录连接）。仅 user 目标消费此
	// 字段；无 sessionID 的旧连接不跳过（宁多发不漏发）。
	exceptSessionID string
}

type clientMessage struct {
	client *Client
	data   []byte
}

// defaultGracefulOfflineDelay is the time to wait after a user's last connection
// disconnects before marking them offline. This absorbs transient network
// blips and page reloads so the presence indicator doesn't flicker.
const defaultGracefulOfflineDelay = 30 * time.Second

// Hub maintains the set of active clients and broadcasts messages
type Hub struct {
	clients               map[*Client]bool
	channels              map[string]map[*Client]bool
	rooms                 map[string]map[*Client]bool      // minigame roomID -> clients
	whiteboards           map[string]map[*Client]bool      // whiteboardID -> clients
	userClientCount       map[string]int                   // userID -> number of active connections
	offlineTimers         map[string]*time.Timer           // userID -> graceful offline timer
	gracefulOfflineDelay  time.Duration                    // configurable for tests
	broadcast             chan []byte
	register              chan *Client
	unregister            chan *Client
	registerChannel       chan channelSubscription
	unregisterChannel     chan channelSubscription
	registerRoom          chan roomSubscription
	unregisterRoom        chan roomSubscription
	registerWhiteboard    chan whiteboardSubscription
	unregisterWhiteboard  chan whiteboardSubscription
	cancelGracefulOffline chan string
	chanMsg               chan channelMessage
	clientSend            chan clientMessage
	// C2: admin subscribers receive a copy of every broadcast message so the
	// admin frontend (dedicated admin port /admin/ws) can filter for
	// module_status_changed events. Admin subscribers are NOT regular clients:
	// they do not participate in presence/channel/room tracking.
	adminSubs     map[chan []byte]bool
	regAdminSub   chan chan []byte
	unregAdminSub chan chan []byte
		log           *applogger.Logger
	mu            sync.RWMutex
	// channelSharers 维护「频道 → 当前屏幕共享者 userID」的内存映射
	//（A5-S1，DES-20261001-01 §6.2）。screenshare_started 写入、
	// screenshare_stopped 与断连清理删除；标注路由用它 O(1) 校验发送者
	// 是否是当前共享者，不查库。由 sharersMu 保护——handleClientMessage
	// 在 readPump goroutine 执行，unregister 在 Run 循环执行，两者并发。
	channelSharers map[string]string
	sharersMu      sync.Mutex
	// OnClientConnect is called when a new client connects (arg = total clients)
	OnClientConnect func(total int)
	// OnClientDisconnect is called when a client disconnects (arg = total clients)
	OnClientDisconnect func(total int)
	// OnUserFullyOffline is called when a user's last connection disconnects.
	OnUserFullyOffline func(userID string)
	// OnUserCameOnline is called when a user's first connection is established
	// (userClientCount transitions from 0 → 1).
	OnUserCameOnline func(userID string)
	// OnMessage is called when a client message is handled
	OnMessage func(msgType string)
	// OnPresenceUpdate is called when a client sends a presence_update.
	// Arguments: userID, status, customStatus. Returns error if persistence failed.
	OnPresenceUpdate func(userID, status, customStatus string) error
	// AuthorizeChannel is called when a client wants to subscribe to a channel.
	// Return true to allow, false to deny (client will receive an error response).
	AuthorizeChannel func(userID, channelID string) bool
	// AuthorizeRoom is called when a client wants to subscribe to a minigame room.
	// Return true to allow, false to deny (client will receive an error response).
	AuthorizeRoom func(userID, roomID string) bool
	// AuthorizeWhiteboard is called when a client wants to subscribe to a
	// whiteboard collaboration stream. Return true to allow, false to deny.
	// Fail-closed (M11): when nil, every whiteboard subscription is rejected —
	// an unwired hub must not leak stroke streams across spaces.
	AuthorizeWhiteboard func(userID, whiteboardID string) bool
	// AuthorizeDoc is called when a client wants to subscribe to a shared
	// document collaboration stream (H16). The key is the document's raw id
	// (idgen.PrefixDoc-prefixed, e.g. "doc_723169646487932928") — the same key
	// every doc_* relay event is broadcast on. Return true to allow, false to
	// deny. Fail-closed: when nil, every doc subscription is rejected and every
	// doc_* relay event is dropped — an unwired hub must not leak doc ops or
	// editor presence across spaces.
	AuthorizeDoc func(userID, docID string) bool
	// OnSubscribeSync is called when a client subscribes to a channel.
	// Returns the sync_snapshot payload (nil if not available).
	// H18: enables initial state push after subscription.
	OnSubscribeSync func(userID, channelID string) map[string]interface{}
	// OnDocEditorJoin is called when a client starts editing a shared document.
	// Arguments: docID, userID
	OnDocEditorJoin func(docID, userID string)
	// OnDocEditorLeave is called when a client stops editing a shared document.
	// Arguments: docID, userID
	OnDocEditorLeave func(docID, userID string)
	// OnWhiteboardJoin is called when a client joins a whiteboard collaboration.
	// Arguments: whiteboardID, userID
	OnWhiteboardJoin func(whiteboardID, userID string)
	// OnWhiteboardLeave is called when a client leaves a whiteboard collaboration.
	// Arguments: whiteboardID, userID
	OnWhiteboardLeave func(whiteboardID, userID string)
	// OnRemoteAssistControl authorizes a remote assist control event (T49, H14)
	// before the hub forwards it to the target. Arguments: sessionID,
	// requesterID (the authenticated sender), targetID (the payload's target).
	// A non-nil return drops the event. Wired in internal/server/routes.go to
	// remoteassist.Service.ValidateControlEvent. When nil the hub drops every
	// control event (fail closed: the hub cannot verify authorization alone).
	OnRemoteAssistControl func(sessionID, requesterID, targetID string) error
	// OnRemoteAssistFromTarget authorizes a clipboard pull-data reply sent by
	// the session target (A6-S1, DES-20261001-01 §7.2) before the hub relays
	// it to the requester. Arguments: sessionID, senderID (the authenticated
	// sender). Returns the session requester's ID to route the data to, or a
	// non-nil error to drop the event. Wired in internal/server/routes.go to
	// remoteassist.Service.ValidateFromTarget. When nil every data reply is
	// dropped (fail closed: the hub cannot verify authorization alone).
	OnRemoteAssistFromTarget func(sessionID, senderID string) (string, error)
	// OnRemoteAssistChatParticipant authorizes an in-session chat message
	// (A7-S1, DES-20261001-01 §8.1) before the hub relays it to both parties.
	// Arguments: sessionID, senderID. Returns the other party's (peer's) ID
	// on success. Wired to remoteassist.Service.ValidateChatParticipant.
	// When nil every chat message is dropped (fail closed).
	OnRemoteAssistChatParticipant func(sessionID, senderID string) (string, error)
}

// NewHub creates a new Hub
func NewHub(log *applogger.Logger) *Hub {
	return &Hub{
		clients:               make(map[*Client]bool),
		channels:              make(map[string]map[*Client]bool),
		rooms:                 make(map[string]map[*Client]bool),
		whiteboards:           make(map[string]map[*Client]bool),
		userClientCount:       make(map[string]int),
		offlineTimers:         make(map[string]*time.Timer),
		gracefulOfflineDelay:  defaultGracefulOfflineDelay,
		broadcast:             make(chan []byte, 4096),
		register:              make(chan *Client),
		unregister:            make(chan *Client),
		registerChannel:       make(chan channelSubscription),
		unregisterChannel:     make(chan channelSubscription),
		registerRoom:          make(chan roomSubscription),
		unregisterRoom:        make(chan roomSubscription),
		registerWhiteboard:    make(chan whiteboardSubscription),
		unregisterWhiteboard:  make(chan whiteboardSubscription),
		cancelGracefulOffline: make(chan string),
		chanMsg:               make(chan channelMessage, 4096),
		clientSend:            make(chan clientMessage, 4096),
		adminSubs:             make(map[chan []byte]bool),
		regAdminSub:           make(chan chan []byte),
		unregAdminSub:         make(chan chan []byte),
		channelSharers:        make(map[string]string),
		log:                   log,
	}
}

// Run starts the Hub's event loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			if client.channels == nil {
				client.channels = make(map[string]bool)
			}
			if client.rooms == nil {
				client.rooms = make(map[string]bool)
			}
			if client.whiteboards == nil {
				client.whiteboards = make(map[string]bool)
			}
			wasFirstConnection := h.userClientCount[client.userID] == 0
			h.userClientCount[client.userID]++
			// User reconnected within the graceful window — cancel the timer.
			if timer, ok := h.offlineTimers[client.userID]; ok {
				timer.Stop()
				delete(h.offlineTimers, client.userID)
				h.log.Debug("cancelled graceful offline timer", "user_id", client.userID)
			}
			total := len(h.clients)
			h.mu.Unlock()
			h.log.Info("websocket client connected", "user_id", client.userID, "total_clients", total)
			if h.OnClientConnect != nil {
				h.OnClientConnect(total)
			}
			if wasFirstConnection && h.OnUserCameOnline != nil {
				h.OnUserCameOnline(client.userID)
			}

		case client := <-h.unregister:
			h.mu.Lock()
			wasLastConnection := false
			disconnectedUserID := client.userID
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				h.userClientCount[client.userID]--
				wasLastConnection = h.userClientCount[client.userID] == 0
				if wasLastConnection {
					delete(h.userClientCount, client.userID)
				}
				client.closeOnce.Do(func() { close(client.send) })
				// Remove from all channels and rooms — hold client read lock to prevent
				// concurrent map read/write with readPump processing subscribe/unsubscribe.
				client.mu.RLock()
				for channel := range client.channels {
					if h.channels[channel] != nil {
						delete(h.channels[channel], client)
					}
				}
				for room := range client.rooms {
					if h.rooms[room] != nil {
						delete(h.rooms[room], client)
						if len(h.rooms[room]) == 0 {
							delete(h.rooms, room)
						}
					}
				}
				// A2-S1：收集断连后仍有其他订阅者的白板，锁外代发 leave
				// 兜底事件（防失联残留光标）。
				var leftWhiteboards []string
				for whiteboard := range client.whiteboards {
					if h.whiteboards[whiteboard] != nil {
						delete(h.whiteboards[whiteboard], client)
						if len(h.whiteboards[whiteboard]) == 0 {
							delete(h.whiteboards, whiteboard)
						} else {
							leftWhiteboards = append(leftWhiteboards, whiteboard)
						}
					}
				}
				client.mu.RUnlock()
				// BroadcastToWhiteboardExcept 仅做非阻塞 chanMsg 投递，
				// 在事件循环内调用不会死锁；消息在下一轮 select 消费。
				for _, whiteboard := range leftWhiteboards {
					h.broadcastWhiteboardLeave(whiteboard, client)
				}
				// A5-S1：断连清理屏幕共享者登记，残留则向频道代发
				// {tool:"clear"} 兜底（观看端标注叠加层不得残留笔迹）。
				// releaseClientSharer 内部自持 sharersMu，与 readPump 侧的
				// started/stopped 处理串行；代发是非阻塞投递。
				if ch, sharer := h.releaseClientSharer(client); ch != "" {
					h.broadcastAnnotationClear(ch, sharer)
				}
			}
			// Graceful offline: wait before marking the user fully offline so that
			// transient disconnects and page reloads do not flash the indicator.
			if wasLastConnection {
				uid := disconnectedUserID
				delay := h.gracefulOfflineDelay
				h.offlineTimers[uid] = time.AfterFunc(delay, func() {
					h.mu.Lock()
					delete(h.offlineTimers, uid)
					h.mu.Unlock()
					if h.OnUserFullyOffline != nil {
						h.OnUserFullyOffline(uid)
					}
				})
			}
			total := len(h.clients)
			h.mu.Unlock()
			h.log.Info("websocket client disconnected", "user_id", disconnectedUserID, "total_clients", total)
			if h.OnClientDisconnect != nil {
				h.OnClientDisconnect(total)
			}

		case userID := <-h.cancelGracefulOffline:
			h.mu.Lock()
			if timer, ok := h.offlineTimers[userID]; ok {
				timer.Stop()
				delete(h.offlineTimers, userID)
				h.log.Debug("cancelled graceful offline timer via API", "user_id", userID)
			}
			h.mu.Unlock()

		case sub := <-h.registerChannel:
			h.mu.Lock()
			if h.channels[sub.channel] == nil {
				h.channels[sub.channel] = make(map[*Client]bool)
			}
			h.channels[sub.channel][sub.client] = true
			h.mu.Unlock()

		case sub := <-h.unregisterChannel:
			h.mu.Lock()
			if h.channels[sub.channel] != nil {
				delete(h.channels[sub.channel], sub.client)
				if len(h.channels[sub.channel]) == 0 {
					delete(h.channels, sub.channel)
				}
			}
			h.mu.Unlock()

		case sub := <-h.registerRoom:
			h.mu.Lock()
			if h.rooms[sub.room] == nil {
				h.rooms[sub.room] = make(map[*Client]bool)
			}
			h.rooms[sub.room][sub.client] = true
			h.mu.Unlock()

		case sub := <-h.unregisterRoom:
			h.mu.Lock()
			if h.rooms[sub.room] != nil {
				delete(h.rooms[sub.room], sub.client)
				if len(h.rooms[sub.room]) == 0 {
					delete(h.rooms, sub.room)
				}
			}
			h.mu.Unlock()

		case sub := <-h.registerWhiteboard:
			h.mu.Lock()
			if h.whiteboards[sub.whiteboardID] == nil {
				h.whiteboards[sub.whiteboardID] = make(map[*Client]bool)
			}
			h.whiteboards[sub.whiteboardID][sub.client] = true
			h.mu.Unlock()

		case sub := <-h.unregisterWhiteboard:
			h.mu.Lock()
			if h.whiteboards[sub.whiteboardID] != nil {
				delete(h.whiteboards[sub.whiteboardID], sub.client)
				if len(h.whiteboards[sub.whiteboardID]) == 0 {
					delete(h.whiteboards, sub.whiteboardID)
				}
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			clients := make(map[*Client]bool)
			for c := range h.clients {
				clients[c] = true
			}
			// C2: snapshot admin subscribers so we can deliver outside the lock.
			// Admin subs receive a copy of every broadcast; the admin WS handler
			// is responsible for filtering by message type (module_status_changed).
			adminSubs := make([]chan []byte, 0, len(h.adminSubs))
			for sub := range h.adminSubs {
				adminSubs = append(adminSubs, sub)
			}
			h.mu.RUnlock()
			for client := range clients {
				select {
				case client.send <- message:
				default:
					// Client's send channel is full, drop the message
					h.log.Warn("websocket client send buffer full, dropping message", "user_id", client.userID)
				}
			}
			// C2: fan out to admin subscribers (non-blocking; drop on full buffer).
			for _, sub := range adminSubs {
				select {
				case sub <- message:
				default:
					h.log.Warn("admin subscriber buffer full, dropping message")
				}
			}

		case sub := <-h.regAdminSub:
			// C2: register an admin broadcast subscriber.
			h.mu.Lock()
			h.adminSubs[sub] = true
			h.mu.Unlock()

		case sub := <-h.unregAdminSub:
			// C2: unregister and close an admin broadcast subscriber.
			h.mu.Lock()
			if _, ok := h.adminSubs[sub]; ok {
				delete(h.adminSubs, sub)
				close(sub)
			}
			h.mu.Unlock()

		case cm := <-h.chanMsg:
			switch cm.target {
			case "channel":
				h.mu.RLock()
				channelClients := make(map[*Client]bool)
				if h.channels[cm.id] != nil {
					for c := range h.channels[cm.id] {
						channelClients[c] = true
					}
				}
				h.mu.RUnlock()
				for client := range channelClients {
					select {
					case client.send <- cm.data:
					default:
						h.log.Warn("chanMsg send buffer full", "channel", cm.id)
					}
				}
			case "user":
				h.mu.RLock()
				var userClients []*Client
				for c := range h.clients {
					if c.userID != cm.id {
						continue
					}
					// A4-S2：跳过触发告警的新会话连接；旧连接无 sessionID，
					// 不跳过（宁多发不漏发）。
					if cm.exceptSessionID != "" && c.sessionID == cm.exceptSessionID {
						continue
					}
					userClients = append(userClients, c)
				}
				h.mu.RUnlock()
				for _, client := range userClients {
					select {
					case client.send <- cm.data:
					default:
						h.log.Warn("chanMsg send buffer full", "user", cm.id)
					}
				}
			case "room":
				h.mu.RLock()
				roomClients := make(map[*Client]bool)
				if h.rooms[cm.id] != nil {
					for c := range h.rooms[cm.id] {
						roomClients[c] = true
					}
				}
				h.mu.RUnlock()
				for client := range roomClients {
					select {
					case client.send <- cm.data:
					default:
						h.log.Warn("chanMsg send buffer full", "room", cm.id)
					}
				}
			case "whiteboard":
				h.mu.RLock()
				whiteboardClients := make(map[*Client]bool)
				if h.whiteboards[cm.id] != nil {
					for c := range h.whiteboards[cm.id] {
						if cm.exclude != nil && c == cm.exclude {
							continue
						}
						whiteboardClients[c] = true
					}
				}
				h.mu.RUnlock()
				for client := range whiteboardClients {
					select {
					case client.send <- cm.data:
					default:
						h.log.Warn("chanMsg send buffer full", "whiteboard", cm.id)
					}
				}
			}

		case cs := <-h.clientSend:
			// Only deliver if the client is still registered, to avoid writes
			// to a closed send channel after unregister.
			h.mu.RLock()
			_, ok := h.clients[cs.client]
			h.mu.RUnlock()
			if !ok {
				break
			}
			select {
			case cs.client.send <- cs.data:
			default:
				h.log.Warn("client send buffer full, dropping message", "user_id", cs.client.userID)
			}
		}
	}
}

// BroadcastToChannel sends a message to all clients subscribed to a channel.
// Routes through the Hub event loop to avoid races with client unregister.
func (h *Hub) BroadcastToChannel(channelID string, data []byte) {
	select {
	case h.chanMsg <- channelMessage{target: "channel", id: channelID, data: data}:
	default:
		h.log.Warn("chanMsg buffer full, dropping channel broadcast", "channel", channelID)
	}
}

// BroadcastToWhiteboard sends a message to all clients subscribed to a whiteboard.
func (h *Hub) BroadcastToWhiteboard(whiteboardID string, data []byte) {
	select {
	case h.chanMsg <- channelMessage{target: "whiteboard", id: whiteboardID, data: data}:
	default:
		h.log.Warn("chanMsg buffer full, dropping whiteboard broadcast", "whiteboard", whiteboardID)
	}
}

// BroadcastToWhiteboardExcept sends a message to all clients subscribed to a
// whiteboard except the given one. A2-S1: whiteboard_cursor is relayed with
// the sender excluded (no echo), so clients render their own cursor locally.
// The excluded client is skipped at fan-out time; existing callers of
// BroadcastToWhiteboard are unaffected.
func (h *Hub) BroadcastToWhiteboardExcept(whiteboardID string, exclude *Client, data []byte) {
	select {
	case h.chanMsg <- channelMessage{target: "whiteboard", id: whiteboardID, data: data, exclude: exclude}:
	default:
		h.log.Warn("chanMsg buffer full, dropping whiteboard broadcast", "whiteboard", whiteboardID)
	}
}

// BroadcastToUser sends a message to a specific user's connections.
// Routes through the Hub event loop to avoid races with client unregister.
func (h *Hub) BroadcastToUser(userID string, data []byte) {
	select {
	case h.chanMsg <- channelMessage{target: "user", id: userID, data: data}:
	default:
		h.log.Warn("chanMsg buffer full, dropping user broadcast", "user", userID)
	}
}

// BroadcastToUserExcept sends a message to a specific user's connections,
// skipping connections whose access-token session_id equals exceptSessionID
// (A4-S2：security_alert 不打扰触发它的那条新登录连接). Connections without
// a sessionID (legacy tokens) are NOT skipped — 宁可多发不漏发. Existing
// callers of BroadcastToUser are unaffected (empty exceptSessionID = no skip).
func (h *Hub) BroadcastToUserExcept(userID string, exceptSessionID string, data []byte) {
	select {
	case h.chanMsg <- channelMessage{target: "user", id: userID, data: data, exceptSessionID: exceptSessionID}:
	default:
		h.log.Warn("chanMsg buffer full, dropping user broadcast", "user", userID)
	}
}

// BroadcastToUsers sends a message to multiple users' connections (same
// mechanics as BroadcastToUser, fanned out per user). Used for space-scoped
// events — e.g. whiteboard list changes — where the caller resolves the
// recipient set from space membership (审计 N9).
func (h *Hub) BroadcastToUsers(userIDs []string, data []byte) {
	for _, id := range userIDs {
		h.BroadcastToUser(id, data)
	}
}

// BroadcastToRoom sends a message to all clients subscribed to a minigame room.
// Routes through the Hub event loop to avoid races with client unregister.
func (h *Hub) BroadcastToRoom(roomID string, data []byte) {
	select {
	case h.chanMsg <- channelMessage{target: "room", id: roomID, data: data}:
	default:
		h.log.Warn("chanMsg buffer full, dropping room broadcast", "room", roomID)
	}
}

// SendToClient sends a message to a single client through the Hub event loop.
// This is safe to call from any goroutine (e.g. handleClientMessage) and avoids
// races with client unregister that would otherwise close client.send.
func (h *Hub) SendToClient(client *Client, data []byte) {
	select {
	case h.clientSend <- clientMessage{client: client, data: data}:
	default:
		h.log.Warn("clientSend buffer full, dropping message", "user_id", client.userID)
	}
}

// Broadcast sends a message to all connected clients
func (h *Hub) Broadcast(data []byte) {
	select {
	case h.broadcast <- data:
	default:
		h.log.Warn("hub broadcast buffer full, dropping message")
	}
}

// SubscribeAdmin registers an admin broadcast subscriber and returns a
// receive-only channel that receives a copy of every message broadcast via
// Broadcast() (i.e. messages sent to all clients, such as
// module_status_changed and presence_update).
//
// This is used by the admin WebSocket handler (GET /admin/ws on the dedicated
// admin engine) so the admin frontend can react to module enable/disable
// events in real time. The subscriber is NOT a regular client: it does not
// participate in presence tracking, channel subscriptions, or rate limiting,
// and it does not count against the per-user connection limit.
//
// The returned unsubscribe function must be called when the admin WebSocket
// connection closes so the Hub stops delivering messages and releases the
// associated resources. It is safe to call unsubscribe multiple times.
//
// The caller is responsible for filtering messages by type — admin
// subscribers receive all broadcasts and should only forward the types
// relevant to the admin frontend (e.g. module_status_changed).
func (h *Hub) SubscribeAdmin() (<-chan []byte, func()) {
	sub := make(chan []byte, 64)
	h.regAdminSub <- sub
	var once sync.Once
	return sub, func() {
		// idempotent: only the first call sends to the unregister channel.
		once.Do(func() {
			h.unregAdminSub <- sub
		})
	}
}

// CancelGracefulOffline cancels the pending graceful-offline timer for a user.
// This should be called when the user explicitly updates their presence via the
// HTTP API (e.g. setting online or offline), so the Hub does not broadcast a
// duplicate offline event when the timer eventually fires.
func (h *Hub) CancelGracefulOffline(userID string) {
	select {
	case h.cancelGracefulOffline <- userID:
	default:
		h.log.Warn("cancelGracefulOffline buffer full", "user_id", userID)
	}
}

// ClientCount returns the number of connected clients
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ConnectionCount returns the total number of active WebSocket connections
// across all users, including test registrations. Used by admin runtime stats
// for the websocket_conns metric.
func (h *Hub) ConnectionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	for _, count := range h.userClientCount {
		total += count
	}
	return total
}

// OnlineUserIDs returns a snapshot of user IDs that currently have at least
// one active WebSocket connection. Used by admin runtime stats (online_users).
func (h *Hub) OnlineUserIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.userClientCount))
	for uid := range h.userClientCount {
		ids = append(ids, uid)
	}
	return ids
}

// RegisterTestUser marks a userID as having one active connection without
// requiring a real WebSocket. Intended for tests that need to simulate an
// online user without spinning up a full client.
func (h *Hub) RegisterTestUser(userID string) {
	h.mu.Lock()
	h.userClientCount[userID]++
	total := len(h.clients)
	h.mu.Unlock()
	h.log.Info("test user registered", "user_id", userID, "total_clients", total)
	if h.OnClientConnect != nil {
		h.OnClientConnect(total)
	}
}

// handleClientMessage processes messages from clients
func (h *Hub) handleClientMessage(client *Client, msg *Message) {
	if h.OnMessage != nil {
		h.OnMessage(msg.Type)
	}
	switch msg.Type {
	case "subscribe":
		var payload struct {
			ChannelID string `json:"channelId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.ChannelID != "" {
			// H16: 共享文档的虚拟订阅键是文档原始 id（idgen.PrefixDoc 前缀，
			// 如 "doc_7231..."），与 channels 表无关，走 AuthorizeDoc 鉴权。
			// Fail-closed：回调未装配一律拒绝。文档没有消息历史，不推送
			// sync_snapshot。
			if strings.HasPrefix(payload.ChannelID, idgen.PrefixDoc+"_") {
				if h.AuthorizeDoc == nil || !h.AuthorizeDoc(client.userID, payload.ChannelID) {
					errData, _ := json.Marshal(map[string]interface{}{
						"type": "error",
						"payload": map[string]interface{}{
							"code":    string(coreerrors.SHAREDOC_PERMISSION_DENIED),
							"message": coreerrors.GetChineseMessage(coreerrors.SHAREDOC_PERMISSION_DENIED),
						},
					})
					h.SendToClient(client, errData)
					return
				}
				client.subscribe(payload.ChannelID)
				return
			}
			// Check channel authorization if a callback is registered
			if h.AuthorizeChannel != nil && !h.AuthorizeChannel(client.userID, payload.ChannelID) {
				// Send error back to the client via the hub loop to avoid races.
				errData, _ := json.Marshal(map[string]interface{}{
					"type": "error",
					"payload": map[string]interface{}{
						"code":    string(coreerrors.CHANNEL_ACCESS_DENIED),
						"message": coreerrors.GetChineseMessage(coreerrors.CHANNEL_ACCESS_DENIED),
					},
				})
				h.SendToClient(client, errData)
				return
			}
			client.subscribe(payload.ChannelID)
			// H18: Send sync_snapshot after successful subscription so the
			// client can render initial state (recent messages, pinned ids, unread).
			if h.OnSubscribeSync != nil {
				if snapshot := h.OnSubscribeSync(client.userID, payload.ChannelID); snapshot != nil {
					snapshotData, _ := json.Marshal(map[string]interface{}{
						"type": "sync_snapshot",
						"payload": map[string]interface{}{
							"channelId": payload.ChannelID,
							"data":      snapshot,
						},
					})
					h.SendToClient(client, snapshotData)
				}
			}
		}
	case "unsubscribe":
		var payload struct {
			ChannelID string `json:"channelId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.ChannelID != "" {
			client.unsubscribe(payload.ChannelID)
		}
	case "subscribe_room":
		var payload struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.SessionID != "" {
			if h.AuthorizeRoom != nil && !h.AuthorizeRoom(client.userID, payload.SessionID) {
				errData, _ := json.Marshal(map[string]interface{}{
					"type": "error",
					"payload": map[string]interface{}{
						"code":    string(coreerrors.ROOM_ACCESS_DENIED),
						"message": coreerrors.GetChineseMessage(coreerrors.ROOM_ACCESS_DENIED),
					},
				})
				h.SendToClient(client, errData)
				return
			}
			client.subscribeRoom(payload.SessionID)
		}
	case "unsubscribe_room":
		var payload struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.SessionID != "" {
			client.unsubscribeRoom(payload.SessionID)
		}
	case "subscribe_whiteboard":
		var payload struct {
			WhiteboardID string `json:"whiteboardId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.WhiteboardID != "" {
			// M11: 笔迹按白板定向广播，订阅前必须校验请求者对白板所属空间有
			// 成员权限；回调未装配时一律拒绝（fail-closed），与 subscribe 的
			// AuthorizeChannel、subscribe_room 的 AuthorizeRoom 对齐。
			if h.AuthorizeWhiteboard == nil || !h.AuthorizeWhiteboard(client.userID, payload.WhiteboardID) {
				errData, _ := json.Marshal(map[string]interface{}{
					"type": "error",
					"payload": map[string]interface{}{
						"code":    string(coreerrors.WHITEBOARD_NO_PERMISSION),
						"message": coreerrors.GetChineseMessage(coreerrors.WHITEBOARD_NO_PERMISSION),
					},
				})
				h.SendToClient(client, errData)
				return
			}
			client.subscribeWhiteboard(payload.WhiteboardID)
			if h.OnWhiteboardJoin != nil {
				go h.OnWhiteboardJoin(payload.WhiteboardID, client.userID)
			}
		}
	case "unsubscribe_whiteboard":
		var payload struct {
			WhiteboardID string `json:"whiteboardId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.WhiteboardID != "" {
			// A2-S1：显式退订时向同白板其余订阅者代发 action:"leave" 的
			// whiteboard_cursor（兜底，防止其他端光标残留）。对从未订阅的
			// 白板退订保持静默（不广播、不触发 leave 回调）。
			if client.isSubscribedWhiteboard(payload.WhiteboardID) {
				client.unsubscribeWhiteboard(payload.WhiteboardID)
				if h.OnWhiteboardLeave != nil {
					go h.OnWhiteboardLeave(payload.WhiteboardID, client.userID)
				}
				h.broadcastWhiteboardLeave(payload.WhiteboardID, client)
			}
		}
	case "whiteboard_cursor":
		// A2-S1（DES-20261001-01 §3.3）：白板光标路由。只转发给同白板的
		// 其他订阅者且剔除发送者（不回显）。payload.whiteboardId 必须已在
		// 发送者的 whiteboards 订阅集中——订阅本身经 subscribe_whiteboard
		// 的 AuthorizeWhiteboard 校验（M11），此处天然把光标路由收窄到
		// 授权可见的白板。服务端注入 userId/action/ts，不信任客户端自报；
		// 坐标 clamp 到 [0,1]（归一化画布坐标），非法 action 视为 move。
		// 高频事件：独立限速桶 15 msg/s（见 typeRateLimits）。不落库。
		var payload struct {
			WhiteboardID string  `json:"whiteboardId"`
			X            float64 `json:"x"`
			Y            float64 `json:"y"`
			Action       string  `json:"action"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.WhiteboardID == "" {
			return
		}
		if !client.isSubscribedWhiteboard(payload.WhiteboardID) {
			return
		}
		payload.X = clamp01(payload.X)
		payload.Y = clamp01(payload.Y)
		action := payload.Action
		if action != "move" && action != "leave" {
			action = "move"
		}
		cursorPayload := map[string]interface{}{
			"whiteboardId": payload.WhiteboardID,
			"userId":       client.userID,
			"action":       action,
			"ts":           time.Now().UnixMilli(),
		}
		if action == "move" {
			cursorPayload["x"] = payload.X
			cursorPayload["y"] = payload.Y
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type":    "whiteboard_cursor",
			"payload": cursorPayload,
		})
		h.BroadcastToWhiteboardExcept(payload.WhiteboardID, client, data)
	case "message_ack":
		// Handle message acknowledgment (will be processed by message module)
		var payload struct {
			MessageID string `json:"messageId"`
		}
		json.Unmarshal(msg.Payload, &payload)
		h.log.Debug("message ack received", "user_id", client.userID, "message_id", payload.MessageID)
	case "presence_update":
		var payload struct {
			Status       string `json:"status"`
			CustomStatus string `json:"customStatus"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil {
			// 枚举校验：拒绝非法 status 值
			if payload.Status != "" && !isValidPresenceStatus(payload.Status) {
				h.log.Warn("invalid presence status received", "user_id", client.userID, "status", payload.Status)
				return
			}
			h.log.Debug("presence update", "user_id", client.userID, "status", payload.Status)
		// 同步持久化改为异步，避免阻塞整个 Hub 事件循环
		if h.OnPresenceUpdate != nil {
			go func(userID, status, customStatus string) {
				if err := h.OnPresenceUpdate(userID, status, customStatus); err != nil {
					h.log.Warn("failed to persist presence, skipping broadcast", "user_id", userID, "error", err)
					return
				}
				// H16: Filter invisible status for external broadcast.
				// invisible users appear as offline to others, but keep their real status internally.
				broadcastStatus := status
				if status == "invisible" {
					broadcastStatus = "offline"
				}
				data, _ := json.Marshal(map[string]interface{}{
					"type": "presence_update",
					"payload": map[string]interface{}{
						"userId":       userID,
						"status":       broadcastStatus,
						"customStatus": customStatus,
					},
				})
				h.Broadcast(data)
			}(client.userID, payload.Status, payload.CustomStatus)
		} else {
			// No persistence callback: still broadcast so local state stays in sync.
			broadcastStatus := payload.Status
			if payload.Status == "invisible" {
				broadcastStatus = "offline"
			}
			data, _ := json.Marshal(map[string]interface{}{
				"type": "presence_update",
				"payload": map[string]interface{}{
					"userId":       client.userID,
					"status":       broadcastStatus,
					"customStatus": payload.CustomStatus,
				},
			})
			h.Broadcast(data)
		}
		}
	case "typing":
		var payload struct {
			ChannelID string `json:"channelId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil {
			data, _ := json.Marshal(map[string]interface{}{
				"type": "typing",
				"payload": map[string]interface{}{
					"userId":    client.userID,
					"channelId": payload.ChannelID,
				},
			})
			h.BroadcastToChannel(payload.ChannelID, data)
		}
	case "voice_state_change",
		"raise_hand",
		"stroke_draw",
		"stroke_erase",
		"recording_started",
		"recording_stopped":
		var payload struct {
			ChannelID string `json:"channelId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.ChannelID != "" {
			data, _ := json.Marshal(map[string]interface{}{
				"type":    msg.Type,
				"userId":  client.userID,
				"payload": msg.Payload,
			})
			h.BroadcastToChannel(payload.ChannelID, data)
		}
	case "screenshare_started":
		// A5-S1（DES-20261001-01 §6.2）：登记「频道 → 共享者」内存缓存，
		// 供 screenshare_annotate 的 O(1) 共享者校验；原状态广播行为不变。
		// 一个连接同时至多共享一个频道：重复 started 覆盖旧登记。
		var payload struct {
			ChannelID string `json:"channelId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.ChannelID != "" {
			h.markChannelSharer(payload.ChannelID, client)
			data, _ := json.Marshal(map[string]interface{}{
				"type":    msg.Type,
				"userId":  client.userID,
				"payload": msg.Payload,
			})
			h.BroadcastToChannel(payload.ChannelID, data)
		}
	case "screenshare_stopped":
		// A5-S1：清理共享者缓存并向频道代发 {tool:"clear"}（观看端清空
		// 标注叠加层，防止共享结束后残留笔迹）；原状态广播行为不变。
		var payload struct {
			ChannelID string `json:"channelId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.ChannelID != "" {
			h.clearChannelSharer(payload.ChannelID, client)
			data, _ := json.Marshal(map[string]interface{}{
				"type":    msg.Type,
				"userId":  client.userID,
				"payload": msg.Payload,
			})
			h.BroadcastToChannel(payload.ChannelID, data)
		}
	case "screenshare_annotate":
		// A5-S1（DES-20261001-01 §6.2）：屏幕共享标注路由。仅当前共享者可
		// 发（channelSharers[ channelId ] == 发送者），否则静默丢弃——H14
		// 口径：不回错误，避免攻击者据此探测「谁是共享者/频道有无共享」。
		// 高频事件：独立限速桶 20 msg/s（见 typeRateLimits）。坐标 clamp 到
		// [0,1]，tool 白名单，points ≤64，color 限 #RRGGBB。服务端注入 by，
		// 不信任客户端自报。以 screenshare_annotation 转发给频道订阅者
		// （含共享者，供其预览回显校验）。不落库。
		var payload struct {
			ChannelID string        `json:"channelId"`
			Tool      string        `json:"tool"`
			Points    [][2]float64  `json:"points"`
			Color     string        `json:"color"`
			SegID     string        `json:"segId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.ChannelID == "" {
			return
		}
		if sharer, ok := h.channelSharerOf(payload.ChannelID); !ok || sharer != client.userID {
			return
		}
		if !isValidAnnotateTool(payload.Tool) {
			return
		}
		if len(payload.Points) > maxAnnotatePoints {
			return
		}
		if payload.Color != "" && !isValidAnnotateColor(payload.Color) {
			return
		}
		points := make([][2]float64, len(payload.Points))
		for i, p := range payload.Points {
			points[i] = [2]float64{clamp01(p[0]), clamp01(p[1])}
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "screenshare_annotation",
			"payload": map[string]interface{}{
				"channelId": payload.ChannelID,
				"tool":      payload.Tool,
				"points":    points,
				"color":     payload.Color,
				"segId":     payload.SegID,
				"by":        client.userID,
			},
		})
		h.BroadcastToChannel(payload.ChannelID, data)
	// A6-S1（DES-20261001-01 §7.2）：远程协助剪贴板同步路由。三个客户端消息
	// 全部走与 remote_assist_control 相同的校验钩子体系（H14 口径：校验失败
	// 静默丢弃，不回错误，避免会话 ID 探测）。独立限速桶 5 msg/s（见
	// typeRateLimits）。全程不落库，会话终止（end/超时/M10 到限）后所有
	// clipboard 消息因校验失败自然失效。
	case "remote_assist_clipboard":
		// 控制端 → 被控端：requester 发起 push（写对方剪贴板）/ pull（读对方
		// 剪贴板）。校验复用 OnRemoteAssistControl（存在 + authorized +
		// sender==requester + payload.targetId==target，四项与控制事件完全
		// 同口径），不另设钩子。text ≤32KB 字节（WS 读帧 64KB 的一半，留
		// JSON 结构与转义余量），超限与空文本 push 静默丢弃（无「清空对方
		// 剪贴板」的产品流程，空 push 只剩骚扰用途）。
		var payload struct {
			SessionID string `json:"sessionId"`
			TargetID  string `json:"targetId"`
			Op        string `json:"op"` // push / pull
			ReqID     string `json:"reqId"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		if payload.SessionID == "" || payload.TargetID == "" {
			return
		}
		if h.OnRemoteAssistControl == nil {
			h.log.Warn("remote assist clipboard dropped: validator not wired",
				"sessionId", payload.SessionID, "userId", client.userID)
			return
		}
		if err := h.OnRemoteAssistControl(payload.SessionID, client.userID, payload.TargetID); err != nil {
			h.log.Warn("remote assist clipboard dropped: validation failed",
				"sessionId", payload.SessionID, "userId", client.userID,
				"targetId", payload.TargetID, "error", err)
			return
		}
		switch payload.Op {
		case "push":
			if payload.Text == "" || len(payload.Text) > maxClipboardTextBytes {
				return
			}
			data, _ := json.Marshal(map[string]interface{}{
				"type": "remote_assist_clipboard_push",
				"payload": map[string]interface{}{
					"sessionId": payload.SessionID,
					"text":      payload.Text,
				},
			})
			h.BroadcastToUser(payload.TargetID, data)
		case "pull":
			// reqId 定案：透传客户端提供的关联 ID（控制端据此把 data 响应与
			// 自己的 pull 配对——服务端不做挂起 pull 状态，无状态转发与 H14
			// 一致）；客户端未提供时服务端生成兜底。透传值限长，防超长串。
			if len(payload.ReqID) > maxClipboardReqIDLen {
				return
			}
			reqID := payload.ReqID
			if reqID == "" {
				reqID = idgen.GenerateID(idgen.PrefixRemoteAssist)
			}
			data, _ := json.Marshal(map[string]interface{}{
				"type": "remote_assist_clipboard_pull",
				"payload": map[string]interface{}{
					"sessionId": payload.SessionID,
					"reqId":     reqID,
				},
			})
			h.BroadcastToUser(payload.TargetID, data)
		}
	case "remote_assist_clipboard_data":
		// 被控端 → 控制端：pull 的应答。发送者必须是会话 target
		// （ValidateFromTarget：存在 + authorized + M10 惰性判定 +
		// sender==target），返回 requester 供路由。payload 中的 targetId
		// 字段不参与校验也不信任——路由目标由会话反查所得。text ≤32KB
		// 字节；reqId 必须携带（应答无关联 ID 即无效），超限静默丢弃。
		// 空 text 照转：被控端剪贴板为空是对 pull 的合法诚实应答。
		var payload struct {
			SessionID string `json:"sessionId"`
			ReqID     string `json:"reqId"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		if payload.SessionID == "" {
			return
		}
		if h.OnRemoteAssistFromTarget == nil {
			h.log.Warn("remote assist clipboard data dropped: validator not wired",
				"sessionId", payload.SessionID, "userId", client.userID)
			return
		}
		requesterID, err := h.OnRemoteAssistFromTarget(payload.SessionID, client.userID)
		if err != nil {
			h.log.Warn("remote assist clipboard data dropped: validation failed",
				"sessionId", payload.SessionID, "userId", client.userID, "error", err)
			return
		}
		if payload.ReqID == "" || len(payload.ReqID) > maxClipboardReqIDLen {
			return
		}
		if len(payload.Text) > maxClipboardTextBytes {
			return
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_clipboard_data",
			"payload": map[string]interface{}{
				"sessionId": payload.SessionID,
				"reqId":     payload.ReqID,
				"text":      payload.Text,
			},
		})
		h.BroadcastToUser(requesterID, data)
	case "remote_assist_chat":
		// A7-S1（DES-20261001-01 §8.1）：远程协助会话内聊天。发送者必须是
		// 会话 requester 或 target（ValidateChatParticipant，返回对端供路由），
		// 第三者即使猜中 sessionId 也被静默丢弃。≤2000 rune（客户端保证）+
		// ≤8KB 字节（服务端双保险）。from 服务端注入（不信客户端自报），
		// at 为服务端毫秒时间戳。双方各收一份（含发送者——多端登录时其他
		// 设备同步显示）。会话即焚不落库；独立限速桶 10 msg/s。
		var payload struct {
			SessionID string `json:"sessionId"`
			Text      string `json:"text"`
			// payload.targetId 由客户端携带但服务端不校验：会话参与者由
			// 校验器反查，消息本来就投递给双方，自报 target 无授权意义。
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		if payload.SessionID == "" {
			return
		}
		if h.OnRemoteAssistChatParticipant == nil {
			h.log.Warn("remote assist chat dropped: validator not wired",
				"sessionId", payload.SessionID, "userId", client.userID)
			return
		}
		peerID, err := h.OnRemoteAssistChatParticipant(payload.SessionID, client.userID)
		if err != nil {
			h.log.Warn("remote assist chat dropped: validation failed",
				"sessionId", payload.SessionID, "userId", client.userID, "error", err)
			return
		}
		if payload.Text == "" || len(payload.Text) > maxAssistChatTextBytes ||
			utf8.RuneCountInString(payload.Text) > maxAssistChatRunes {
			return
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_chat",
			"payload": map[string]interface{}{
				"sessionId": payload.SessionID,
				"from":      client.userID,
				"text":      payload.Text,
				"at":        time.Now().UnixMilli(),
			},
		})
		// BroadcastToUsers 对每个用户各走一次 BroadcastToUser（事件循环内
		// 按连接扇出），等价「双方各 BroadcastToUser」。
		h.BroadcastToUsers([]string{client.userID, peerID}, data)
	// H16: 共享文档协同事件（M23）统一按 payload.docId（文档原始 id）路由：
	// 广播键 = 客户端订阅键 = 文档原始 id。发送者必须先通过 AuthorizeDoc
	// 鉴权（防止非成员向他人文档注入伪造操作/出没记录），回调未装配时
	// fail-closed 丢弃。客户端按消息顶层 userId 过滤自己发的操作（协议
	// 语义见 DES-2026-0713 §5.6），服务端不剔除发送者。
	case "doc_cursor": // M23: 协同编辑 - 光标位置同步
		var payload struct {
			DocID    string `json:"docId"`
			Position int    `json:"position"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.DocID != "" &&
			h.AuthorizeDoc != nil && h.AuthorizeDoc(client.userID, payload.DocID) {
			data, _ := json.Marshal(map[string]interface{}{
				"type":   "doc_cursor",
				"userId": client.userID,
				"payload": map[string]interface{}{
					"docId":    payload.DocID,
					"position": payload.Position,
				},
			})
			h.BroadcastToChannel(payload.DocID, data)
		}
	case "doc_selection": // M23: 协同编辑 - 选区同步
		var payload struct {
			DocID string `json:"docId"`
			Start int    `json:"start"`
			End   int    `json:"end"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.DocID != "" &&
			h.AuthorizeDoc != nil && h.AuthorizeDoc(client.userID, payload.DocID) {
			data, _ := json.Marshal(map[string]interface{}{
				"type":   "doc_selection",
				"userId": client.userID,
				"payload": map[string]interface{}{
					"docId": payload.DocID,
					"start": payload.Start,
					"end":   payload.End,
				},
			})
			h.BroadcastToChannel(payload.DocID, data)
		}
	case "doc_op": // M23: 协同编辑 - 编辑操作广播
		var payload struct {
			DocID   string `json:"docId"`
			OpType  string `json:"opType"` // insert / delete
			Position int   `json:"position"`
			Text    string `json:"text"`
			Length  int    `json:"length"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.DocID != "" &&
			h.AuthorizeDoc != nil && h.AuthorizeDoc(client.userID, payload.DocID) {
			data, _ := json.Marshal(map[string]interface{}{
				"type":   "doc_op",
				"userId": client.userID,
				"payload": map[string]interface{}{
					"docId":    payload.DocID,
					"opType":   payload.OpType,
					"position": payload.Position,
					"text":     payload.Text,
					"length":   payload.Length,
					"version":  payload.Version,
				},
			})
			h.BroadcastToChannel(payload.DocID, data)
		}
	case "doc_editor_joined": // M23: 协同编辑 - 用户开始编辑
		var payload struct {
			DocID string `json:"docId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.DocID != "" &&
			h.AuthorizeDoc != nil && h.AuthorizeDoc(client.userID, payload.DocID) {
			if h.OnDocEditorJoin != nil {
				h.OnDocEditorJoin(payload.DocID, client.userID)
			}
			data, _ := json.Marshal(map[string]interface{}{
				"type":   "doc_editor_joined",
				"userId": client.userID,
				"payload": map[string]interface{}{
					"docId": payload.DocID,
				},
			})
			h.BroadcastToChannel(payload.DocID, data)
		}
	case "doc_editor_left": // M23: 协同编辑 - 用户离开编辑
		var payload struct {
			DocID string `json:"docId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.DocID != "" &&
			h.AuthorizeDoc != nil && h.AuthorizeDoc(client.userID, payload.DocID) {
			if h.OnDocEditorLeave != nil {
				h.OnDocEditorLeave(payload.DocID, client.userID)
			}
			data, _ := json.Marshal(map[string]interface{}{
				"type":   "doc_editor_left",
				"userId": client.userID,
				"payload": map[string]interface{}{
					"docId": payload.DocID,
				},
			})
			h.BroadcastToChannel(payload.DocID, data)
		}
	case "remote_assist_control": // T49: 远程协助 - 鼠标/键盘控制事件转发
		// 控制事件由 requester 发起，转发给 target（被控端）。服务端不解析
		// 具体的鼠标/键盘数据，但 H14：转发前必须按 sessionId 反查会话并
		// 校验「会话存在 + 仍处于已授权状态 + 发送者就是该会话的 requester
		// + payload.targetId 就是该会话的 target」。校验由 remoteassist
		// 服务提供（hub 不直接访问数据库，避免反向依赖）。
		// 任一条件不满足即静默丢弃：不回错误，避免攻击者据此探测 sessionId，
		// 也避免被控端因伪造事件被反复打断连接。
		var payload struct {
			SessionID string          `json:"sessionId"`
			TargetID  string          `json:"targetId"`
			EventType string          `json:"eventType"` // mouse_move / mouse_down / mouse_up / key_down / key_up ...
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		if payload.SessionID == "" || payload.TargetID == "" {
			return
		}
		if h.OnRemoteAssistControl == nil {
			h.log.Warn("remote assist control dropped: validator not wired",
				"sessionId", payload.SessionID, "userId", client.userID)
			return
		}
		if err := h.OnRemoteAssistControl(payload.SessionID, client.userID, payload.TargetID); err != nil {
			h.log.Warn("remote assist control dropped: validation failed",
				"sessionId", payload.SessionID, "userId", client.userID,
				"targetId", payload.TargetID, "error", err)
			return
		}

		data, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_control",
			"payload": map[string]interface{}{
				"sessionId":   payload.SessionID,
				"requesterId": client.userID,
				"eventType":   payload.EventType,
				"data":        payload.Data,
			},
		})
		h.BroadcastToUser(payload.TargetID, data)
	}
}

// HandleWebSocket upgrades HTTP to WebSocket.
// Rejects connections if the user already has 3 concurrent connections.
func (h *Hub) HandleWebSocket(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(string)
	if uid == "" {
		coreerrors.JSONError(c, coreerrors.New(coreerrors.AUTH_TOKEN_MISSING, "websocket authentication required"))
		return
	}

	// Limit to 3 concurrent connections per user
	h.mu.RLock()
	userCount := h.userClientCount[uid]
	h.mu.RUnlock()
	if userCount >= 3 {
		coreerrors.JSONError(c, coreerrors.New(coreerrors.WS_CONNECTION_LIMIT, "maximum 3 concurrent websocket connections per user"))
		return
	}

	// Negotiate Sec-WebSocket-Protocol if the caller requested one.
	// We copy the package-level upgrader so concurrent requests do not race
	// on the Subprotocols slice.
	wsUpgrader := Upgrader
	if proto, ok := c.Get("ws_subprotocol"); ok {
		if p, ok := proto.(string); ok && p != "" {
			wsUpgrader.Subprotocols = []string{p}
		}
	}

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Error("websocket upgrade failed", "error", err)
		// Don't write response here; upgrader already handles it
		return
	}

	client := &Client{
		hub:         h,
		conn:        conn,
		send:        make(chan []byte, 256),
		userID:      uid,
		channels:    make(map[string]bool),
		rateLimiter: newRateLimiter(),
	}
	// A4-S2：保存 access token 的 session_id 声明（HTTP 鉴权处已解析进
	// gin 上下文；routes.go 的 query-token 路径同样 Set）。旧 token 无此
	// 声明时保持空串——定向广播不跳过无 sessionID 的连接（宁多发不漏发）。
	if sid, ok := c.Get("session_id"); ok {
		if s, ok := sid.(string); ok {
			client.sessionID = s
		}
	}

	h.register <- client

	// H18: Send welcome event on connection established so the client
	// knows the handshake is complete and can begin subscribing.
	welcomeData, _ := json.Marshal(map[string]interface{}{
		"type": "welcome",
		"payload": map[string]interface{}{
			"userId":     uid,
			"serverTime": time.Now().UTC().Format(time.RFC3339),
			"protocol":   "1.0",
		},
	})
	h.SendToClient(client, welcomeData)

	go client.writePump()
	go client.readPump()
}

// isValidPresenceStatus 校验 status 是否为合法的在线状态枚举值。
// 空字符串视为合法（允许仅刷新 customStatus 的场景）。
func isValidPresenceStatus(status string) bool {
	switch status {
	case "online", "away", "dnd", "offline", "invisible", "gaming", "":
		return true
	}
	return false
}

// clamp01 把归一化坐标收敛到 [0,1]（A2-S1：whiteboard_cursor 坐标越界
// 不得污染其他端渲染）。
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// A5-S1（DES-20261001-01 §6.2）：标注协议常量与校验。
const (
	// maxAnnotatePoints 单条标注事件的坐标点上限（笔迹分段批量）。
	maxAnnotatePoints = 64
)

// A6-S1（DES-20261001-01 §7.2）：剪贴板同步协议常量。
const (
	// maxClipboardTextBytes 剪贴板单条文本的字节上限（32KB）。WS 读帧上限
	// 64KB，一半留给 JSON 结构与转义余量；超限静默丢弃（H14 口径）。
	maxClipboardTextBytes = 32 * 1024
	// maxClipboardReqIDLen pull 关联 ID（透传自客户端）的长度上限。
	maxClipboardReqIDLen = 64
)

// A7-S1（DES-20261001-01 §8.1）：会话内聊天协议常量。rune 上限 2000 由
// 客户端界面保证，服务端字节（8KB）与 rune 双校验——超限一律静默丢弃。
const (
	maxAssistChatTextBytes = 8 * 1024
	maxAssistChatRunes     = 2000
)

// isValidAnnotateTool 校验标注工具白名单：pen/arrow/rect 画图形，eraser
// 删单段（带 segId），clear 清空（无 points）。
func isValidAnnotateTool(tool string) bool {
	switch tool {
	case "pen", "arrow", "rect", "eraser", "clear":
		return true
	}
	return false
}

// isValidAnnotateColor 校验颜色必须是 #RRGGBB 十六进制格式（小写客户端
// 常见；大小写均接受，其余格式一律拒绝——自由文本色值可能携带注入内容
// 直接进入其他端渲染）。
func isValidAnnotateColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := color[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// markChannelSharer 登记频道共享者（screenshare_started）。一个连接同时
// 至多共享一个频道：已在共享时先清理旧频道的登记。并发由 sharersMu 串行。
func (h *Hub) markChannelSharer(channelID string, client *Client) {
	h.sharersMu.Lock()
	if client.sharingChannel != "" && client.sharingChannel != channelID {
		if h.channelSharers[client.sharingChannel] == client.userID {
			delete(h.channelSharers, client.sharingChannel)
		}
	}
	h.channelSharers[channelID] = client.userID
	client.sharingChannel = channelID
	h.sharersMu.Unlock()
}

// clearChannelSharer 清理频道共享者登记（screenshare_stopped / 断连）。
// 仅当登记的共享者确实是该 client 时才清理并代发 {tool:"clear"}——防止
// 后来者覆盖登记后，旧共享者的停止事件误清新会话的标注。
func (h *Hub) clearChannelSharer(channelID string, client *Client) {
	h.sharersMu.Lock()
	if h.channelSharers[channelID] != client.userID || client.sharingChannel != channelID {
		h.sharersMu.Unlock()
		return
	}
	delete(h.channelSharers, channelID)
	client.sharingChannel = ""
	h.sharersMu.Unlock()

	// 收尾：向频道全体订阅者（含已停止共享的原共享者）代发清空标注。
	// 仅做非阻塞 chanMsg 投递，readPump / Run 循环内调用均不会死锁。
	h.broadcastAnnotationClear(channelID, client.userID)
}

// channelSharerOf 返回频道当前共享者（A5-S1：标注路由的 O(1) 校验）。
func (h *Hub) channelSharerOf(channelID string) (string, bool) {
	h.sharersMu.Lock()
	defer h.sharersMu.Unlock()
	userID, ok := h.channelSharers[channelID]
	return userID, ok
}

// releaseClientSharer 断连清理（unregister 路径，Run 循环调用）：在
// sharersMu 保护下读取并清空该连接的共享登记。仅当缓存登记的共享者确实是
// 该连接时才返回 (channelID, userID)——调用方据此在锁外代发 {tool:"clear"}
// 兜底；登记已被同用户新连接覆盖时不代发（新共享还在进行）。
func (h *Hub) releaseClientSharer(client *Client) (string, string) {
	h.sharersMu.Lock()
	defer h.sharersMu.Unlock()
	channelID := client.sharingChannel
	if channelID == "" || h.channelSharers[channelID] != client.userID {
		return "", ""
	}
	delete(h.channelSharers, channelID)
	client.sharingChannel = ""
	return channelID, client.userID
}

// broadcastAnnotationClear 以共享者身份向频道代发 tool:"clear" 的
// screenshare_annotation（A5-S1 收尾：共享停止/断连后观看端叠加层不得
// 残留笔迹）。by 记录共享者 id，便于客户端区分清空来源。
func (h *Hub) broadcastAnnotationClear(channelID, sharerUserID string) {
	data, _ := json.Marshal(map[string]interface{}{
		"type": "screenshare_annotation",
		"payload": map[string]interface{}{
			"channelId": channelID,
			"tool":      "clear",
			"by":        sharerUserID,
		},
	})
	h.BroadcastToChannel(channelID, data)
}

// broadcastWhiteboardLeave 以该 client 的身份向同白板其余订阅者代发
// action:"leave" 的 whiteboard_cursor 事件（A2-S1 兜底：客户端显式退订或
// 连接断开后，其他端不得残留其光标）。leave 事件不带坐标；发送者被剔除。
// 仅向 chanMsg 非阻塞投递，可在 Hub 事件循环内安全调用（不会死锁）。
func (h *Hub) broadcastWhiteboardLeave(whiteboardID string, client *Client) {
	data, _ := json.Marshal(map[string]interface{}{
		"type": "whiteboard_cursor",
		"payload": map[string]interface{}{
			"whiteboardId": whiteboardID,
			"userId":       client.userID,
			"action":       "leave",
			"ts":           time.Now().UnixMilli(),
		},
	})
	h.BroadcastToWhiteboardExcept(whiteboardID, client, data)
}
