package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/auth"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	"github.com/Itz-snj/MSclaudeConnector/internal/store"
	"github.com/coder/websocket"
)

// clientQueueHighWater is the per-client outbound queue depth. Overflow closes
// the connection with StatusTryAgainLater so the client reconnects from its
// last contiguous seq instead of silently losing events. It is a var so tests
// can exercise overflow with a small queue.
var clientQueueHighWater = 4096

// AuthService is the subset of *auth.Auth the hub depends on. It is an
// interface so tests can substitute a counting fake.
type AuthService interface {
	ValidateCredential(credential string) (deviceID string, ok bool)
	PeekPairingToken(token string) (*store.PendingToken, bool)
	ConsumePairingToken(token string) (name string, ok bool)
	ApprovePairingToken(token, name string) error
	IssueCredential(deviceID, name string) (string, error)
}

// ApprovalRequest describes an inbound pairing attempt awaiting human
// confirmation on the host console.
type ApprovalRequest struct {
	Token      string
	RemoteAddr string
	UserAgent  string
}

// Approver decides whether an inbound pairing attempt is allowed. The host
// installs a terminal implementation; a nil Approver means only out-of-band
// approved tokens are accepted.
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (name string, ok bool)
}

// Config holds hub-scoped runtime configuration.
type Config struct {
	SessionID string
	Approver  Approver
}

// Hub is the WSS sync server: auth, snapshot+tail fan-out, command intake,
// and first-write-wins permission resolution.
type Hub struct {
	cfg     Config
	log     logger.Logger
	store   *store.Store
	auth    AuthService
	adapter agent.Adapter

	mu               sync.RWMutex
	clients          map[*client]struct{}
	pending          map[string]protocol.PermissionView // requestId -> view
	pendingQuestions map[string]protocol.QuestionView   // questionId -> view
	idempotency      map[string]struct{}                // deviceID:idempotencyKey
}

type client struct {
	deviceID string
	name     string
	conn     *websocket.Conn
	send     chan []byte
	overflow chan struct{}
	lastSeq  int64
	log      logger.Logger

	closeOnce sync.Once
}

func New(cfg Config, log logger.Logger, st *store.Store, authz *auth.Auth, adapter agent.Adapter) *Hub {
	return &Hub{
		cfg:              cfg,
		log:              log,
		store:            st,
		auth:             authz,
		adapter:          adapter,
		clients:          map[*client]struct{}{},
		pending:          map[string]protocol.PermissionView{},
		pendingQuestions: map[string]protocol.QuestionView{},
		idempotency:      map[string]struct{}{},
	}
}

// Run starts the adapter event ingestion loop and blocks until ctx is done.
func (h *Hub) Run(ctx context.Context) {
	go h.ingestAdapterEvents(ctx)
	<-ctx.Done()

	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.fail(websocket.StatusGoingAway, "daemon shutting down")
	}
}

// ServeHTTP upgrades HTTP requests to WebSocket and handles them.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	headerCred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		h.log.Error("websocket accept failed", "error", err)
		return
	}

	// Guarantee a close frame on every exit path, including pairing (which
	// serves one frame and returns) and early auth failures.
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := h.handleConnection(r.Context(), conn, headerCred, r.RemoteAddr, r.UserAgent()); err != nil {
		h.log.Warn("connection closed", "error", err)
	}
}

// handleConnection reads the hello frame, resolves authentication (header
// beats hello; credential-in-hello exists because browsers cannot set an
// Authorization header on a WebSocket), and dispatches to pairing or the
// authenticated sync loop.
func (h *Hub) handleConnection(ctx context.Context, conn *websocket.Conn, headerCred, remoteAddr, userAgent string) error {
	hello, err := readHello(ctx, conn)
	if err != nil {
		return err
	}

	cred := headerCred
	if cred == "" {
		cred = hello.Credential
	}

	deviceID, authenticated := "", false
	if cred != "" {
		// ValidateCredential is called at most once per connection; it bumps
		// last_seen as a side effect.
		if d, ok := h.auth.ValidateCredential(cred); ok {
			deviceID, authenticated = d, true
		}
	}
	if !authenticated {
		return h.handlePairing(ctx, conn, hello, remoteAddr, userAgent)
	}
	return h.serveAuthenticated(ctx, conn, deviceID, hello)
}

func readHello(ctx context.Context, conn *websocket.Conn) (*protocol.HelloPayload, error) {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, errors.New("expected text message")
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Type != protocol.MsgHello || env.Hello == nil {
		return nil, errors.New("expected hello envelope")
	}
	return env.Hello, nil
}

