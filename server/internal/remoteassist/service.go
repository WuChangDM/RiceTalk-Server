// Package remoteassist implements the T49 remote assist control signaling
// channel. A requester asks a target for remote control of their computer;
// the target must explicitly authorize the request before any control
// events (mouse/keyboard) can be exchanged.
package remoteassist

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/admin"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
)

// Session status constants.
const (
	StatusPending    = "pending"
	StatusAuthorized = "authorized"
	StatusRejected   = "rejected"
	StatusEnded      = "ended"
	StatusTimeout    = "timeout"
)

// RequestTimeout is how long a request may stay pending before the server
// auto-rejects it. Design doc T49 §"超时自动拒绝": 30 seconds.
const RequestTimeout = 30 * time.Second

// MaxSessionDuration is how long an authorized (active) session may run before
// the server force-ends it (M10 / DES-2026-0912-07 §5.2). Before this the cap
// existed only client-side (RemoteAssistPanel REMOTE_ASSIST_MAX_DURATION_MS);
// a modified or unofficial client could keep a session alive indefinitely.
// The deadline is derived from the session's UpdatedAt (refreshed to the
// authorization moment by markStatus), so lazy checks and the notification
// timer judge against the same instant. No activity renewal: matching the
// client's absolute 30-minute cap — renewing on control events would let a
// continuously active session run forever, defeating the point.
const MaxSessionDuration = 30 * time.Minute

// WS reasons used in the "remote_assist_end" payload. The duration cap reuses
// the existing end event (whitelisted end-to-end to the client panel) instead
// of introducing a new event type, mirroring how the pending timeout reuses it
// with reason "timeout".
const reasonUserEnded = "user_ended"
const reasonTimeout = "timeout"
const reasonDurationLimit = "duration_limit"

// Permissions describes the control capabilities granted by the target.
//
// The struct is persisted as a JSON blob on model.RemoteAssistSession, so new
// fields are backward compatible by construction: a session written by an older
// client simply omits "screen" and unmarshals to the zero value (false), which
// means "no screen capture" — the pre-existing blind-operation behaviour.
// See DES-2026-0912-07 §4.1.
type Permissions struct {
	Mouse    bool `json:"mouse"`
	Keyboard bool `json:"keyboard"`
	// Screen grants one-way screen capture from the target to the requester
	// (remote assist 画面回传). false = requester operates blind (legacy).
	Screen bool `json:"screen"`
}

// Service handles remote assist business logic.
type Service struct {
	db  *gorm.DB
	hub *realtime.Hub
	// auditSvc is used to record security audit events for remote assist
	// operations (request/authorize/reject/control/end).
	auditSvc *admin.Service
	// timeoutTimers tracks in-flight timers so they can be cancelled before
	// their deadline fires. A session has at most one timer at a time: a
	// pending auto-reject timer (RequestTimeout) while pending, replaced in
	// Authorize by a duration-limit timer (MaxSessionDuration) once
	// authorized. cancelTimeout therefore stops whichever phase is current.
	timeoutTimers map[string]*time.Timer
	mu            sync.Mutex
}

// NewService creates a new remote assist service.
func NewService(db *gorm.DB, hub *realtime.Hub, auditSvc *admin.Service) *Service {
	return &Service{
		db:            db,
		hub:           hub,
		auditSvc:      auditSvc,
		timeoutTimers: make(map[string]*time.Timer),
	}
}

