package ws

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/stretchr/testify/require"
)

func newHubTestSession(id string, bufferSize int) (*Session, <-chan BufMsg) {
	writeBuf := make(chan BufMsg, bufferSize)
	return &Session{
		id:       id,
		writeBuf: writeBuf,
		cancel:   func(error) {},
	}, writeBuf
}

func TestHubBroadcastVariantsUseSubscriberSnapshot(t *testing.T) {
	hub := NewHub()
	require.NoError(t, hub.AddTopic("updates"))
	included, includedMessages := newHubTestSession("included", 4)
	excluded, excludedMessages := newHubTestSession("excluded", 4)
	require.NoError(t, hub.Subscribe("updates", included))
	require.NoError(t, hub.Subscribe("updates", excluded))

	hub.Broadcast("updates", "all-text")
	hub.broadcastExcept("updates", "except-text", excluded.id)
	hub.BroadcastBinary("updates", []byte("all-binary"))
	hub.broadcastExceptBinary("updates", []byte("except-binary"), excluded.id)

	require.Equal(t, websocket.TextMessage, (<-includedMessages).mt)
	require.Equal(t, websocket.TextMessage, (<-includedMessages).mt)
	require.Equal(t, websocket.BinaryMessage, (<-includedMessages).mt)
	require.Equal(t, websocket.BinaryMessage, (<-includedMessages).mt)
	require.Equal(t, websocket.TextMessage, (<-excludedMessages).mt)
	require.Equal(t, websocket.BinaryMessage, (<-excludedMessages).mt)
	require.Empty(t, excludedMessages)
}

func TestHubConcurrentSubscriptionChangesAndBroadcasts(t *testing.T) {
	const (
		sessionCount = 24
		iterations   = 500
	)

	hub := NewHub()
	require.NoError(t, hub.AddTopic("updates"))
	sessions := make([]*Session, 0, sessionCount)
	for i := 0; i < sessionCount; i++ {
		session, _ := newHubTestSession(fmt.Sprintf("session-%d", i), iterations*4)
		sessions = append(sessions, session)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, session := range sessions {
		session := session
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				require.NoError(t, hub.Subscribe("updates", session))
				runtime.Gosched()
				require.NoError(t, hub.Unsubscribe("updates", session))
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			hub.Broadcast("updates", "text")
			hub.broadcastExcept("updates", "text", sessions[0].id)
			hub.BroadcastBinary("updates", []byte("binary"))
			hub.broadcastExceptBinary("updates", []byte("binary"), sessions[0].id)
			runtime.Gosched()
		}
	}()

	close(start)
	wg.Wait()
}