// handlePairing drives the connect-time pairing exchange. A token approved
// out-of-band is consumed directly; otherwise the host Approver is consulted
// and the token is approved and consumed on success.
func (h *Hub) handlePairing(ctx context.Context, conn *websocket.Conn, hello *protocol.HelloPayload, remoteAddr, userAgent string) error {
	token := hello.PairingToken
	if token == "" {
		_ = h.sendError(ctx, conn, "", "auth_required", "credential or pairing token required")
		return errors.New("auth required")
	}
	if _, ok := h.auth.PeekPairingToken(token); !ok {
		_ = h.sendError(ctx, conn, "", "pairing_denied", "pairing token invalid, expired, or already used")
		return errors.New("pairing denied")
	}

	name := ""
	if n, ok := h.auth.ConsumePairingToken(token); ok {
		name = n
	} else if h.cfg.Approver != nil {
		n, ok := h.cfg.Approver.RequestApproval(ctx, ApprovalRequest{
			Token:      token,
			RemoteAddr: remoteAddr,
			UserAgent:  userAgent,
		})
		if !ok {
			_ = h.sendError(ctx, conn, "", "pairing_denied", "pairing not approved on host")
			return errors.New("pairing denied")
		}
		name = strings.TrimSpace(n)
		if name == "" {
			name = nameFromUserAgent(userAgent)
		}
		if err := h.auth.ApprovePairingToken(token, name); err != nil {
			_ = h.sendError(ctx, conn, "", "pairing_denied", "pairing token invalid or expired")
			return err
		}
		if n, ok := h.auth.ConsumePairingToken(token); ok {
			name = n
		}
	} else {
		_ = h.sendError(ctx, conn, "", "pairing_denied", "pairing token not approved")
		return errors.New("pairing denied")
	}

	newDeviceID, err := generateID()
	if err != nil {
		return err
	}
	credential, err := h.auth.IssueCredential(newDeviceID, name)
	if err != nil {
		_ = h.sendError(ctx, conn, "", "server_error", err.Error())
		return err
	}
	resp := protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgPaired,
		Paired: &protocol.PairedPayload{
			DeviceID:   newDeviceID,
			Name:       name,
			Credential: credential,
		},
	}
	h.log.Info("device paired", "device", newDeviceID, "name", name)
	return h.writeEnvelope(ctx, conn, resp)
}

func (h *Hub) serveAuthenticated(ctx context.Context, conn *websocket.Conn, deviceID string, hello *protocol.HelloPayload) error {
	if hello.DeviceID == "" {
		hello.DeviceID = deviceID
	}
	if hello.DeviceID != deviceID {
		_ = h.sendError(ctx, conn, "", "device_mismatch", "device id does not match credential")
		return errors.New("device mismatch")
	}

	name := deviceID
	if d, err := h.store.GetDevice(deviceID); err == nil && d != nil && d.Name != "" {
		name = d.Name
	}

	snapshot, err := h.store.GetSnapshot(h.cfg.SessionID)
	if err != nil {
		_ = h.sendError(ctx, conn, "", "server_error", err.Error())
		return err
	}
	events, err := h.store.GetEventsSince(h.cfg.SessionID, hello.LastSeq, 0)
	if err != nil {
		_ = h.sendError(ctx, conn, "", "server_error", err.Error())
		return err
	}

	c := &client{
		deviceID: deviceID,
		name:     name,
		conn:     conn,
		send:     make(chan []byte, clientQueueHighWater),
		overflow: make(chan struct{}, 1),
		lastSeq:  hello.LastSeq,
		log:      h.log,
	}
	go c.writeLoop(ctx)

	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		c.fail(websocket.StatusNormalClosure, "")
	}()

	h.mu.Lock()
	h.clients[c] = struct{}{}
	_ = h.enqueue(c, protocol.Envelope{
		Version:  protocol.Version,
		Type:     protocol.MsgSnapshot,
		Snapshot: h.snapshotToPayload(snapshot),
	})
	for i := range events {
		ev := events[i]
		_ = h.enqueue(c, protocol.Envelope{
			Version: protocol.Version,
			Type:    protocol.MsgEvent,
			Event:   &ev,
		})
	}
	h.mu.Unlock()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Type != protocol.MsgCommand || env.Command == nil {
			continue
		}
		h.handleCommand(ctx, c, env.Command)
	}
}

