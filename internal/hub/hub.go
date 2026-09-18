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

	"github.com/coder/websocket"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/auth"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	"github.com/Itz-snj/MSclaudeConnector/internal/store"
)

// Config holds hub-scoped runtime configuration.
type Config struct {
	SessionID string
}

// Hub is the WSS sync server: auth, snapshot+tail fan-out, command intake,
// and first-write-wins permission resolution.
type Hub struct {
	cfg     Config
	log     logger.Logger
	store   *store.Store
	auth    *auth.Auth
	adapter agent.Adapter

	mu          sync.RWMutex
	clients     map[*client]struct{}
	pending     map[string]protocol.PermissionView // requestId -> view
	idempotency map[string]struct{}                // deviceID:idempotencyKey
}

type client struct {
	deviceID string
	name     string
	conn     *websocket.Conn
	send     chan []byte
	lastSeq  int64
}

func New(cfg Config, log logger.Logger, st *store.Store, authz *auth.Auth, adapter agent.Adapter) *Hub {
	return &Hub{
		cfg:         cfg,
		log:         log,
		store:       st,
		auth:        authz,
		adapter:     adapter,
		clients:     map[*client]struct{}{},
		pending:     map[string]protocol.PermissionView{},
		idempotency: map[string]struct{}{},
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
		_ = c.conn.Close(websocket.StatusGoingAway, "daemon shutting down")
	}
}

// ServeHTTP upgrades HTTP requests to WebSocket and handles them.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	deviceID := ""
	authenticated := false
	if credential != "" {
		if d, ok := h.auth.ValidateCredential(credential); ok {
			deviceID = d
			authenticated = true
		}
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		h.log.Error("websocket accept failed", "error", err)
		return
	}

	ctx := r.Context()
	if err := h.handleConnection(ctx, conn, deviceID, authenticated); err != nil {
		h.log.Warn("connection closed", "device", deviceID, "error", err)
	}
}

func (h *Hub) handleConnection(ctx context.Context, conn *websocket.Conn, deviceID string, authenticated bool) error {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageText {
		return errors.New("expected text message")
	}
	var hello protocol.Envelope
	if err := json.Unmarshal(data, &hello); err != nil {
		return err
	}
	if hello.Type != protocol.MsgHello || hello.Hello == nil {
		return errors.New("expected hello envelope")
	}

	// Pairing flow: no device credential yet.
	if !authenticated {
		if hello.Hello.PairingToken == "" {
			_ = h.sendError(ctx, conn, "", "auth_required", "authorization header or pairing token required")
			return errors.New("auth required")
		}
		name, ok := h.auth.ConsumePairingToken(hello.Hello.PairingToken)
		if !ok {
			_ = h.sendError(ctx, conn, "", "pairing_denied", "pairing token invalid, expired, or not approved")
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
		return h.writeEnvelope(ctx, conn, resp)
	}

	// Authenticated client.
	if hello.Hello.DeviceID == "" {
		hello.Hello.DeviceID = deviceID
	}
	if hello.Hello.DeviceID != deviceID {
		_ = h.sendError(ctx, conn, "", "device_mismatch", "device id does not match credential")
		return errors.New("device mismatch")
	}

	snapshot, err := h.store.GetSnapshot(h.cfg.SessionID)
	if err != nil {
		_ = h.sendError(ctx, conn, "", "server_error", err.Error())
		return err
	}
	events, err := h.store.GetEventsSince(h.cfg.SessionID, hello.Hello.LastSeq, 0)
	if err != nil {
		_ = h.sendError(ctx, conn, "", "server_error", err.Error())
		return err
	}

	c := &client{
		deviceID: deviceID,
		name:     deviceID,
		conn:     conn,
		send:     make(chan []byte, 256),
		lastSeq:  hello.Hello.LastSeq,
	}
	go c.writeLoop(ctx, h.log)

	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		// Do not close c.send here; the writeLoop owns it and broadcast uses
		// non-blocking sends. Closing it would race with broadcast.
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	h.mu.Lock()
	h.clients[c] = struct{}{}
	_ = h.writeToClient(c, protocol.Envelope{
		Version:  protocol.Version,
		Type:     protocol.MsgSnapshot,
		Snapshot: snapshotToPayload(snapshot),
	})
	for i := range events {
		ev := events[i]
		_ = h.writeToClient(c, protocol.Envelope{
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
		_ = h.sendError(ctx, c.conn, "", "bad_request", "command id required")
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
		_ = h.ack(ctx, c.conn, cmd.ID, 0)
		return
	}

	switch cmd.Kind {
	case protocol.CmdSendPrompt:
		var body protocol.SendPromptBody
		if err := json.Unmarshal(cmd.Payload, &body); err != nil {
			_ = h.sendError(ctx, c.conn, cmd.ID, "bad_request", "invalid send_prompt payload")
			return
		}
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventUserPrompt, protocol.UserPromptBody{
			Text:     body.Text,
			ByDevice: c.deviceID,
		})
		if err != nil {
			_ = h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		_ = h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdSendPrompt, Payload: body}); err != nil {
			_ = h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	case protocol.CmdAnswerPermission:
		var body protocol.AnswerPermissionBody
		if err := json.Unmarshal(cmd.Payload, &body); err != nil {
			_ = h.clientError(c, cmd.ID, "bad_request", "invalid answer_permission payload")
			return
		}
		h.mu.Lock()
		_, pending := h.pending[body.RequestID]
		if pending {
			delete(h.pending, body.RequestID)
		}
		h.mu.Unlock()
		if !pending {
			_ = h.clientError(c, cmd.ID, "already_resolved", "permission request already resolved or unknown")
			return
		}
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventPermissionResolved, protocol.PermissionResolvedBody{
			RequestID: body.RequestID,
			Allow:     body.Allow,
			ByDevice:  c.deviceID,
		})
		if err != nil {
			_ = h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		_ = h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdAnswerPermission, Payload: body}); err != nil {
			_ = h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	case protocol.CmdInterrupt:
		ev, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventStatusChange, protocol.StatusChangeBody{Status: "idle"})
		if err != nil {
			_ = h.clientError(c, cmd.ID, "server_error", err.Error())
			return
		}
		_ = h.clientAck(c, cmd.ID, ev.Seq)
		h.broadcast(ev)
		if err := h.adapter.Command(agent.Command{Kind: protocol.CmdInterrupt, Payload: struct{}{}}); err != nil {
			_ = h.clientError(c, cmd.ID, "agent_error", err.Error())
			return
		}

	default:
		_ = h.clientError(c, cmd.ID, "unsupported_command", string(cmd.Kind))
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
			h.broadcast(stored)
		}
	}
}

