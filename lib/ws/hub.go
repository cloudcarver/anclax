package ws

import (
	"sync"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

var subscriptionGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "anclax_hub_subscriptions",
	Help: "Current number of websocket subscriptions",
})

var broadcastErrorCounter = promauto.NewCounter(prometheus.CounterOpts{
	Name: "anclax_ws_broadcast_errors_total",
	Help: "Total number of websocket broadcast errors",
})

var (
	ErrTopicAlreadyExists = errors.New("topic already exists")
	ErrTopicNotFound      = errors.New("topic not found")
	ErrAlreadySubscribed  = errors.New("already subscribed to topic")
)

type Hub struct {
	mu         sync.RWMutex
	topicRooms map[string]map[string]*Session
}

func NewHub() *Hub {
	return &Hub{
		topicRooms: make(map[string]map[string]*Session),
	}
}

func (h *Hub) AddTopic(topic string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.topicRooms[topic]; ok {
		return ErrTopicAlreadyExists
	}
	h.topicRooms[topic] = make(map[string]*Session)

	return nil
}

func (h *Hub) Subscribe(topic string, s *Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	rooms, ok := h.topicRooms[topic]
	if !ok {
		return errors.Wrapf(ErrTopicNotFound, "topic %s does not exist", topic)
	}
	if _, ok := rooms[s.id]; ok {
		return errors.Wrapf(ErrAlreadySubscribed, "session %s already subscribed to topic %s", s.id, topic)
	}
	rooms[s.id] = s
	subscriptionGauge.Inc()
	return nil
}

func (h *Hub) Unsubscribe(topic string, s *Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	rooms, ok := h.topicRooms[topic]
	if !ok {
		return errors.Wrapf(ErrTopicNotFound, "topic %s does not exist", topic)
	}
	if _, ok := rooms[s.id]; !ok {
		return nil
	}
	delete(rooms, s.id)
	subscriptionGauge.Dec()
	return nil
}

func (h *Hub) snapshotSessions(topic string) []*Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room, ok := h.topicRooms[topic]
	if !ok {
		return nil
	}
	sessions := make([]*Session, 0, len(room))
	for _, session := range room {
		sessions = append(sessions, session)
	}
	return sessions
}

func (h *Hub) broadcastExcept(topic string, data any, exceptID string) {
	for _, s := range h.snapshotSessions(topic) {
		if s.id == exceptID {
			continue
		}
		if err := s.WriteTextMessage(data); err != nil {
			broadcastErrorCounter.Inc()
			wslog.Error(
				"failed to write text message while broadcasting",
				zap.Error(err),
				zap.String("topic", topic),
				zap.String("session_id", s.id),
			)
		}
	}
}

func (h *Hub) Broadcast(topic string, data any) {
	for _, s := range h.snapshotSessions(topic) {
		if err := s.WriteTextMessage(data); err != nil {
			broadcastErrorCounter.Inc()
			wslog.Error(
				"failed to write text message while broadcasting",
				zap.Error(err),
				zap.String("topic", topic),
				zap.String("session_id", s.id),
			)
		}
	}
}

// broadcastExceptBinary sends a binary payload to all subscribers of a topic
// except the session identified by exceptID.
func (h *Hub) broadcastExceptBinary(topic string, data []byte, exceptID string) {
	for _, s := range h.snapshotSessions(topic) {
		if s.id == exceptID {
			continue
		}
		s.WriteBinaryMessage(data)
	}
}

// BroadcastBinary sends a binary payload to all subscribers of a topic.
func (h *Hub) BroadcastBinary(topic string, data []byte) {
	for _, s := range h.snapshotSessions(topic) {
		s.WriteBinaryMessage(data)
	}
}
