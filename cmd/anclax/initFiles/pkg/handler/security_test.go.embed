package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"myexampleapp/pkg/model"
	"myexampleapp/pkg/zgen/taskgen"

	anclaxutils "github.com/cloudcarver/anclax/pkg/utils"
	"github.com/gofiber/fiber/v3"
	"github.com/pkg/errors"
	"go.uber.org/mock/gomock"
)

func TestCounterDoesNotDiscloseInfrastructureErrors(t *testing.T) {
	const secret = "infrastructure-secret-canary"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, cause := range []error{errors.New(secret), errors.Wrap(fiber.NewError(http.StatusBadRequest, secret), "internal failure")} {
			t.Run(method+"/"+cause.Error(), func(t *testing.T) {
				ctrl := gomock.NewController(t)
				m := model.NewMockModelInterface(ctrl)
				runner := taskgen.NewMockTaskRunner(ctrl)
				h := &Handler{model: m, taskrunner: runner}
				app := fiber.New(fiber.Config{ErrorHandler: anclaxutils.ErrorHandler})
				if method == http.MethodGet {
					m.EXPECT().GetCounter(gomock.Any()).Return(nil, cause)
					app.Get("/counter", h.GetCounter)
				} else {
					runner.EXPECT().RunIncrementCounter(gomock.Any(), gomock.Any()).Return(int32(0), cause)
					app.Post("/counter", h.IncrementCounter)
				}
				resp, err := app.Test(httptest.NewRequest(method, "/counter", nil))
				if err != nil {
					t.Fatalf("request counter: %v", err)
				}
				defer func() {
					if err := resp.Body.Close(); err != nil {
						t.Errorf("close response: %v", err)
					}
				}()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read response: %v", err)
				}
				if resp.StatusCode != fiber.StatusInternalServerError {
					t.Errorf("status = %d, want %d", resp.StatusCode, fiber.StatusInternalServerError)
				}
				if strings.Contains(string(body), secret) {
					t.Errorf("response disclosed infrastructure error: %s", body)
				}
			})
		}
	}
}
