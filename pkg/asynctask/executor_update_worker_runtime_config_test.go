package asynctask

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/metrics"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestExecuteUpdateWorkerRuntimeConfigRejectsInvalidParams(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	e := &Executor{
		model: model.NewMockModelInterface(ctrl),
		now:   time.Now,
	}

	err := e.ExecuteUpdateWorkerRuntimeConfig(context.Background(), &taskgen.UpdateWorkerRuntimeConfigParameters{
		Labels:  []string{"billing"},
		Weights: []int32{},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
}

func TestExecuteUpdateWorkerRuntimeConfigConvergesAfterNotifyAndAckWait(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	fixedNow := time.Date(2026, 2, 13, 12, 0, 0, 0, time.UTC)
	requestID := "req-1"
	maxStrict := int32(60)
	defaultWeight := int32(3)

	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model: mockModel,
		now: func() time.Time {
			return fixedNow
		},
	}
	waitCalls := 0
	e.waitForAck = func(ctx context.Context, requestID string, listenTimeout time.Duration) error {
		waitCalls++
		require.Equal(t, "req-1", requestID)
		require.Equal(t, 2*time.Second, listenTimeout)
		return nil
	}

	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload json.RawMessage) (*querier.AnclaxWorkerRuntimeConfig, error) {
			var parsed runtimeConfigPayload
			require.NoError(t, json.Unmarshal(payload, &parsed))
			require.NotNil(t, parsed.MaxStrictPercentage)
			require.Equal(t, int32(60), *parsed.MaxStrictPercentage)
			require.Equal(t, int32(3), parsed.LabelWeights["default"])
			require.Equal(t, int32(5), parsed.LabelWeights["billing"])
			return &querier.AnclaxWorkerRuntimeConfig{Version: 7}, nil
		},
	)

	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 7}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
		HeartbeatCutoff: fixedNow.Add(-9 * time.Second),
		Version:         7,
	}).Return([]uuid.UUID{uuid.New()}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfig(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			var parsed runtimeConfigNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &parsed))
			require.Equal(t, "up_config", parsed.Op)
			require.Equal(t, int64(7), parsed.Params.Version)
			require.Equal(t, requestID, parsed.Params.RequestID)
			return nil
		},
	)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 7}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
		HeartbeatCutoff: fixedNow.Add(-9 * time.Second),
		Version:         7,
	}).Return([]uuid.UUID{}, nil)

	err := e.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{
		RequestID:           &requestID,
		MaxStrictPercentage: &maxStrict,
		DefaultWeight:       &defaultWeight,
		Labels:              []string{"billing"},
		Weights:             []int32{5},
	})
	require.NoError(t, err)
	require.Equal(t, 1, waitCalls)
}

func TestExecuteUpdateWorkerRuntimeConfigSupersededByNewerVersion(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	requestID := "req-2"

	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model:      mockModel,
		now:        time.Now,
		waitForAck: func(context.Context, string, time.Duration) error { return nil },
	}

	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 5}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 6}, nil)

	before := testutil.ToFloat64(metrics.RuntimeConfigSupersededTotal)
	err := e.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{
		RequestID: &requestID,
	})
	require.NoError(t, err)
	after := testutil.ToFloat64(metrics.RuntimeConfigSupersededTotal)
	require.Equal(t, before+1, after)
}

func TestExecuteUpdateWorkerRuntimeConfigReturnsWaitError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	requestID := "req-3"
	waitErr := errors.New("wait failed")

	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model: mockModel,
		now:   time.Now,
		waitForAck: func(context.Context, string, time.Duration) error {
			return waitErr
		},
	}

	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 11}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 11}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfig(ctx, gomock.Any()).Return(nil)

	err := e.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{
		RequestID: &requestID,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for runtime config ack")
}

func TestExecuteUpdateWorkerRuntimeConfigGeneratesRequestIDWhenMissing(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model:      mockModel,
		now:        time.Now,
		waitForAck: nil,
	}

	notifyInterval := "1ms"
	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 21}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 21}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfig(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			var parsed runtimeConfigNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &parsed))
			require.Equal(t, "up_config", parsed.Op)
			require.Equal(t, int64(21), parsed.Params.Version)
			_, err := uuid.Parse(parsed.Params.RequestID)
			require.NoError(t, err)
			return nil
		},
	)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 21}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, gomock.Any()).Return([]uuid.UUID{}, nil)

	err := e.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: &notifyInterval,
	})
	require.NoError(t, err)
}

