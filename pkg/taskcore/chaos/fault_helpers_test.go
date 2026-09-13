package chaos

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateRecordsCancellationAndHoldsReplacementUntilRelease(t *testing.T) {
	g, err := newGateService()
	require.NoError(t, err)
	t.Cleanup(g.close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url := fmt.Sprintf("http://127.0.0.1:%d", g.port)
	done := make(chan error, 1)
	go func() {
		done <- WaitAtGate(ctx, url, GateAttempt{Key: "task", TaskID: 1, LeaseVersion: 1, Worker: "old"})
	}()
	require.Eventually(t, func() bool { return len(g.snapshot("task")) == 1 }, time.Second, time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("gate returned before release: %v", err)
	default:
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Eventually(t, func() bool { return g.snapshot("task")[0].Exited }, time.Second, time.Millisecond)
	go func() {
		done <- WaitAtGate(t.Context(), url, GateAttempt{Key: "task", TaskID: 1, LeaseVersion: 2, Worker: "new"})
	}()
	require.Eventually(t, func() bool { return len(g.snapshot("task")) == 2 }, time.Second, time.Millisecond)
	require.False(t, g.snapshot("task")[1].Exited)
	g.release("task")
	g.release("task") // Idempotent cleanup must not panic.
	require.NoError(t, <-done)
	got := g.snapshot("task")
	require.Equal(t, int64(1), got[0].LeaseVersion)
	require.Equal(t, int64(2), got[1].LeaseVersion)
}

func TestDatabaseProxyDropsExistingConnectionsAndRejectsNewOnesUntilHealed(t *testing.T) {
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	p, err := newTCPProxy(echo.Addr().String())
	require.NoError(t, err)
	t.Cleanup(p.close)
	dial := func() net.Conn {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.listener.Addr().(*net.TCPAddr).Port))
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
		return conn
	}
	checkEcho := func(conn net.Conn) {
		_, err := conn.Write([]byte("ping"))
		require.NoError(t, err)
		buf := make([]byte, 4)
		_, err = io.ReadFull(conn, buf)
		require.NoError(t, err)
		require.Equal(t, "ping", string(buf))
	}
	old := dial()
	checkEcho(old)
	p.partition()
	_, err = old.Read(make([]byte, 1))
	require.Error(t, err)
	blocked := dial()
	_, err = blocked.Read(make([]byte, 1))
	require.Error(t, err)
	if timeout, ok := err.(net.Error); ok {
		require.False(t, timeout.Timeout(), "partition must reject new connections, not just leave reads waiting")
	}
	p.heal()
	checkEcho(dial())
}