// Session represents a remote assist session in API responses.
type Session struct {
	ID          string      `json:"id"`
	RequesterID string      `json:"requesterId"`
	TargetID    string      `json:"targetId"`
	ChannelID   string      `json:"channelId"`
	Status      string      `json:"status"`
	Permissions Permissions `json:"permissions"`
	ExpiresAt   time.Time   `json:"expiresAt"`
	CreatedAt   time.Time   `json:"createdAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
}

// toSessionResponse converts a model into its API representation, decoding
// the JSON-encoded permissions column into a typed struct.
func toSessionResponse(s *model.RemoteAssistSession) Session {
	resp := Session{
		ID:          s.ID,
		RequesterID: s.RequesterID,
		TargetID:    s.TargetID,
		ChannelID:   s.ChannelID,
		Status:      s.Status,
		ExpiresAt:   s.ExpiresAt,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
	if s.Permissions != "" {
		_ = json.Unmarshal([]byte(s.Permissions), &resp.Permissions)
	}
	return resp
}

// CreateRequest creates a new pending remote assist request.
// channelId is optional (FIX-20261003-01 NJ-16): when provided, both parties
// must be members of the channel's space; when omitted, the pair must share at
// least one common space membership. A target may not have more than one
// pending request from the same requester at a time.
func (s *Service) CreateRequest(requesterID, targetID, channelID string, perms Permissions) (Session, error) {
	if requesterID == "" || targetID == "" {
		return Session{}, errors.ErrBadRequest
	}
	if requesterID == targetID {
		return Session{}, errors.New(errors.REMOTE_ASSIST_INVALID_STATE, "cannot request remote assist from yourself")
	}

	// Verify target user exists.
	var target model.User
	if err := s.db.Select("id, username").First(&target, "id = ?", targetID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return Session{}, errors.New(errors.USER_NOT_FOUND, "target user not found")
		}
		return Session{}, errors.ErrInternal
	}

	// Resolve the space that scopes this session: from the channel when given,
	// otherwise from the pair's shared space membership (NJ-16).
	spaceID := ""
	if channelID != "" {
		var channel model.Channel
		if err := s.db.Select("id, space_id").First(&channel, "id = ?", channelID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return Session{}, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
			}
			return Session{}, errors.ErrInternal
		}
		spaceID = channel.SpaceID
		if inSpace, err := s.IsUserInSpace(requesterID, spaceID); err != nil {
			return Session{}, errors.ErrInternal
		} else if !inSpace {
			return Session{}, errors.New(errors.REMOTE_ASSIST_FORBIDDEN, "requester is not a member of the space")
		}
		if targetInSpace, err := s.IsUserInSpace(targetID, spaceID); err != nil {
			return Session{}, errors.ErrInternal
		} else if !targetInSpace {
			return Session{}, errors.New(errors.REMOTE_ASSIST_FORBIDDEN, "target is not a member of the space")
		}
	} else {
		common, err := s.findCommonSpace(requesterID, targetID)
		if err != nil {
			return Session{}, errors.ErrInternal
		}
		if common == "" {
			return Session{}, errors.New(errors.REMOTE_ASSIST_FORBIDDEN, "requester and target are not in a common space")
		}
		spaceID = common
	}

	// Reject if there is already an active (pending or authorized) session
	// between the same requester and target. Over-limit authorized sessions
	// are force-ended here first (M10 lazy enforcement) so a stale row — e.g.
	// left behind after a server restart dropped the in-memory duration
	// timer — cannot block new requests between the same pair forever.
	var existing []model.RemoteAssistSession
	if err := s.db.Where(
		"requester_id = ? AND target_id = ? AND status IN ?", requesterID, targetID,
		[]string{StatusPending, StatusAuthorized},
	).Find(&existing).Error; err != nil {
		return Session{}, errors.ErrInternal
	}
	active := 0
	for i := range existing {
		if !s.expireIfOverLimit(&existing[i]) {
			active++
		}
	}
	if active > 0 {
		return Session{}, errors.New(errors.REMOTE_ASSIST_ALREADY_ACTIVE, "an active remote assist session already exists between these users")
	}

	now := time.Now().UTC()
	permsJSON, _ := json.Marshal(perms)
	session := model.RemoteAssistSession{
		ID:          idgen.GenerateID(idgen.PrefixRemoteAssist),
		RequesterID: requesterID,
		TargetID:    targetID,
		ChannelID:   channelID,
		Status:      StatusPending,
		Permissions: string(permsJSON),
		ExpiresAt:   now.Add(RequestTimeout),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.db.Create(&session).Error; err != nil {
		return Session{}, errors.ErrInternal
	}

	// Schedule an auto-reject timer. When it fires, the session is moved to
	// the "timeout" status and the requester is notified via WebSocket.
	s.scheduleTimeout(session.ID)

	s.LogControl(session.ID, requesterID, "remote_assist_request", map[string]interface{}{
		"targetId":  targetID,
		"channelId": channelID,
	})

	return toSessionResponse(&session), nil
}

// Authorize marks a pending request as authorized. Only the target user may
// authorize a request. The timeout timer is cancelled on success.
func (s *Service) Authorize(sessionID, targetID string) (Session, error) {
	session, err := s.getSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	if session.TargetID != targetID {
		return Session{}, errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "only the target user can authorize the request")
	}
	if session.Status != StatusPending {
		return Session{}, errors.New(errors.REMOTE_ASSIST_INVALID_STATE, fmt.Sprintf("session is not pending (status=%s)", session.Status))
	}
	if time.Now().UTC().After(session.ExpiresAt) {
		s.cancelTimeout(session.ID)
		_ = s.markStatus(session.ID, StatusTimeout)
		return Session{}, errors.New(errors.REMOTE_ASSIST_EXPIRED, "request has expired")
	}

	if err := s.markStatus(session.ID, StatusAuthorized); err != nil {
		return Session{}, err
	}
	s.cancelTimeout(session.ID)
	// M10: the 30-minute session cap is now enforced server-side. The
	// duration timer replaces the cancelled pending timer (same map slot) and
	// fires handleSessionExpiry, which force-ends the session and notifies
	// both parties. markStatus refreshed UpdatedAt to "now", which is exactly
	// the instant the timer measures from — matching the lazy check in
	// ValidateControlEvent / GetSession / ListActiveSessions.
	s.scheduleDurationTimeout(session.ID)

	s.LogControl(session.ID, targetID, "remote_assist_authorize", nil)

	session.Status = StatusAuthorized
	return toSessionResponse(session), nil
}

// Reject marks a pending request as rejected. Only the target user may
// reject a request. The timeout timer is cancelled on success.
func (s *Service) Reject(sessionID, targetID string) (Session, error) {
	session, err := s.getSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	if session.TargetID != targetID {
		return Session{}, errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "only the target user can reject the request")
	}
	if session.Status != StatusPending {
		return Session{}, errors.New(errors.REMOTE_ASSIST_INVALID_STATE, fmt.Sprintf("session is not pending (status=%s)", session.Status))
	}

	if err := s.markStatus(session.ID, StatusRejected); err != nil {
		return Session{}, err
	}
	s.cancelTimeout(session.ID)

	s.LogControl(session.ID, targetID, "remote_assist_reject", nil)

	session.Status = StatusRejected
	return toSessionResponse(session), nil
}

// End terminates an active (authorized) session. Either party may end it.
// Pending sessions should be rejected rather than ended.
func (s *Service) End(sessionID, userID string) (Session, error) {
	session, err := s.getSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	if session.RequesterID != userID && session.TargetID != userID {
		return Session{}, errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "only the requester or target can end the session")
	}
	if session.Status != StatusAuthorized && session.Status != StatusPending {
		return Session{}, errors.New(errors.REMOTE_ASSIST_INVALID_STATE, fmt.Sprintf("session is not active (status=%s)", session.Status))
	}

	if err := s.markStatus(session.ID, StatusEnded); err != nil {
		return Session{}, err
	}
	s.cancelTimeout(session.ID)

	s.LogControl(session.ID, userID, "remote_assist_end", nil)

	session.Status = StatusEnded
	return toSessionResponse(session), nil
}

// GetSession returns the API representation of a session. An authorized
// session that has exceeded MaxSessionDuration is force-ended first (M10
// lazy enforcement; DES-2026-0912-07 §5.2) so callers never observe a
// stale "authorized" session that is over the cap.
func (s *Service) GetSession(sessionID string) (Session, error) {
	session, err := s.getSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	s.expireIfOverLimit(session)
	return toSessionResponse(session), nil
}

// ListActiveSessions returns all sessions that are still pending or
// authorized. Used by the GET /remote-assist/sessions endpoint. Authorized
// sessions past MaxSessionDuration are force-ended and excluded, so a client
// refreshing (or restoring after a server restart, where the in-memory
// duration timer was lost) can never resume an over-limit session (M10).
func (s *Service) ListActiveSessions(userID string) ([]Session, error) {
	var sessions []model.RemoteAssistSession
	if err := s.db.Where(
		"(requester_id = ? OR target_id = ?) AND status IN ?",
		userID, userID, []string{StatusPending, StatusAuthorized},
	).Order("updated_at DESC").Find(&sessions).Error; err != nil {
		return nil, errors.ErrInternal
	}
	result := make([]Session, 0, len(sessions))
	for i := range sessions {
		// Lazy duration enforcement: over-limit authorized sessions are
		// terminated (and both parties notified) and dropped from the list.
		if s.expireIfOverLimit(&sessions[i]) {
			continue
		}
		result = append(result, toSessionResponse(&sessions[i]))
	}
	return result, nil
}

// ValidateControlEvent authorizes a single remote control event before the
// realtime hub forwards it to the target (H14). All four conditions must hold:
//
//  1. the session exists
//  2. the session is still authorized — the only state in which the target has
//     agreed to be controlled (pending / rejected / ended / timeout are not)
//  3. requesterID matches the session's requester
//  4. targetID matches the session's target
//
// Additionally (M10, DES-2026-0912-07 §5.2): an authorized session that has
// been running longer than MaxSessionDuration (measured from UpdatedAt, i.e.
// the authorization instant) is force-ended on the spot and the event is
// rejected with REMOTE_ASSIST_EXPIRED. This is the lazy half of the duration
// cap — it backstops the in-memory timer, which does not survive a server
// restart, so a modified client cannot keep injecting input into an
// over-limit session.
//
// A non-nil error means the event must be dropped. The hub deliberately does
// not parse the mouse/keyboard payload, so this is the only authorization
// checkpoint between an authenticated WebSocket client and the target's local
// input injection (nut.js). Callers must treat any error as "drop and stay
// silent" — replying with an error would let an attacker probe session IDs.
func (s *Service) ValidateControlEvent(sessionID, requesterID, targetID string) error {
	if sessionID == "" || requesterID == "" || targetID == "" {
		return errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "sessionId, requester and target are required")
	}
	session, err := s.getSession(sessionID)
	if err != nil {
		return err
	}
	if session.Status != StatusAuthorized {
		return errors.New(errors.REMOTE_ASSIST_INVALID_STATE,
			fmt.Sprintf("session is not active (status=%s)", session.Status))
	}
	if s.expireIfOverLimit(session) {
		return errors.New(errors.REMOTE_ASSIST_EXPIRED, "session has reached the maximum duration")
	}
	if session.RequesterID != requesterID {
		return errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "only the session requester can send control events")
	}
	if session.TargetID != targetID {
		return errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "control event target does not match the session target")
	}
	return nil
}

// ValidateFromTarget authorizes a clipboard pull-data reply before the
// realtime hub relays it to the requester (A6-S1, DES-20261001-01 §7.2).
// The sender of a "remote_assist_clipboard_data" message must be the
// session's target — the only participant that legitimately reads its own
// clipboard on behalf of a pull. Conditions mirror ValidateControlEvent:
//
//  1. the session exists
//  2. the session is still authorized (pending / rejected / ended / timeout
//     are all chat- and clipboard-dead)
//  3. the M10 lazy duration check: an authorized session past
//     MaxSessionDuration is force-ended on the spot and the event rejected
//  4. senderID matches the session's target (the requester-side direction is
//     gated by ValidateControlEvent, reused via the same hub hook)
//
// On success it returns the session requester's ID so the hub can route the
// data reply without a second database lookup. A non-nil error means the
// event must be dropped silently (H14: never reply with the reason, so a
// client cannot probe session IDs).
func (s *Service) ValidateFromTarget(sessionID, senderID string) (string, error) {
	if sessionID == "" || senderID == "" {
		return "", errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "sessionId and sender are required")
	}
	session, err := s.getSession(sessionID)
	if err != nil {
		return "", err
	}
	if session.Status != StatusAuthorized {
		return "", errors.New(errors.REMOTE_ASSIST_INVALID_STATE,
			fmt.Sprintf("session is not active (status=%s)", session.Status))
	}
	if s.expireIfOverLimit(session) {
		return "", errors.New(errors.REMOTE_ASSIST_EXPIRED, "session has reached the maximum duration")
	}
	if session.TargetID != senderID {
		return "", errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "only the session target may answer clipboard pulls")
	}
	return session.RequesterID, nil
}

// ValidateChatParticipant authorizes an in-session chat message before the
// realtime hub relays it to both parties (A7-S1, DES-20261001-01 §8.1). The
// sender must be the session's requester or its target — no third party, even
// one that guessed the sessionID. Same state gate (authorized only) and M10
// lazy duration check as ValidateControlEvent / ValidateFromTarget: a session
// that ended, was rejected, timed out, or ran past MaxSessionDuration is
// chat-dead too. On success it returns the other party's ID (the peer) so the
// hub can deliver without a second lookup. A non-nil error means the message
// must be dropped silently (H14).
func (s *Service) ValidateChatParticipant(sessionID, senderID string) (string, error) {
	if sessionID == "" || senderID == "" {
		return "", errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "sessionId and sender are required")
	}
	session, err := s.getSession(sessionID)
	if err != nil {
		return "", err
	}
	if session.Status != StatusAuthorized {
		return "", errors.New(errors.REMOTE_ASSIST_INVALID_STATE,
			fmt.Sprintf("session is not active (status=%s)", session.Status))
	}
	if s.expireIfOverLimit(session) {
		return "", errors.New(errors.REMOTE_ASSIST_EXPIRED, "session has reached the maximum duration")
	}
	switch senderID {
	case session.RequesterID:
		return session.TargetID, nil
	case session.TargetID:
		return session.RequesterID, nil
	default:
		return "", errors.New(errors.REMOTE_ASSIST_PERMISSION_DENIED, "only the session requester or target may chat")
	}
}

// IsUserInSpace checks if a user is a member of a space.
func (s *Service) IsUserInSpace(userID, spaceID string) (bool, error) {
	var count int64
	if err := s.db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id = ?", spaceID, userID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// findCommonSpace returns a space in which both users hold a membership
// (NJ-16: used when a request carries no channel context). Deterministic on
// the primary deployment shape (single space); with multiple common spaces any
// one of them is a valid scope. Returns "" when the pair shares no space.
func (s *Service) findCommonSpace(userA, userB string) (string, error) {
	var spaceIDs []string
	if err := s.db.Model(&model.Membership{}).
		Where("user_id IN ?", []string{userA, userB}).
		Distinct().
		Pluck("space_id", &spaceIDs).Error; err != nil {
		return "", err
	}
	for _, spaceID := range spaceIDs {
		inA, err := s.IsUserInSpace(userA, spaceID)
		if err != nil {
			return "", err
		}
		if !inA {
			continue
		}
		inB, err := s.IsUserInSpace(userB, spaceID)
		if err != nil {
			return "", err
		}
		if inB {
			return spaceID, nil
		}
	}
	return "", nil
}

// LogControl records a security audit entry for a remote assist event.
// Reuses admin.Service.LogSecurityEvent so the audit trail is unified with
// other security-sensitive operations (design doc T49 §"审计日志").
func (s *Service) LogControl(sessionID, userID, eventType string, details map[string]interface{}) {
	if s.auditSvc == nil {
		return
	}
	if details == nil {
		details = make(map[string]interface{})
	}
	details["sessionId"] = sessionID
	details["eventType"] = eventType
	// IP/UserAgent are not available at the service layer; the handler
	// enriches details when it has request context. Here we record the
	// bare event with userID as the actor.
	if err := s.auditSvc.LogSecurityEvent(userID, eventType, "remote_assist_session", sessionID, "", "", details); err != nil {
		// Audit logging failures must not break the control flow; the
		// caller has already committed the state transition.
		return
	}
}

// getSession loads a session by ID and returns ErrNotFound if missing.
func (s *Service) getSession(sessionID string) (*model.RemoteAssistSession, error) {
	var session model.RemoteAssistSession
	if err := s.db.First(&session, "id = ?", sessionID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.REMOTE_ASSIST_NOT_FOUND, "remote assist session not found")
		}
		return nil, errors.ErrInternal
	}
	return &session, nil
}

// markStatus updates the status column and UpdatedAt timestamp.
func (s *Service) markStatus(sessionID, status string) error {
	result := s.db.Model(&model.RemoteAssistSession{}).
		Where("id = ?", sessionID).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return errors.ErrInternal
	}
	if result.RowsAffected == 0 {
		return errors.New(errors.REMOTE_ASSIST_NOT_FOUND, "remote assist session not found")
	}
	return nil
}

// scheduleTimeout starts a goroutine that auto-rejects the request after
// RequestTimeout. The timer is tracked in s.timeoutTimers so it can be
// cancelled by cancelTimeout when the target responds in time.
func (s *Service) scheduleTimeout(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stop any previous timer for the same session (defensive).
	if t, ok := s.timeoutTimers[sessionID]; ok {
		t.Stop()
	}
	timer := time.AfterFunc(RequestTimeout, func() {
		s.handleTimeout(sessionID)
	})
	s.timeoutTimers[sessionID] = timer
}

// scheduleDurationTimeout starts the M10 duration-limit timer for an
// authorized session. It reuses the same per-session timer slot (and the same
// time.AfterFunc pattern) as the pending auto-reject timer — no new background
// worker system. cancelTimeout (called from End / Reject / handleTimeout)
// stops it like any other phase's timer.
func (s *Service) scheduleDurationTimeout(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.timeoutTimers[sessionID]; ok {
		t.Stop()
	}
	timer := time.AfterFunc(MaxSessionDuration, func() {
		s.handleSessionExpiry(sessionID)
	})
	s.timeoutTimers[sessionID] = timer
}

// cancelTimeout stops the timeout timer for a session without firing it.
func (s *Service) cancelTimeout(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.timeoutTimers[sessionID]; ok {
		t.Stop()
		delete(s.timeoutTimers, sessionID)
	}
}

// handleTimeout fires when the request deadline elapses. It atomically
// transitions the session from pending → timeout and notifies the requester.
func (s *Service) handleTimeout(sessionID string) {
	s.mu.Lock()
	delete(s.timeoutTimers, sessionID)
	s.mu.Unlock()

	session, err := s.getSession(sessionID)
	if err != nil {
		return
	}
	if session.Status != StatusPending {
		return
	}
	if err := s.markStatus(sessionID, StatusTimeout); err != nil {
		return
	}

	s.LogControl(sessionID, session.RequesterID, "remote_assist_timeout", nil)

	// Notify the requester that the request timed out.
	s.notifySessionEnded(session, reasonTimeout, StatusTimeout, session.RequesterID)
}

// expireIfOverLimit is the lazy half of the M10 duration cap (DES
// 2026-0912-07 §5.2): if the session is authorized and has been running
// longer than MaxSessionDuration — measured from UpdatedAt, which markStatus
// refreshed to the authorization instant — it is force-ended right now
// (status, audit log, WS notification to both parties) and the method returns
// true. Callers: ValidateControlEvent (control-event gate), GetSession and
// ListActiveSessions (visibility paths). It backstops the in-memory duration
// timer, which does not survive a server restart. Idempotent: anything not in
// the authorized state is left untouched and reports false.
func (s *Service) expireIfOverLimit(session *model.RemoteAssistSession) bool {
	if session.Status != StatusAuthorized {
		return false
	}
	if !time.Now().UTC().After(session.UpdatedAt.Add(MaxSessionDuration)) {
		return false
	}
	s.handleSessionExpiry(session.ID)
	// Reflect the forced transition on the in-memory copy so the caller's
	// subsequent use of the struct sees the terminal state.
	session.Status = StatusEnded
	return true
}

// handleSessionExpiry force-ends an authorized session that reached
// MaxSessionDuration (M10). Reuses the existing end path: same terminal
// status (ended), same audit channel (LogControl), same WS event shape as a
// user-initiated end — with reason "duration_limit" so clients can render a
// specific message. Both parties are notified (a user-initiated end only
// notifies the other side, because the initiator already knows locally).
func (s *Service) handleSessionExpiry(sessionID string) {
	s.mu.Lock()
	delete(s.timeoutTimers, sessionID)
	s.mu.Unlock()

	session, err := s.getSession(sessionID)
	if err != nil {
		return
	}
	// Idempotence / race guard: only an authorized session may be expired.
	// If End() won the race the session is already ended — nothing to do.
	if session.Status != StatusAuthorized {
		return
	}
	if err := s.markStatus(sessionID, StatusEnded); err != nil {
		return
	}

	s.LogControl(sessionID, session.RequesterID, "remote_assist_expired", map[string]interface{}{
		"targetId":        session.TargetID,
		"reason":          reasonDurationLimit,
		"maxDurationSecs": int(MaxSessionDuration / time.Second),
	})

	session.Status = StatusEnded
	s.notifySessionEnded(session, reasonDurationLimit, StatusEnded, session.RequesterID, session.TargetID)
}

// notifySessionEnded broadcasts the shared "remote_assist_end" WS event to the
// given users. The event type is reused (instead of a new
// "remote_assist_expired" type) because the client forwards a fixed whitelist
// of remote_assist_* types from its WS layer to the panel, and the pending
// timeout already follows the same reuse pattern.
func (s *Service) notifySessionEnded(session *model.RemoteAssistSession, reason, status string, userIDs ...string) {
	if s.hub == nil {
		return
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"type": "remote_assist_end",
		"payload": map[string]interface{}{
			"sessionId": session.ID,
			"endedBy":   "server",
			"reason":    reason,
			"status":    status,
		},
	})
	s.hub.BroadcastToUsers(userIDs, payload)
}
