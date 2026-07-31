package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"myexampleapp/pkg/model"

	anclaxutils "github.com/cloudcarver/anclax/pkg/utils"
	"github.com/gofiber/fiber/v3"
	"github.com/pkg/errors"
	"go.uber.org/mock/gomock"
)

func TestGetCounterDoesNotReturnDatabaseErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterface(ctrl)
	m.EXPECT().GetCounter(gomock.Any()).Return(nil, errors.New("database-secret-canary"))

	h := &Handler{model: m}
	app := fiber.New(fiber.Config{ErrorHandler: anclaxutils.ErrorHandler})
	app.Get("/counter", h.GetCounter)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/counter", nil))
	if err != nil {
		t.Fatalf("request counter: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusInternalServerError)
	}
	if strings.Contains(string(body), "database-secret-canary") {
		t.Fatalf("response disclosed database error: %s", body)
	}
}
