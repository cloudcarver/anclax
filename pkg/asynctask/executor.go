package asynctask

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
)

const runtimeConfigAckChannel = "anclax_worker_runtime_config_ack"

type Executor struct {
	model      model.ModelInterface
	now        func() time.Time
	waitForAck func(ctx context.Context, requestID string, listenTimeout time.Duration) error
}

type runtimeConfigPayload struct {
	MaxStrictPercentage *int32           `json:"maxStrictPercentage,omitempty"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type runtimeConfigNotification struct {
	Op     string `json:"op"`
	Params struct {
		Version   int64  `json:"version"`
		RequestID string `json:"request_id"`
	} `json:"params"`
}

type runtimeConfigAckNotification struct {
	Op     string `json:"op"`
	Params struct {
		RequestID      string `json:"request_id"`
		WorkerID       string `json:"worker_id"`
		AppliedVersion int64  `json:"applied_version"`
	} `json:"params"`
}

func NewExecutor(cfg *config.Config, model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{
		model:      model,
		now:        time.Now,
		waitForAck: newRuntimeConfigAckWaiter(runtimeListenDSNFromConfig(cfg)),
	}
}

func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, params *taskgen.DeleteOpaqueKeyParameters) error {
	return e.model.DeleteOpaqueKey(ctx, params.KeyID)
}

func (e *Executor) OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *taskgen.DeleteOpaqueKeyParameters, tx core.Tx) error {
	return nil
}

func (e *Executor) ExecuteUpdateWorkerRuntimeConfig(ctx context.Context, params *taskgen.UpdateWorkerRuntimeConfigParameters) error {
	startAt := e.now()
	requestID := ""
	if params.RequestID != nil && *params.RequestID != "" {
		requestID = *params.RequestID
	} else {
		requestID = uuid.NewString()
	}

	notifyInterval, listenTimeout, heartbeatTTL, err := parseRuntimeConfigDurations(params)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	labelWeights, err := buildLabelWeights(params)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	maxStrictPercentage, err := normalizeMaxStrictPercentage(params.MaxStrictPercentage)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	payloadRaw, err := json.Marshal(runtimeConfigPayload{
		MaxStrictPercentage: maxStrictPercentage,
		LabelWeights:        labelWeights,
	})
	if err != nil {
		return errors.Wrap(err, "marshal runtime config payload")
	}

	created, err := e.model.CreateWorkerRuntimeConfig(ctx, payloadRaw)
	if err != nil {
		return errors.Wrap(err, "create worker runtime config")
	}

	targetVersion := created.Version
	notifyRaw, err := json.Marshal(runtimeConfigNotification{
		Op: "up_config",
		Params: struct {
			Version   int64  `json:"version"`
			RequestID string `json:"request_id"`
		}{
			Version:   targetVersion,
			RequestID: requestID,
		},
	})
	if err != nil {
		return errors.Wrap(err, "marshal runtime config notification payload")
	}

	for {
		latest, err := e.model.GetLatestWorkerRuntimeConfig(ctx)
		if err != nil {
			return errors.Wrap(err, "get latest runtime config")
		}
		if latest.Version > targetVersion {
			metrics.RuntimeConfigSupersededTotal.Inc()
			return nil
		}

		laggingWorkers, err := e.model.ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
			HeartbeatCutoff: e.now().Add(-heartbeatTTL),
			Version:         targetVersion,
		})
		if err != nil {
			return errors.Wrap(err, "list lagging alive workers")
		}
		metrics.RuntimeConfigLaggingWorkers.Set(float64(len(laggingWorkers)))
		if len(laggingWorkers) == 0 {
			metrics.RuntimeConfigConvergenceSeconds.Observe(e.now().Sub(startAt).Seconds())
			return nil
		}

		if err := e.model.NotifyWorkerRuntimeConfig(ctx, string(notifyRaw)); err != nil {
			return errors.Wrap(err, "notify worker runtime config")
		}

		if e.waitForAck != nil {
			if err := e.waitForAck(ctx, requestID, listenTimeout); err != nil {
				return errors.Wrap(err, "wait for runtime config ack")
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(notifyInterval):
		}
	}
}

func (e *Executor) OnUpdateWorkerRuntimeConfigFailed(ctx context.Context, taskID int32, params *taskgen.UpdateWorkerRuntimeConfigParameters, tx core.Tx) error {
	return nil
}

func normalizeMaxStrictPercentage(maxStrictPercentage *int32) (*int32, error) {
	if maxStrictPercentage == nil {
		return nil, nil
	}
	if *maxStrictPercentage < 0 || *maxStrictPercentage > 100 {
		return nil, errors.New("maxStrictPercentage must be within [0, 100]")
	}
	return maxStrictPercentage, nil
}

func buildLabelWeights(params *taskgen.UpdateWorkerRuntimeConfigParameters) (map[string]int32, error) {
	defaultWeight := int32(1)
	if params.DefaultWeight != nil {
		defaultWeight = *params.DefaultWeight
	}
	if defaultWeight < 1 {
		return nil, errors.New("defaultWeight must be greater than or equal to 1")
	}

	if len(params.Labels) != len(params.Weights) {
		return nil, errors.New("labels and weights must have the same length")
	}

	labelWeights := map[string]int32{
		"default": defaultWeight,
	}
	for idx, label := range params.Labels {
		if label == "" {
			return nil, errors.New("labels cannot contain empty value")
		}
		weight := params.Weights[idx]
		if weight < 1 {
			return nil, errors.New("weights must be greater than or equal to 1")
		}
		labelWeights[label] = weight
	}
	return labelWeights, nil
}

func parseRuntimeConfigDurations(params *taskgen.UpdateWorkerRuntimeConfigParameters) (notifyInterval time.Duration, listenTimeout time.Duration, heartbeatTTL time.Duration, retErr error) {
	notifyInterval = time.Second
	listenTimeout = 2 * time.Second
	heartbeatTTL = 10 * time.Second

	if params.NotifyInterval != nil {
		notifyInterval, retErr = time.ParseDuration(*params.NotifyInterval)
		if retErr != nil {
			return 0, 0, 0, errors.Wrap(retErr, "invalid notifyInterval duration")
		}
	}

	if params.ListenTimeout != nil {
		listenTimeout, retErr = time.ParseDuration(*params.ListenTimeout)
		if retErr != nil {
			return 0, 0, 0, errors.Wrap(retErr, "invalid listenTimeout duration")
		}
	}

	if params.HeartbeatTTL != nil {
		heartbeatTTL, retErr = time.ParseDuration(*params.HeartbeatTTL)
		if retErr != nil {
			return 0, 0, 0, errors.Wrap(retErr, "invalid heartbeatTTL duration")
		}
	}

	if notifyInterval <= 0 || listenTimeout <= 0 || heartbeatTTL <= 0 {
		return 0, 0, 0, errors.New("notifyInterval, listenTimeout, and heartbeatTTL must be positive")
	}
	return notifyInterval, listenTimeout, heartbeatTTL, nil
}

func isRuntimeConfigAckForRequest(payload string, requestID string) bool {
	if requestID == "" {
		return false
	}
	var ack runtimeConfigAckNotification
	if err := json.Unmarshal([]byte(payload), &ack); err != nil {
		return false
	}
	if ack.Op != "" && ack.Op != "ack" {
		return false
	}
	return ack.Params.RequestID == requestID
}

func runtimeListenDSNFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.Pg.DSN != nil && *cfg.Pg.DSN != "" {
		return *cfg.Pg.DSN
	}
	if cfg.Pg.User == "" || cfg.Pg.Host == "" || cfg.Pg.Port == 0 || cfg.Pg.Db == "" {
		return ""
	}
	dsnURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.Pg.User, cfg.Pg.Password),
		Host:     fmt.Sprintf("%s:%d", cfg.Pg.Host, cfg.Pg.Port),
		Path:     cfg.Pg.Db,
		RawQuery: "sslmode=" + utils.IfElse(cfg.Pg.SSLMode == "", "require", cfg.Pg.SSLMode),
	}
	return dsnURL.String()
}

func newRuntimeConfigAckWaiter(dsn string) func(ctx context.Context, requestID string, listenTimeout time.Duration) error {
	if dsn == "" {
		return nil
	}
	return func(ctx context.Context, requestID string, listenTimeout time.Duration) error {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())

		if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", runtimeConfigAckChannel)); err != nil {
			return err
		}

		waitCtx, cancel := context.WithTimeout(ctx, listenTimeout)
		defer cancel()
		for {
			notification, err := conn.WaitForNotification(waitCtx)
			if err != nil {
				if stdErrors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				if stdErrors.Is(err, context.Canceled) && ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}

			if isRuntimeConfigAckForRequest(notification.Payload, requestID) {
				return nil
			}
		}
	}
}