func TestExecuteUpdateWorkerRuntimeConfigUsesExecutorHeartbeatTTL(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	fixedNow := time.Date(2026, 2, 13, 12, 0, 0, 0, time.UTC)
	notifyInterval := "1ms"

	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model:                     mockModel,
		now:                       func() time.Time { return fixedNow },
		waitForAck:                nil,
		runtimeConfigHeartbeatTTL: 30 * time.Second,
	}

	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 25}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 25}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
		HeartbeatCutoff: fixedNow.Add(-30 * time.Second),
		Version:         25,
	}).Return([]uuid.UUID{uuid.New()}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfig(ctx, gomock.Any()).Return(nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 25}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
		HeartbeatCutoff: fixedNow.Add(-30 * time.Second),
		Version:         25,
	}).Return([]uuid.UUID{}, nil)

	err := e.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: &notifyInterval,
	})
	require.NoError(t, err)
	afterLagging := testutil.ToFloat64(metrics.RuntimeConfigLaggingWorkers)
	require.Equal(t, float64(0), afterLagging)
}

func TestExecuteUpdateWorkerRuntimeConfigReturnsContextErrorWithoutAckWaiter(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model:      mockModel,
		now:        time.Now,
		waitForAck: nil,
	}
	notifyInterval := "1s"

	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 27}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 27}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfig(ctx, gomock.Any()).Return(nil)

	err := e.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: &notifyInterval,
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestOnUpdateWorkerRuntimeConfigFailedNoop(t *testing.T) {
	e := &Executor{}
	err := e.OnUpdateWorkerRuntimeConfigFailed(context.Background(), 1, &taskgen.UpdateWorkerRuntimeConfigParameters{}, nil)
	require.NoError(t, err)
}

func TestExecuteUpdateWorkerRuntimeConfigPropagatesCreateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).
		Return(nil, errors.New("create failed"))

	exec := &Executor{model: mockModel, now: time.Now}
	err := exec.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{})
	require.ErrorContains(t, err, "create worker runtime config")
}

func TestExecuteUpdateWorkerRuntimeConfigPropagatesGetLatestError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).
		Return(&querier.AnclaxWorkerRuntimeConfig{Version: 9}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).
		Return(nil, errors.New("latest failed"))

	exec := &Executor{model: mockModel, now: time.Now}
	err := exec.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{})
	require.ErrorContains(t, err, "get latest runtime config")
}

func TestExecuteUpdateWorkerRuntimeConfigPropagatesListLaggingError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).
		Return(&querier.AnclaxWorkerRuntimeConfig{Version: 10}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).
		Return(&querier.AnclaxWorkerRuntimeConfig{Version: 10}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, gomock.Any()).
		Return(nil, errors.New("list failed"))

	exec := &Executor{model: mockModel, now: time.Now}
	err := exec.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{})
	require.ErrorContains(t, err, "list lagging alive workers")
}

func TestExecuteUpdateWorkerRuntimeConfigPropagatesNotifyError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().CreateWorkerRuntimeConfig(ctx, gomock.Any()).
		Return(&querier.AnclaxWorkerRuntimeConfig{Version: 11}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).
		Return(&querier.AnclaxWorkerRuntimeConfig{Version: 11}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(ctx, gomock.Any()).
		Return([]uuid.UUID{uuid.New()}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfig(ctx, gomock.Any()).
		Return(errors.New("notify failed"))

	exec := &Executor{model: mockModel, now: time.Now}
	err := exec.ExecuteUpdateWorkerRuntimeConfig(ctx, &taskgen.UpdateWorkerRuntimeConfigParameters{})
	require.ErrorContains(t, err, "notify worker runtime config")
}

func TestNormalizeMaxStrictPercentage(t *testing.T) {
	ret, err := normalizeMaxStrictPercentage(nil)
	require.NoError(t, err)
	require.Nil(t, ret)

	v := int32(100)
	ret, err = normalizeMaxStrictPercentage(&v)
	require.NoError(t, err)
	require.NotNil(t, ret)
	require.Equal(t, int32(100), *ret)

	invalid := int32(101)
	_, err = normalizeMaxStrictPercentage(&invalid)
	require.Error(t, err)
}

func TestBuildLabelWeightsValidation(t *testing.T) {
	_, err := buildLabelWeights(&taskgen.UpdateWorkerRuntimeConfigParameters{
		Labels:  []string{"w1"},
		Weights: []int32{0},
	})
	require.Error(t, err)

	_, err = buildLabelWeights(&taskgen.UpdateWorkerRuntimeConfigParameters{
		Labels:  []string{"", "w2"},
		Weights: []int32{1, 1},
	})
	require.Error(t, err)

	defaultWeight := int32(2)
	ret, err := buildLabelWeights(&taskgen.UpdateWorkerRuntimeConfigParameters{
		DefaultWeight: &defaultWeight,
		Labels:        []string{"w1", "w2"},
		Weights:       []int32{5, 1},
	})
	require.NoError(t, err)
	require.Equal(t, int32(2), ret["default"])
	require.Equal(t, int32(5), ret["w1"])
}

func TestParseRuntimeConfigDurationsValidation(t *testing.T) {
	_, _, err := parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: ptr("x"),
	})
	require.Error(t, err)

	zero := "0s"
	_, _, err = parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{
		ListenTimeout: &zero,
	})
	require.Error(t, err)

	ni, lt, err := parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: ptr("3s"),
		ListenTimeout:  ptr("4s"),
	})
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, ni)
	require.Equal(t, 4*time.Second, lt)
}

