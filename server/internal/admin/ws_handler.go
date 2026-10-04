package admin

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	applogger "ridgericetalk/internal/logger"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Admin WebSocket connection tuning constants. These mirror the values used
// by the main client WebSocket handler (internal/realtime/hub.go) so that
// admin connections enjoy the same heartbeat / timeout behaviour.
const (
	adminWriteWait   = 10 * time.Second
	adminPongWait    = 90 * time.Second
	adminPingPeriod  = 30 * time.Second
	adminReadLimit   = 64 * 1024
	// adminSubBufferSize must match the buffer size used by Hub.SubscribeAdmin
	// so the admin write pump does not block the Hub broadcast loop.
)

// AdminWSHandler handles admin WebSocket connections on the dedicated admin
// engine (port cfg.AdminPort). Unlike the main /ws handler which creates a
// full Hub Client (with presence tracking, channel subscriptions, rate
// limiting), the admin handler only subscribes to the Hub's broadcast fan-out
// and filters messages down to the module_status_changed type — the only event
// the admin frontend needs in real time to drive the 5-minute module grace
// period countdown (audit finding C2).
type AdminWSHandler struct {
	hub *realtime.Hub
	cfg *config.Config
	db  *gorm.DB
	log *applogger.Logger
}

// NewAdminWSHandler constructs an admin WebSocket handler. The hub is the
// shared Hub instance created in cmd/server/main.go; the same instance is
// used by the main engine so module_status_changed broadcasts reach admin
// subscribers without any cross-engine plumbing.
func NewAdminWSHandler(hub *realtime.Hub, cfg *config.Config, db *gorm.DB, log *applogger.Logger) *AdminWSHandler {
	return &AdminWSHandler{hub: hub, cfg: cfg, db: db, log: log}
}

