# Anclax websocket / realtime

Use `github.com/cloudcarver/anclax/lib/ws` for websocket transport and keep business logic in services.

## Core types

### `ws.Handler`
Implement the websocket entry point.

```go
type Handler interface {
    OnSessionCreated(s *Session) error
    Handle(ctx *Ctx, data []byte) error
}
```

Use it to:
- initialize per-connection state,
- register cleanup,
- parse incoming frames,
- dispatch to business logic,
- write responses.

### `ws.Session`
Represents one websocket connection.

Useful methods:
- `ID()`
- `Conn()`
- `RegisterOnClose(func() error)`
- `WriteTextMessage(any)`
- `WriteBinaryMessage([]byte)`
- `Broadcast(topic, data)`
- `BroadcastBinary(topic, data)`

Use connection locals for per-session state:

```go
s.Conn().Locals("subs", map[string]struct{}{})
```

### `ws.Ctx`
Message-scoped context containing the session.

Useful fields/methods:
- embedded `context.Context`
- embedded `*ws.Session`
- `SetID(id string)`
- `SendError(err error)`

### `ws.Hub`
Topic-based pub/sub for active sessions.

Useful methods:
- `AddTopic(topic string)`
- `Subscribe(topic string, s *Session)`
- `Unsubscribe(topic string, s *Session)`
- `Broadcast(topic string, data any)`
- `BroadcastBinary(topic string, data []byte)`

Important: `Subscribe` only works for topics that were added first.

### `ws.WsCfg`
Controller config.

Main fields:
- `WebSocketPath`
- `ReadLimit`
- `IdleTimeoutSeconds`
- `PingIntervalSeconds`
- `WriteWaitSeconds`
- `SessionIDKey`

## Design rules

- Treat the websocket layer as a transport adapter around app-specific business logic.
- Define an explicit websocket message protocol for your app.
- Implement `ws.Handler` with:
  - `OnSessionCreated(*ws.Session)` for connection setup
  - `Handle(*ws.Ctx, []byte)` for parsing, validation, dispatch, and replies
- Store per-session state in `Conn().Locals(...)`, such as subscriptions or authenticated user info.
- Register cleanup with `Session.RegisterOnClose(...)`.
- Create allowed topics up front with `hub.AddTopic(...)`.
- Keep subscribe/unsubscribe rules in a service layer when they involve business policy.
- Use `hub.Broadcast(...)` for server-originated fanout.
- Use `session.Broadcast(...)` to fan out to other subscribers while excluding the sender.
- Call `c.SetID(req.ID)` if your protocol uses request IDs for correlated responses.
- Return `ws.ErrBadRequest` for malformed frames.
- Write typed error responses yourself when your app needs a structured response envelope.

## Recommended flow

1. Define websocket request/response types.
2. Implement a handler that unmarshals requests and dispatches by message type.
3. Initialize session-local state in `OnSessionCreated`.
4. Register `OnClose` cleanup to remove subscriptions and other resources.
5. Add topics during startup.
6. Subscribe/unsubscribe sessions through a service or policy layer.
7. Broadcast domain events through the hub.

## Minimal pattern

### Message handler

