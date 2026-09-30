package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"myexampleapp/pkg/model"
	"myexampleapp/pkg/zgen/apigen"
	"myexampleapp/pkg/zgen/querier"
	"myexampleapp/pkg/zgen/schemas/counter"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/mock/gomock"
)

func TestCounterAPI(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterface(ctrl)
	runner := taskgen.NewMockTaskRunner(ctrl)
	h, err := NewHandler(m, runner)
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	apigen.RegisterHandlers(app, h)

	m.EXPECT().GetCounter(gomock.Any()).Return(&querier.Counter{ID: 1, Value: 7}, nil)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/counter", nil))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := apigen.ParseGetCounterResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.JSON200 == nil || parsed.JSON200.Count != 7 {
		t.Fatalf("response does not match the generated client contract: %s", parsed.Body)
	}

	runner.EXPECT().RunIncrementCounter(gomock.Any(), &counter.IncrementCounterParams{Amount: 1}).Return(int32(42), nil)
	resp, err = app.Test(httptest.NewRequest(http.MethodPost, "/counter", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want 202", resp.StatusCode)
	}
}
