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
	"github.com/cloudcarver/anclax/pkg/taskcore"
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
		HeartbeatCutoff: fixedNow.Add(-10 * time.Second),
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
		HeartbeatCutoff: fixedNow.Add(-10 * time.Second),
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

func TestExecuteUpdateWorkerRuntimeConfigUsesCustomHeartbeatTTL(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	fixedNow := time.Date(2026, 2, 13, 12, 0, 0, 0, time.UTC)
	heartbeatTTL := "30s"
	notifyInterval := "1ms"

	mockModel := model.NewMockModelInterface(ctrl)
	e := &Executor{
		model:      mockModel,
		now:        func() time.Time { return fixedNow },
		waitForAck: nil,
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
		HeartbeatTTL:   &heartbeatTTL,
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
	_, _, _, err := parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: ptr("x"),
	})
	require.Error(t, err)

	zero := "0s"
	_, _, _, err = parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{
		ListenTimeout: &zero,
	})
	require.Error(t, err)

	ni, lt, hb, err := parseRuntimeConfigDurations(&taskgen.UpdateWorkerRuntimeConfigParameters{
		NotifyInterval: ptr("3s"),
		ListenTimeout:  ptr("4s"),
		HeartbeatTTL:   ptr("5s"),
	})
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, ni)
	require.Equal(t, 4*time.Second, lt)
	require.Equal(t, 5*time.Second, hb)
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

func ptr(s string) *string {
	return &s
}