func permissionBodyFromEvent(ev *protocol.EventPayload) protocol.PermissionRequestBody {
	var body protocol.PermissionRequestBody
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
		select {
		case c.send <- data:
		default:
			h.log.Warn("client send buffer full; dropping event", "device", c.deviceID)
		}
	}
}

func (h *Hub) writeToClient(c *client, env protocol.Envelope) bool {
	data, err := json.Marshal(env)
	if err != nil {
		h.log.Error("failed to marshal client message", "error", err)
		return false
	}
	select {
	case c.send <- data:
		return true
	default:
		h.log.Warn("client send buffer full during catch-up", "device", c.deviceID)
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

func (h *Hub) ack(ctx context.Context, conn *websocket.Conn, commandID string, seq int64) error {
	return h.writeEnvelope(ctx, conn, protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgAck,
		Ack:     &protocol.AckPayload{CommandID: commandID, Seq: seq},
	})
}

func (h *Hub) clientAck(c *client, commandID string, seq int64) bool {
	return h.writeToClient(c, protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgAck,
		Ack:     &protocol.AckPayload{CommandID: commandID, Seq: seq},
	})
}

func (h *Hub) sendError(ctx context.Context, conn *websocket.Conn, commandID, code, message string) error {
	return h.writeEnvelope(ctx, conn, protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgError,
		Error: &protocol.ErrorPayload{
			CommandID: commandID,
			Code:      code,
			Message:   message,
		},
	})
}

func (h *Hub) clientError(c *client, commandID, code, message string) bool {
	return h.writeToClient(c, protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgError,
		Error: &protocol.ErrorPayload{
			CommandID: commandID,
			Code:      code,
			Message:   message,
		},
	})
}

func (c *client) writeLoop(ctx context.Context, log logger.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
				log.Warn("client write failed", "device", c.deviceID, "error", err)
				return
			}
		}
	}
}

func snapshotToPayload(s *store.Snapshot) *protocol.SnapshotPayload {
	return &protocol.SnapshotPayload{
		SessionID:          s.SessionID,
		Status:             s.Status,
		LastSeq:            s.LastSeq,
		PendingPermissions: s.PendingPermissions,
	}
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
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}
