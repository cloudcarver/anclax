package chaos

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// tcpProxy isolates one worker's database connections without stopping either
// process or disconnecting the executor's independent gate connection.
type tcpProxy struct {
	listener net.Listener
	target   string
	mu       sync.Mutex
	blocked  bool
	closed   bool
	epoch    uint64
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func newTCPProxy(target string) (*tcpProxy, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	p := &tcpProxy{listener: ln, target: target, conns: make(map[net.Conn]struct{})}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

func (p *tcpProxy) serve() {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go p.forward(client)
	}
}

func (p *tcpProxy) forward(client net.Conn) {
	defer p.wg.Done()
	defer client.Close()
	p.mu.Lock()
	blocked, epoch := p.blocked || p.closed, p.epoch
	p.mu.Unlock()
	if blocked {
		return
	}
	server, err := net.DialTimeout("tcp", p.target, time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	p.mu.Lock()
	if p.blocked || p.closed || p.epoch != epoch {
		p.mu.Unlock()
		return
	}
	p.conns[client] = struct{}{}
	p.conns[server] = struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, client)
		delete(p.conns, server)
		p.mu.Unlock()
	}()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(server, client)
		_ = server.Close()
		close(done)
	}()
	_, _ = io.Copy(client, server)
	_ = client.Close()
	<-done
}

func (p *tcpProxy) partition() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = true
	p.epoch++
	for conn := range p.conns {
		_ = conn.Close()
	}
}

func (p *tcpProxy) heal() {
	p.mu.Lock()
	p.blocked = false
	p.mu.Unlock()
}

func (p *tcpProxy) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.listener.Close()
	p.partition()
	p.wg.Wait()
}

func (p *tcpProxy) containerAddress() string {
	return fmt.Sprintf("host.docker.internal:%d", p.listener.Addr().(*net.TCPAddr).Port)
}
