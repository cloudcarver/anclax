package ws

import (
	"sync"

	"github.com/pkg/errors"
)

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
	return nil
}

func (h *Hub) Broadcast(topic string, data any) {
	h.mu.RLock()
	rooms, ok := h.topicRooms[topic]
	h.mu.RUnlock()
	if !ok {
		return
	}
	for _, s := range rooms {
		s.WriteTextMessage(data)
	}
}