func (h *Hub) handleCommand(ctx context.Context, c *client, cmd *protocol.CommandPayload) {
	if cmd.ID == "" {
		_ = h.enqueue(c, h.errorEnvelope("", "bad_request", "command id required"))
		return
	}
	if cmd.IdempotencyKey == "" {
		cmd.IdempotencyKey = cmd.ID
	}

	// First-write-wins for idempotency across reconnects.
	idempKey := c.deviceID + ":" + cmd.IdempotencyKey
	h.mu.Lock()
	_, seen := h.idempotency[idempKey]
	if !seen {
		h.idempotency[idempKey] = struct{}{}
	}
	h.mu.Unlock()
	if seen {
		_ = h.enqueue(c, h.ackEnvelope(cmd.ID, 0))
		return
	}

	switch cmd.Kind {
	case protocol.CmdSendPrompt:
		var body protocol.SendPromptBody
		if err := json.Unmarshal(cmd.Payload, &body); err != nil {
			h.clientError(c, cmd.ID, "bad_request", "invalid send_prompt payload")
			return
		}
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventUserPrompt, protocol.UserPromptBody{
			Text:     body.Text,
			ByDevice: c.deviceID,
		})
		if err != nil {
			h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdSendPrompt, Payload: body}); err != nil {
			h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	case protocol.CmdAnswerPermission:
		var body protocol.AnswerPermissionBody
		if err := json.Unmarshal(cmd.Payload, &body); err != nil {
			h.clientError(c, cmd.ID, "bad_request", "invalid answer_permission payload")
			return
		}
		h.mu.Lock()
		_, pending := h.pending[body.RequestID]
		if pending {
			delete(h.pending, body.RequestID)
		}
		h.mu.Unlock()
		if !pending {
			h.clientError(c, cmd.ID, "already_resolved", "permission request already resolved or unknown")
			return
		}
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventPermissionResolved, protocol.PermissionResolvedBody{
			RequestID: body.RequestID,
			Allow:     body.Allow,
			ByDevice:  c.deviceID,
		})
		if err != nil {
			h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdAnswerPermission, Payload: body}); err != nil {
			h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	case protocol.CmdAnswerQuestion:
		var body protocol.AnswerQuestionBody
		if err := json.Unmarshal(cmd.Payload, &body); err != nil {
			h.clientError(c, cmd.ID, "bad_request", "invalid answer_question payload")
			return
		}
		h.mu.Lock()
		_, pending := h.pendingQuestions[body.QuestionID]
		if pending {
			delete(h.pendingQuestions, body.QuestionID)
		}
		h.mu.Unlock()
		if !pending {
			h.clientError(c, cmd.ID, "already_resolved", "question already resolved or unknown")
			return
		}
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventQuestionResolved, protocol.QuestionResolvedBody{
			QuestionID: body.QuestionID,
			Text:       body.Text,
			ByDevice:   c.deviceID,
		})
		if err != nil {
			h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdAnswerQuestion, Payload: body}); err != nil {
			h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	case protocol.CmdSetMode:
		var body protocol.SetModeBody
		if err := json.Unmarshal(cmd.Payload, &body); err != nil || body.Mode == "" {
			h.clientError(c, cmd.ID, "bad_request", "invalid set_mode payload")
			return
		}
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventModeChanged, protocol.ModeChangedBody{
			Mode:     body.Mode,
			ByDevice: c.deviceID,
		})
		if err != nil {
			h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdSetMode, Payload: body}); err != nil {
			h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	case protocol.CmdInterrupt:
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventStatusChange, protocol.StatusChangeBody{Status: "idle"})
		if err != nil {
			h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdInterrupt, Payload: struct{}{}}); err != nil {
			h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	default:
		h.clientError(c, cmd.ID, "unsupported_command", string(cmd.Kind))
	}
}

func (h *Hub) ingestAdapterEvents(ctx context.Context) {
	ch := h.adapter.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			stored, err := h.store.AppendEvent(h.cfg.SessionID, ev.Kind, ev.Payload)
			if err != nil {
				h.log.Error("failed to append adapter event", "error", err)
				continue
			}
			if stored.Kind == protocol.EventPermissionRequest {
				body := permissionBodyFromEvent(stored)
				h.mu.Lock()
				h.pending[body.RequestID] = protocol.PermissionView{
					RequestID: body.RequestID,
					Kind:      body.Kind,
					Summary:   body.Summary,
				}
				h.mu.Unlock()
			}
			if stored.Kind == protocol.EventQuestion {
				body := questionBodyFromEvent(stored)
				h.mu.Lock()
				h.pendingQuestions[body.QuestionID] = protocol.QuestionView{
					QuestionID: body.QuestionID,
					Text:       body.Text,
				}
				h.mu.Unlock()
			}
			h.broadcast(stored)
		}
	}
}

