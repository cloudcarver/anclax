package ws

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"

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
	idleTimeout  = 40 * time.Second
	pingInterval = 30 * time.Second
	writeWait    = 10 * time.Second

	wsSessionIDKey = "ws_session_id"
)

type BufMsg struct {
	mt  int
	msg []byte
}

type Session struct {
	id       string
	conn     *websocket.Conn
	writeBuf chan<- BufMsg
	closers  []func() error
	cancel   context.CancelCauseFunc
}

func NewSession(conn *websocket.Conn, writeBuf chan<- BufMsg, cancel context.CancelCauseFunc) *Session {
	id := uuid.New().String()
	conn.Locals(wsSessionIDKey, id)
	return &Session{
		conn:     conn,
		writeBuf: writeBuf,
		closers:  make([]func() error, 0),
		cancel:   cancel,
		id:       id,
	}
}

func (s *Session) Close() {
	for _, closer := range s.closers {
		if err := closer(); err != nil {
			wslog.Error("failed to close resource", zap.Error(err), zap.String(wsSessionIDKey, s.ID()))
		}
	}
}

func (s *Session) RegisterCloser(closer func() error) {
	s.closers = append(s.closers, closer)
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Locals(key string, value ...any) any {
	return s.conn.Locals(key, value...)
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

type Ctx struct {
	context.Context
	*Session
	ID *string
}

func NewCtx(ctx context.Context, s *Session) *Ctx {
	return &Ctx{Context: ctx, Session: s}
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
}

func NewWebsocketController(globalCtx *globalctx.GlobalContext) *WebsocketController {
	return &WebsocketController{
		ctx:              globalCtx.Context(),
		handle:           func(ctx *Ctx, data []byte) error { return ErrHandlerNotRegistered },
		onSessionCreated: func(s *Session) error { return nil },
		hub:              NewHub(),
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

	session := NewSession(c, writeBuf, cancel)
	defer session.Close()

	wslog.Info("WebSocket connection established", zap.String(wsSessionIDKey, session.ID()))

	if err := w.onSessionCreated(session); err != nil {
		wslog.Error("failed to handle session creation", zap.Error(err), zap.String(wsSessionIDKey, session.ID()))
		return
	}

	closeConn := func(err error) {
		onceClose.Do(func() {
			if errors.Is(err, ErrCloseReceived) {
				wslog.Info("WebSocket connection closed by client", zap.String(wsSessionIDKey, session.ID()))
			} else {
				wslog.Info("Closing WebSocket connection", zap.Error(err), zap.String(wsSessionIDKey, session.ID()))
			}
			cancel(err)
		})
	}

	c.SetReadLimit(100 * 1024 /* 100KB */)
	_ = c.SetReadDeadline(time.Now().Add(idleTimeout))

	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(idleTimeout))
	})

	c.SetCloseHandler(func(code int, text string) error {
		closeConn(ErrCloseReceived)
		return nil
	})

	wsCtx := NewCtx(ctx, session)

	// writer
	go func() {
		pingTicker := time.NewTicker(pingInterval)
		defer pingTicker.Stop()
		defer close(writeDone)

		for {
			select {
			case <-ctx.Done():
				_ = c.SetWriteDeadline(time.Now().Add(writeWait))
				_ = c.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(writeWait))
				_ = c.Close()
				return
			case <-pingTicker.C:
				if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
					closeConn(errors.Wrap(err, "failed to send ping"))
					return
				}
			case m, ok := <-writeBuf:
				if !ok {
					return
				}
				_ = c.SetWriteDeadline(time.Now().Add(writeWait))
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
