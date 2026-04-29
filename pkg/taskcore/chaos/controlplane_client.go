package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	FailMode         string   `json:"failMode,omitempty"`
	Labels           []string `json:"labels,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	DelayMs          int32    `json:"delayMs,omitempty"`
	UniqueTag        string   `json:"uniqueTag,omitempty"`
	RetryInterval    string   `json:"retryInterval,omitempty"`
	RetryMaxAttempts *int32   `json:"retryMaxAttempts,omitempty"`
	SignalBaseURL    string   `json:"signalBaseURL,omitempty"`
	SignalIntervalMs int32    `json:"signalIntervalMs,omitempty"`
}

type SubmitStressProbeResponse struct {
	TaskID int32 `json:"taskID"`
}

type SubmitCancelObservableProbeRequest struct {
	TaskName         string   `json:"taskName"`
	Group            string   `json:"group"`
	Labels           []string `json:"labels,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	UniqueTag        string   `json:"uniqueTag,omitempty"`
	SignalBaseURL    string   `json:"signalBaseURL,omitempty"`
	SignalIntervalMs int32    `json:"signalIntervalMs,omitempty"`
}

type RuntimeConfigRequest struct {
	MaxStrictPercentage int32            `json:"maxStrictPercentage"`
	DefaultWeight       int32            `json:"defaultWeight"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type RuntimeConfigResponse struct {
	TaskID int32 `json:"taskID"`
}

type TaskControlRequest struct {
	UniqueTag string `json:"uniqueTag"`
}

type TaskTagsControlRequest struct {
	Tags          []string   `json:"tags"`
	ExceptTagSets [][]string `json:"exceptTagSets,omitempty"`
}

const chaosControlClientTimeout = 5 * time.Minute

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

func (c *ControlPlaneClient) SubmitCancelObservableProbe(ctx context.Context, reqBody SubmitCancelObservableProbeRequest) (int32, error) {
	var out SubmitStressProbeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/tasks/cancel-observable-probe", reqBody, &out); err != nil {
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

func (c *ControlPlaneClient) PauseTasksByTags(ctx context.Context, tags []string, exceptTagSets [][]string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/pause-by-tags", TaskTagsControlRequest{Tags: tags, ExceptTagSets: exceptTagSets}, nil)
}

func (c *ControlPlaneClient) CancelTasksByTags(ctx context.Context, tags []string, exceptTagSets [][]string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/cancel-by-tags", TaskTagsControlRequest{Tags: tags, ExceptTagSets: exceptTagSets}, nil)
}

func (c *ControlPlaneClient) ResumeTasksByTags(ctx context.Context, tags []string, exceptTagSets [][]string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/resume-by-tags", TaskTagsControlRequest{Tags: tags, ExceptTagSets: exceptTagSets}, nil)
}

func (c *ControlPlaneClient) StartUpdateRuntimeConfig(ctx context.Context, reqBody RuntimeConfigRequest) (int32, error) {
	var out RuntimeConfigResponse
	if err := c.doJSON(ctx, http.MethodPost, "/runtime-config", reqBody, &out); err != nil {
		return 0, err
	}
	return out.TaskID, nil
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
		bodyRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		body := strings.TrimSpace(string(bodyRaw))
		if body == "" {
			return fmt.Errorf("control plane %s %s status=%s", method, path, resp.Status)
		}
		return fmt.Errorf("control plane %s %s status=%s body=%q", method, path, resp.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
