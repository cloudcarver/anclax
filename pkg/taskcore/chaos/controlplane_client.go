package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type ControlPlaneClient struct {
	baseURL string
	client  *http.Client
}

type SubmitStressProbeRequest struct {
	TaskName         string   `json:"taskName"`
	JobID            int64    `json:"jobID"`
	SleepMs          int32    `json:"sleepMs"`
	Group            string   `json:"group"`
	Labels           []string `json:"labels,omitempty"`
	DelayMs          int32    `json:"delayMs,omitempty"`
	UniqueTag        string   `json:"uniqueTag,omitempty"`
	SignalBaseURL    string   `json:"signalBaseURL,omitempty"`
	SignalIntervalMs int32    `json:"signalIntervalMs,omitempty"`
}

type SubmitStressProbeResponse struct {
	TaskID int32 `json:"taskID"`
}

type RuntimeConfigRequest struct {
	MaxStrictPercentage int32            `json:"maxStrictPercentage"`
	DefaultWeight       int32            `json:"defaultWeight"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type TaskControlRequest struct {
	UniqueTag string `json:"uniqueTag"`
}

const chaosControlClientTimeout = 60 * time.Second

func NewControlPlaneClient(baseURL string) *ControlPlaneClient {
	return &ControlPlaneClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: chaosControlClientTimeout},
	}
}

func (c *ControlPlaneClient) Health(ctx context.Context) error {
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
		return fmt.Errorf("control plane health status=%s", resp.Status)
	}
	return nil
}

func (c *ControlPlaneClient) SubmitStressProbe(ctx context.Context, reqBody SubmitStressProbeRequest) (int32, error) {
	var out SubmitStressProbeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/tasks/stress-probe", reqBody, &out); err != nil {
		return 0, err
	}
	return out.TaskID, nil
}

func (c *ControlPlaneClient) PauseTask(ctx context.Context, uniqueTag string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/pause", TaskControlRequest{UniqueTag: uniqueTag}, nil)
}

func (c *ControlPlaneClient) CancelTask(ctx context.Context, uniqueTag string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/cancel", TaskControlRequest{UniqueTag: uniqueTag}, nil)
}

func (c *ControlPlaneClient) ResumeTask(ctx context.Context, uniqueTag string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/resume", TaskControlRequest{UniqueTag: uniqueTag}, nil)
}

func (c *ControlPlaneClient) UpdateRuntimeConfig(ctx context.Context, reqBody RuntimeConfigRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/runtime-config", reqBody, nil)
}

func (c *ControlPlaneClient) doJSON(ctx context.Context, method string, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("control plane %s %s status=%s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
