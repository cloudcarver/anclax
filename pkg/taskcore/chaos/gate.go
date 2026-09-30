package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// GateAttempt identifies an actual executor invocation, not just a claimed row.
type GateAttempt struct {
	Key          string `json:"key"`
	TaskID       int32  `json:"taskID"`
	LeaseVersion int64  `json:"leaseVersion"`
	Worker       string `json:"worker"`
	Exited       bool   `json:"exited"`
}

// GateService holds selected test handlers until the orchestrator releases them.
// It runs outside the containers so a killed worker cannot erase its evidence.
type GateService struct {
	mu       sync.Mutex
	gates    map[string]chan struct{}
	attempts []GateAttempt
	server   *http.Server
	port     int
}

func newGateService() (*GateService, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	g := &GateService{gates: make(map[string]chan struct{}), port: ln.Addr().(*net.TCPAddr).Port}
	g.server = &http.Server{Handler: http.HandlerFunc(g.enter), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = g.server.Serve(ln) }()
	return g, nil
}

func (g *GateService) enter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/enter" {
		http.Error(w, "POST /enter required", http.StatusMethodNotAllowed)
		return
	}
	var attempt GateAttempt
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&attempt); err != nil || attempt.Key == "" || attempt.TaskID <= 0 || attempt.LeaseVersion <= 0 || attempt.Worker == "" {
		http.Error(w, "invalid gate attempt", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	gate := g.gates[attempt.Key]
	if gate == nil {
		gate = make(chan struct{})
		g.gates[attempt.Key] = gate
	}
	attempt.Exited = false
	index := len(g.attempts)
	g.attempts = append(g.attempts, attempt)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.attempts[index].Exited = true
		g.mu.Unlock()
	}()
	select {
	case <-gate:
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

func (g *GateService) release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate := g.gates[key]
	if gate == nil {
		gate = make(chan struct{})
		g.gates[key] = gate
	}
	select {
	case <-gate:
	default:
		close(gate)
	}
}

func (g *GateService) snapshot(key string) []GateAttempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []GateAttempt
	for _, attempt := range g.attempts {
		if attempt.Key == key {
			out = append(out, attempt)
		}
	}
	return out
}

func (g *GateService) close() {
	_ = g.server.Close()
}

func (g *GateService) containerURL() string {
	return fmt.Sprintf("http://host.docker.internal:%d", g.port)
}

// WaitAtGate is used only by the chaos worker's executor wrapper.
func WaitAtGate(ctx context.Context, baseURL string, attempt GateAttempt) error {
	raw, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/enter", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("gate returned %s", resp.Status)
	}
	return nil
}
