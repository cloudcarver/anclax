package ws

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/gofiber/contrib/websocket"
	"go.uber.org/zap"
)

var wslog = logger.NewLogAgent("websocket")

var (
	ErrCloseReceived        = errors.New("close frame received")
	ErrBackpressure         = errors.New("backpressure encountered")
	ErrBiz                  = errors.New("business error")
	ErrBadRequest           = errors.New("bad request")
	ErrHandlerNotRegistered = errors.New("handler not registered")
)

const (
	defaultIdleTimeout  = 40 * time.Second
	defaultPingInterval = 30 * time.Second
	defaultWriteWait    = 10 * time.Second

	defaultWsSessionIDKey = "ws_session_id"
)

type BufMsg struct {
	mt  int
	msg []byte
}

type Session struct {
	id           string
	conn         *websocket.Conn
	writeBuf     chan<- BufMsg
	onClose      []func() error
	cancel       context.CancelCauseFunc
	close        func(err error)
	sessionIDKey string
	hub          *Hub
}

func NewSession(conn *websocket.Conn, writeBuf chan<- BufMsg, cancel context.CancelCauseFunc, sessionIDKey string, hub *Hub) *Session {
	id := uuid.New().String()
	conn.Locals(sessionIDKey, id)
	return &Session{
		conn:         conn,
		writeBuf:     writeBuf,
		onClose:      make([]func() error, 0),
		cancel:       cancel,
		id:           id,
		sessionIDKey: sessionIDKey,
		hub:          hub,
	}
}
func (s *Session) release() {
	for _, closer := range s.onClose {
		if err := closer(); err != nil {
			wslog.Error("failed to close resource", zap.Error(err), zap.String(s.sessionIDKey, s.ID()))
		}
	}
}

func (s *Session) Close(err error) {
	s.cancel(err)
}