func TestRuntimeListenDSNFromConfig(t *testing.T) {
	require.Equal(t, "", runtimeListenDSNFromConfig(nil))

	dsn := "postgres://u:p@h:5432/db?sslmode=disable"
	require.Equal(t, dsn, runtimeListenDSNFromConfig(&config.Config{
		Pg: config.Pg{DSN: &dsn},
	}))

	ret := runtimeListenDSNFromConfig(&config.Config{
		Pg: config.Pg{
			User:     "postgres",
			Password: "postgres",
			Host:     "localhost",
			Port:     5432,
			Db:       "postgres",
			SSLMode:  "disable",
		},
	})
	require.Contains(t, ret, "postgres://postgres:postgres@localhost:5432/postgres")
	require.Contains(t, ret, "sslmode=disable")
}

func TestRunUpdateWorkerRuntimeConfigTaskPriorityMaxInt32Sanity(t *testing.T) {
	require.Equal(t, int32(math.MaxInt32), ConfigUpdateTaskPriority)
}

func TestBuildLabelWeightsDuplicateLabelUsesLastValue(t *testing.T) {
	ret, err := buildLabelWeights(&taskgen.UpdateWorkerRuntimeConfigParameters{
		Labels:  []string{"w1", "w2", "w1"},
		Weights: []int32{1, 2, 9},
	})
	require.NoError(t, err)
	require.Equal(t, int32(9), ret["w1"])
	require.Equal(t, int32(2), ret["w2"])
	require.Equal(t, int32(1), ret["default"])
}

func TestParseRuntimeConfigDurationsDefaults(t *testing.T) {
	notifyInterval, listenTimeout, err := parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{})
	require.NoError(t, err)
	require.Equal(t, time.Second, notifyInterval)
	require.Equal(t, 2*time.Second, listenTimeout)
}

func TestRuntimeConfigHeartbeatTTLFromConfig(t *testing.T) {
	require.Equal(t, 9*time.Second, runtimeConfigHeartbeatTTLFromConfig(nil))

	hb := 5 * time.Second
	require.Equal(t, 15*time.Second, runtimeConfigHeartbeatTTLFromConfig(&config.Config{
		Worker: config.Worker{HeartbeatInterval: &hb},
	}))

	invalid := time.Duration(0)
	require.Equal(t, 9*time.Second, runtimeConfigHeartbeatTTLFromConfig(&config.Config{
		Worker: config.Worker{HeartbeatInterval: &invalid},
	}))
}

func TestNormalizeMaxStrictPercentageLowerBound(t *testing.T) {
	valid := int32(0)
	out, err := normalizeMaxStrictPercentage(&valid)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, int32(0), *out)

	invalid := int32(-1)
	_, err = normalizeMaxStrictPercentage(&invalid)
	require.Error(t, err)
}

func TestIsRuntimeConfigAckForRequestEdgeCases(t *testing.T) {
	require.True(t, isRuntimeConfigAckForRequest(`{"params":{"request_id":"r1"}}`, "r1"))
	require.False(t, isRuntimeConfigAckForRequest(`{"op":"up_config","params":{"request_id":"r1"}}`, "r1"))
	require.False(t, isRuntimeConfigAckForRequest(`{"op":"ack","params":{"request_id":"r2"}}`, "r1"))
	require.False(t, isRuntimeConfigAckForRequest(`{"op":"ack","params":{"request_id":"r1"}}`, ""))
}

func TestNewRuntimeConfigAckWaiterWithoutDSNReturnsNil(t *testing.T) {
	require.Nil(t, newRuntimeConfigAckWaiter(""))
}

func ptr(s string) *string {
	return &s
}