// HandleWS is the gin handler for GET /admin/ws on the admin engine.
//
// Authentication mirrors the main /ws route: the access token is accepted via
// the Sec-WebSocket-Protocol header ("access_token.<token>", preferred) or
// via the "token"/"access_token" query parameters (legacy fallback). After
// validating the JWT and TokenVersion against the database, the handler
// additionally requires the ADMIN or OWNER role — non-admin users receive a
// 403 and the connection is not upgraded.
//
// Once upgraded, the handler subscribes to the Hub's broadcast fan-out and
// forwards ONLY module_status_changed messages to the client. All other
// broadcast types (presence_update, channel messages, voice state, etc.)
// are silently dropped because they are irrelevant to the admin dashboard.
func (h *AdminWSHandler) HandleWS(c *gin.Context) {
	token, wantSubprotocol := extractAdminWSToken(c)
	if token == "" {
		errors.JSONError(c, errors.New(errors.AUTH_UNAUTHORIZED, "websocket authentication required"))
		return
	}

	claims, err := middleware.ParseToken(token, h.cfg)
	if err != nil {
		errors.JSONError(c, errors.New(errors.AUTH_TOKEN_INVALID, "invalid token"))
		return
	}
	if claims.Type != "access" {
		errors.JSONError(c, errors.New(errors.AUTH_TOKEN_INVALID, "invalid token type"))
		return
	}

	// Validate token version against the database (revocation check) and
	// re-read the role from the DB so a recently-demoted user cannot keep
	// using an admin token issued before the role change.
	var user model.User
	if err := h.db.First(&user, "id = ?", claims.UserID).Error; err != nil {
		errors.JSONError(c, errors.New(errors.AUTH_UNAUTHORIZED, "user not found"))
		return
	}
	if claims.TokenVersion != user.TokenVersion {
		errors.JSONError(c, errors.New(errors.AUTH_TOKEN_INVALID, "token revoked"))
		return
	}
	if user.Role != middleware.RoleAdmin && user.Role != middleware.RoleOwner {
		errors.JSONError(c, errors.New(errors.AUTH_FORBIDDEN, "admin access required"))
		return
	}

	// Upgrade using the shared upgrader. Copy it so concurrent requests do
	// not race on the Subprotocols slice (same pattern as Hub.HandleWebSocket).
	wsUpgrader := realtime.Upgrader
	if wantSubprotocol {
		wsUpgrader.Subprotocols = []string{"rrt"}
	}
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.log.Error("admin websocket upgrade failed", "error", err, "user_id", claims.UserID)
		// upgrader.Upgrade already wrote an error response; do not write again.
		return
	}
	defer conn.Close()

	h.log.Info("admin websocket connected", "user_id", claims.UserID, "role", user.Role)

	// A11 (DES-20261001-01 §12.3): push an active-alerts snapshot BEFORE the
	// write pump starts consuming broadcasts, so a freshly opened dashboard
	// shows the current alert state instead of waiting up to one evaluation
	// cycle for the next admin_alert event. Snapshot failures are logged but
	// do not abort the connection (module_status_changed still flows).
	h.pushAlertsSnapshot(conn)

	// Subscribe to Hub broadcasts. The subscriber receives every Broadcast()
	// message; we filter below so only admin-relevant types
	// (module_status_changed, admin_alert, admin_alert_snapshot) are forwarded.
	sub, unsubscribe := h.hub.SubscribeAdmin()
	defer unsubscribe()

	// Write pump: forward filtered broadcasts + keepalive pings.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(adminPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-sub:
				conn.SetWriteDeadline(time.Now().Add(adminWriteWait))
				if !ok {
					// Hub closed the subscriber channel (unregister).
					conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				if !isAdminRelevantBroadcast(msg) {
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(adminWriteWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Read pump: the admin frontend does not send data frames, but we must
	// keep reading to process pong frames and detect client disconnect.
	conn.SetReadLimit(adminReadLimit)
	conn.SetReadDeadline(time.Now().Add(adminPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(adminPongWait))
		return nil
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	<-done
}

// extractAdminWSToken pulls the access token from the request using the same
// precedence as the main /ws route (cmd/server/main.go):
//  1. Sec-WebSocket-Protocol header entry "access_token.<token>"
//  2. "token" query parameter (legacy)
//  3. "access_token" query parameter (legacy, deprecated)
//
// The second return value reports whether the client also requested the "rrt"
// subprotocol so the upgrader can negotiate it in the response.
func extractAdminWSToken(c *gin.Context) (token string, wantSubprotocol bool) {
	if protoHeader := c.GetHeader("Sec-WebSocket-Protocol"); protoHeader != "" {
		for _, p := range strings.Split(protoHeader, ",") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "access_token.") {
				token = strings.TrimPrefix(p, "access_token.")
			}
			if p == "rrt" {
				wantSubprotocol = true
			}
		}
	}
	if token == "" {
		token = c.Query("token")
	}
	if token == "" {
		token = c.Query("access_token")
	}
	return token, wantSubprotocol
}

// isAdminRelevantBroadcast reports whether a broadcast message should be
// forwarded to admin WebSocket clients. The allowlist:
//   - module_status_changed — drives the 5-minute grace period countdown
//     banner in the admin frontend (audit finding C2);
//   - admin_alert — live alert open/update/resolved events emitted by the
//     alert evaluator (DES-20261001-01 §12.3);
//   - admin_alert_snapshot — reserved for connection-time snapshots.
//
// Other broadcast types (presence_update, welcome, etc.) are intentionally
// suppressed because they carry user-facing presence data that the admin
// dashboard does not render and should not leak over the admin port.
func isAdminRelevantBroadcast(msg []byte) bool {
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &peek); err != nil {
		return false
	}
	switch peek.Type {
	case "module_status_changed", WSEventAdminAlert, WSEventAdminAlertSnapshot:
		return true
	}
	return false
}

// pushAlertsSnapshot sends the current active alerts to a freshly connected
// admin client as a single admin_alert_snapshot message (A11 §12.3: 新开页
// 不丢当前态). Best-effort: a DB error downgrades to an empty snapshot so the
// connection and subsequent broadcasts are unaffected.
func (h *AdminWSHandler) pushAlertsSnapshot(conn *websocket.Conn) {
	alerts := []model.AdminAlert{}
	if err := h.db.Where("resolved_at IS NULL").Order("last_seen_at DESC").Find(&alerts).Error; err != nil {
		h.log.Error("failed to load admin alerts snapshot", "error", err)
	}
	payload := make([]map[string]interface{}, 0, len(alerts))
	for i := range alerts {
		payload = append(payload, AlertEventPayload("snapshot", &alerts[i]))
	}
	msg, err := json.Marshal(map[string]interface{}{
		"type":    WSEventAdminAlertSnapshot,
		"payload": map[string]interface{}{"alerts": payload},
	})
	if err != nil {
		h.log.Error("failed to marshal admin alerts snapshot", "error", err)
		return
	}
	conn.SetWriteDeadline(time.Now().Add(adminWriteWait))
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		h.log.Warn("failed to push admin alerts snapshot", "error", err)
	}
}