func (s *Session) RegisterOnClose(closer func() error) {
	s.onClose = append(s.onClose, closer)
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Conn() *websocket.Conn {
	return s.conn
}

func (s *Session) Broadcast(topic string, data any) {
	s.hub.broadcastExcept(topic, data, s.id)
}

func (s *Session) WriteTextMessage(data any) error {
	msg, err := json.Marshal(data)
	if err != nil {
		return err
	}

	select {
	case s.writeBuf <- BufMsg{mt: websocket.TextMessage, msg: msg}:
		return nil
	default:
		s.cancel(ErrBackpressure)
		return ErrBackpressure
	}
}

func (s *Session) WriteBinaryMessage(data []byte) error {
	if data == nil {
		data = []byte{}
	}
	select {
	case s.writeBuf <- BufMsg{mt: websocket.BinaryMessage, msg: data}:
		return nil
	default:
		s.cancel(ErrBackpressure)
		return ErrBackpressure
	}
}

type Ctx struct {
	context.Context
	*Session
	ID *string
}

func NewCtx(ctx context.Context, s *Session) *Ctx {
	return &Ctx{
		Context: ctx,
		Session: s,
	}
}

func (c *Ctx) SendError(err error) error {
	return c.WriteTextMessage(map[string]any{
		"ID":    c.ID,
		"Error": err.Error(),
	})
}

func (c *Ctx) SetID(id string) {
	c.ID = &id
}

type OnSessionCreated func(s *Session) error
type MessageHandlerFunc func(ctx *Ctx, data []byte) error

type WebsocketController struct {
	ctx              context.Context
	handle           MessageHandlerFunc
	onSessionCreated OnSessionCreated
	hub              *Hub

	readLimit      int64
	idleTimeout    time.Duration
	pingInterval   time.Duration
	writeWait      time.Duration
	wsSessionIDKey string
}

func NewWebsocketController(globalCtx *globalctx.GlobalContext, libCfg *config.LibConfig) *WebsocketController {
	var readLimit int64 = 1024 * 1024 // 1MB
	if libCfg.Ws != nil && libCfg.Ws.ReadLimit > 0 {
		readLimit = libCfg.Ws.ReadLimit
	}
	var idleTimeout = defaultIdleTimeout
	if libCfg.Ws != nil && libCfg.Ws.IdleTimeoutSeconds > 0 {
		idleTimeout = time.Duration(libCfg.Ws.IdleTimeoutSeconds) * time.Second
	}
	var pingInterval = defaultPingInterval
	if libCfg.Ws != nil && libCfg.Ws.PingIntervalSeconds > 0 {
		pingInterval = time.Duration(libCfg.Ws.PingIntervalSeconds) * time.Second
	}
	var writeWait = defaultWriteWait
	if libCfg.Ws != nil && libCfg.Ws.WriteWaitSeconds > 0 {
		writeWait = time.Duration(libCfg.Ws.WriteWaitSeconds) * time.Second
	}
	var wsSessionIDKey = defaultWsSessionIDKey
	if libCfg.Ws != nil && libCfg.Ws.SessionIDKey != "" {
		wsSessionIDKey = libCfg.Ws.SessionIDKey
	}

	return &WebsocketController{
		ctx:              globalCtx.Context(),
		handle:           func(ctx *Ctx, data []byte) error { return ErrHandlerNotRegistered },
		onSessionCreated: func(s *Session) error { return nil },
		hub:              NewHub(),
		readLimit:        readLimit,
		idleTimeout:      idleTimeout,
		pingInterval:     pingInterval,
		writeWait:        writeWait,
		wsSessionIDKey:   wsSessionIDKey,
	}
}

func (w *WebsocketController) SetMessageHandler(f MessageHandlerFunc) {
	w.handle = f
}

func (w *WebsocketController) SetOnSessionCreated(f OnSessionCreated) {
	w.onSessionCreated = f
}

func (w *WebsocketController) Hub() *Hub {
	return w.hub
}

func (w *WebsocketController) HandleConn(c *websocket.Conn) {
	ctx, cancel := context.WithCancelCause(w.ctx)
	defer cancel(nil)

	var (
		onceClose sync.Once
		writeBuf  = make(chan BufMsg, 128)
		writeDone = make(chan struct{})
	)
	defer close(writeBuf)

	session := NewSession(c, writeBuf, cancel, w.wsSessionIDKey, w.hub)
	defer session.release()

	closeConn := func(err error) {
		onceClose.Do(func() {
			if errors.Is(err, ErrCloseReceived) {
				wslog.Info("WebSocket connection closed by client", zap.String(w.wsSessionIDKey, session.ID()))
			} else {
				wslog.Info("Closing WebSocket connection", zap.Error(err), zap.String(w.wsSessionIDKey, session.ID()))
			}
			cancel(err)
		})
	}

	session.close = closeConn

	wslog.Info("WebSocket connection established", zap.String(w.wsSessionIDKey, session.ID()))

	if err := w.onSessionCreated(session); err != nil {
		wslog.Error("error on session created hook", zap.Error(err), zap.String(w.wsSessionIDKey, session.ID()))
		return
	}

	c.SetReadLimit(w.readLimit)
	_ = c.SetReadDeadline(time.Now().Add(w.idleTimeout))

	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(w.idleTimeout))
	})

	c.SetCloseHandler(func(code int, text string) error {
		closeConn(ErrCloseReceived)
		return nil
	})

	wsCtx := NewCtx(ctx, session)

	// writer
	go func() {
		pingTicker := time.NewTicker(w.pingInterval)
		defer pingTicker.Stop()
		defer close(writeDone)

		for {
			select {
			case <-ctx.Done():
				_ = c.SetWriteDeadline(time.Now().Add(w.writeWait))
				_ = c.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(w.writeWait))
				_ = c.Close()
				return
			case <-pingTicker.C:
				if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(w.writeWait)); err != nil {
					closeConn(errors.Wrap(err, "failed to send ping"))
					return
				}
			case m, ok := <-writeBuf:
				if !ok {
					return
				}
				_ = c.SetWriteDeadline(time.Now().Add(w.writeWait))
				if err := c.WriteMessage(m.mt, m.msg); err != nil {
					closeConn(errors.Wrap(err, "write message error"))
					return
				}
			}
		}
	}()

	// reader
	func() {
		defer func() {
			<-writeDone
		}()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				closeConn(errors.Wrap(err, "read message error"))
				return
			}
			if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
				continue
			}

			if err := w.handle(wsCtx, msg); err != nil {
				if errors.Is(err, ErrBiz) {
					if err := wsCtx.SendError(err); err != nil {
						closeConn(errors.Wrap(err, "failed to write error response"))
					}
					continue
				} else if errors.Is(err, ErrBackpressure) {
					if err := wsCtx.SendError(err); err != nil {
						closeConn(errors.Wrap(err, "failed to write error response"))
					}
					continue
				} else {
					closeConn(errors.Wrap(err, "handle message error"))
				}
				return
			}
		}
	}()
}