```go
type SubscriptionService interface {
    Subscribe(topic string, s *ws.Session) error
    Unsubscribe(topic string, s *ws.Session) error
}

type MessageHandler struct {
    subs SubscriptionService
}

func NewMessageHandler(subs SubscriptionService) *MessageHandler {
    return &MessageHandler{subs: subs}
}

func (h *MessageHandler) OnSessionCreated(s *ws.Session) error {
    s.Conn().Locals("subs", map[string]struct{}{})

    s.RegisterOnClose(func() error {
        subs := s.Conn().Locals("subs").(map[string]struct{})
        for topic := range subs {
            _ = h.subs.Unsubscribe(topic, s)
        }
        return nil
    })

    return nil
}

func (h *MessageHandler) Handle(c *ws.Ctx, data []byte) error {
    var req MyWsMessage
    if err := json.Unmarshal(data, &req); err != nil {
        return errors.Wrap(ws.ErrBadRequest, "invalid json")
    }

    c.SetID(req.ID)

    switch req.Type {
    case "subscribe":
        return h.handleSubscribe(c, req.Topic)
    case "unsubscribe":
        return h.handleUnsubscribe(c, req.Topic)
    case "ping":
        return c.WriteTextMessage(MyWsResponse{ID: req.ID, Type: "pong"})
    default:
        return errors.Wrap(ws.ErrBadRequest, "unknown message type")
    }
}

func (h *MessageHandler) handleSubscribe(c *ws.Ctx, topic string) error {
    subs := c.Conn().Locals("subs").(map[string]struct{})
    if _, ok := subs[topic]; ok {
        return c.WriteTextMessage(MyWsResponse{ID: *c.ID, Type: "success", Message: "already subscribed"})
    }

    if err := h.subs.Subscribe(topic, c.Session); err != nil {
        return err
    }

    subs[topic] = struct{}{}
    return c.WriteTextMessage(MyWsResponse{ID: *c.ID, Type: "success", Message: "subscribed"})
}

func (h *MessageHandler) handleUnsubscribe(c *ws.Ctx, topic string) error {
    subs := c.Conn().Locals("subs").(map[string]struct{})
    if _, ok := subs[topic]; !ok {
        return c.WriteTextMessage(MyWsResponse{ID: *c.ID, Type: "success", Message: "not subscribed"})
    }

    delete(subs, topic)
    _ = h.subs.Unsubscribe(topic, c.Session)
    return c.WriteTextMessage(MyWsResponse{ID: *c.ID, Type: "success", Message: "unsubscribed"})
}
```

### Topic-owning service

```go
type RealtimeService struct {
    hub *ws.Hub
}

func NewRealtimeService(hub *ws.Hub) (*RealtimeService, error) {
    for _, topic := range []string{"orders", "prices", "alerts"} {
        if err := hub.AddTopic(topic); err != nil {
            return nil, err
        }
    }
    return &RealtimeService{hub: hub}, nil
}

func (s *RealtimeService) Subscribe(topic string, session *ws.Session) error {
    return s.hub.Subscribe(topic, session)
}

func (s *RealtimeService) Unsubscribe(topic string, session *ws.Session) error {
    return s.hub.Unsubscribe(topic, session)
}

func (s *RealtimeService) PublishOrderCreated(evt *OrderCreatedEvent) {
    s.hub.Broadcast("orders", evt)
}
```

### Mounting the controller

```go
handler := NewMessageHandler(realtimeService)
wsc := ws.New(globalCtx.Context(), handler, &ws.WsCfg{
    WebSocketPath: "/ws",
})
wsc.Mount(anclaxApp.GetServer().GetApp())
```

Keep the controller or hub reachable from DI if other parts of the app need to publish websocket events.

## Business-logic guidance

Good places for business logic:
- validating whether a session may subscribe to a topic,
- translating domain events into websocket event payloads,
- publishing events from async tasks or services,
- enforcing topic naming or tenancy rules.

Avoid putting these directly in low-level frame parsing code:
- authorization policy,
- domain queries,
- event construction,
- topic lifecycle rules.

## Error handling guidance

- Use `ws.ErrBadRequest` for malformed or unsupported client frames.
- Use your own response envelope if clients expect typed error messages.
- Use `RegisterOnClose` cleanup aggressively so disconnects do not leak subscriptions.
- Expect backpressure errors when clients cannot keep up.

## Current repo caveats

Verify framework plumbing before implementation in this repo version:

- `pkg/server.Server` exposes `Websocket()`, but `server.NewServer` does not initialize `s.wsc`.
- `lib/ws.New(ctx, handler, cfg)` currently does not assign the provided handler to the returned controller.
- `ws.Ctx.SendError` writes a generic error frame, so apps with typed websocket responses usually need custom error writing.

If you build websocket support in this repo, fix or verify those integration points first.