func permissionBodyFromEvent(ev *protocol.EventPayload) protocol.PermissionRequestBody {
	var body protocol.PermissionRequestBody
	_ = json.Unmarshal(ev.Payload, &body)
	return body
}

func questionBodyFromEvent(ev *protocol.EventPayload) protocol.QuestionBody {
	var body protocol.QuestionBody
	_ = json.Unmarshal(ev.Payload, &body)
	return body
}

func (h *Hub) broadcast(ev *protocol.EventPayload) {
	data, err := json.Marshal(protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgEvent,
		Event:   ev,
	})
	if err != nil {
		h.log.Error("failed to marshal event", "error", err)
		return
	}

	h.mu.RLock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		h.enqueueRaw(c, data)
	}
}

// enqueue marshals and routes a message through a client's outbound queue.
func (h *Hub) enqueue(c *client, env protocol.Envelope) bool {
	data, err := json.Marshal(env)
	if err != nil {
		h.log.Error("failed to marshal client message", "error", err)
		return false
	}
	return h.enqueueRaw(c, data)
}

// enqueueRaw routes pre-marshalled bytes through a client's outbound queue. On
// overflow the client is flagged so its writeLoop closes the connection with
// StatusTryAgainLater, prompting a clean resync.
func (h *Hub) enqueueRaw(c *client, data []byte) bool {
	select {
	case c.send <- data:
		return true
	default:
		h.log.Warn("client send queue full; closing for resync", "device", c.deviceID)
		select {
		case c.overflow <- struct{}{}:
		default:
		}
		return false
	}
}

func (h *Hub) writeEnvelope(ctx context.Context, conn *websocket.Conn, env protocol.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func (h *Hub) ackEnvelope(commandID string, seq int64) protocol.Envelope {
	return protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgAck,
		Ack:     &protocol.AckPayload{CommandID: commandID, Seq: seq},
	}
}

func (h *Hub) errorEnvelope(commandID, code, message string) protocol.Envelope {
	return protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgError,
		Error: &protocol.ErrorPayload{
			CommandID: commandID,
			Code:      code,
			Message:   message,
		},
	}
}

func (h *Hub) clientAck(c *client, commandID string, seq int64) bool {
	return h.enqueue(c, h.ackEnvelope(commandID, seq))
}

func (h *Hub) sendError(ctx context.Context, conn *websocket.Conn, commandID, code, message string) error {
	return h.writeEnvelope(ctx, conn, h.errorEnvelope(commandID, code, message))
}

func (h *Hub) clientError(c *client, commandID, code, message string) bool {
	return h.enqueue(c, h.errorEnvelope(commandID, code, message))
}

func (c *client) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.overflow:
			c.fail(websocket.StatusTryAgainLater, "outbound queue overflow")
			return
		case data := <-c.send:
			if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
				c.log.Warn("client write failed", "device", c.deviceID, "error", err)
				return
			}
		}
	}
}

// fail closes the connection exactly once.
func (c *client) fail(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		_ = c.conn.Close(code, reason)
	})
}

func (h *Hub) snapshotToPayload(s *store.Snapshot) *protocol.SnapshotPayload {
	payload := &protocol.SnapshotPayload{
		SessionID:          s.SessionID,
		Status:             s.Status,
		Mode:               s.Mode,
		LastSeq:            s.LastSeq,
		PendingPermissions: s.PendingPermissions,
		PendingQuestions:   s.PendingQuestions,
	}
	if devices, err := h.store.ListDevices(); err == nil {
		views := make([]protocol.DeviceView, 0, len(devices))
		for _, d := range devices {
			if d.Revoked {
				continue
			}
			views = append(views, protocol.DeviceView{DeviceID: d.DeviceID, Name: d.Name})
		}
		payload.Devices = views
	}
	return payload
}

func nameFromUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "device"
	}
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "android"):
		return "Android"
	case strings.Contains(lower, "iphone"), strings.Contains(lower, "ipad"):
		return "iOS"
	case strings.Contains(lower, "chrome"):
		return "Chrome"
	case strings.Contains(lower, "firefox"):
		return "Firefox"
	case strings.Contains(lower, "safari"):
		return "Safari"
	case strings.Contains(lower, "okhttp"), strings.Contains(lower, "react"):
		return "Mobile"
	}
	if len(ua) > 32 {
		return ua[:32]
	}
	return ua
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CloseClient is a test helper that closes a client connection and waits for cleanup.
func (h *Hub) CloseClient(c *client) {
	c.fail(websocket.StatusNormalClosure, "")
}
