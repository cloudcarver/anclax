package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SignalSnapshot struct {
	TaskID        int32      `json:"taskID"`
	Count         int64      `json:"count"`
	LastEmittedAt *time.Time `json:"lastEmittedAt,omitempty"`
}

type signalState struct {
	Count         int64
	LastEmittedAt time.Time
}

type signalEmitRequest struct {
	TaskID int32 `json:"taskID"`
}

type SignalService struct {
	mu               sync.Mutex
	states           map[int32]signalState
	server           *http.Server
	listener         net.Listener
	hostURL          string
	containerBaseURL string
}

func NewSignalService() *SignalService {
	return &SignalService{states: map[int32]signalState{}}
}

func (s *SignalService) Start() error {
	if s == nil {
		return fmt.Errorf("signal service is nil")
	}
	if s.listener != nil {
		return nil
	}
	port, err := findFreePort()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/signals/emit", s.handleEmit)
	mux.HandleFunc("/signals/", s.handleSignal)
	s.server = &http.Server{Handler: mux}
	s.listener = ln
	s.hostURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	s.containerBaseURL = fmt.Sprintf("http://host.docker.internal:%d", port)
	go func() {
		_ = s.server.Serve(ln)
	}()
	return nil
}

func (s *SignalService) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *SignalService) HostURL() string {
	if s == nil {
		return ""
	}
	return s.hostURL
}

func (s *SignalService) ContainerBaseURL() string {
	if s == nil {
		return ""
	}
	return s.containerBaseURL
}

func (s *SignalService) Snapshot(taskID int32) SignalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[taskID]
	if !ok {
		return SignalSnapshot{TaskID: taskID}
	}
	out := SignalSnapshot{TaskID: taskID, Count: state.Count}
	if !state.LastEmittedAt.IsZero() {
		t := state.LastEmittedAt.UTC()
		out.LastEmittedAt = &t
	}
	return out
}

func (s *SignalService) Emit(taskID int32) SignalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[taskID]
	state.Count++
	state.LastEmittedAt = time.Now().UTC()
	s.states[taskID] = state
	out := SignalSnapshot{TaskID: taskID, Count: state.Count}
	t := state.LastEmittedAt
	out.LastEmittedAt = &t
	return out
}

func (s *SignalService) Reset(taskID int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, taskID)
}

func (s *SignalService) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *SignalService) handleEmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req signalEmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TaskID <= 0 {
		http.Error(w, "taskID must be positive", http.StatusBadRequest)
		return
	}
	writeSignalJSON(w, http.StatusOK, s.Emit(req.TaskID))
}

func (s *SignalService) handleSignal(w http.ResponseWriter, r *http.Request) {
	taskID, err := parseSignalTaskID(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeSignalJSON(w, http.StatusOK, s.Snapshot(taskID))
	case http.MethodDelete:
		s.Reset(taskID)
		writeSignalJSON(w, http.StatusOK, SignalSnapshot{TaskID: taskID})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func parseSignalTaskID(path string) (int32, error) {
	raw := strings.TrimPrefix(path, "/signals/")
	if raw == "" || raw == path {
		return 0, fmt.Errorf("taskID is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid taskID %q", raw)
	}
	return int32(v), nil
}

func writeSignalJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type SignalClient struct {
	baseURL string
	client  *http.Client
}

func NewSignalClient(baseURL string) *SignalClient {
	return &SignalClient{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *SignalClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("signal service health status=%s", resp.Status)
	}
	return nil
}

func (c *SignalClient) Emit(ctx context.Context, taskID int32) (*SignalSnapshot, error) {
	var out SignalSnapshot
	body := signalEmitRequest{TaskID: taskID}
	if err := c.doJSON(ctx, http.MethodPost, "/signals/emit", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SignalClient) Snapshot(ctx context.Context, taskID int32) (*SignalSnapshot, error) {
	var out SignalSnapshot
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/signals/%d", taskID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SignalClient) Reset(ctx context.Context, taskID int32) error {
	return c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/signals/%d", taskID), nil, nil)
}

func (c *SignalClient) doJSON(ctx context.Context, method string, path string, body any, out any) error {
	var reqBody *bytes.Reader
	if body == nil {
		reqBody = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("signal service %s %s status=%s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
