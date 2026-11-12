package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type credentialTestScheduler struct{}

func (credentialTestScheduler) PickAuth(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	if req.Diagnostic {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	return pluginapi.SchedulerPickResponse{
		Handled: true, Reject: true, RejectCode: "quota_account_unavailable", RejectReason: "ordinary requests remain isolated",
	}, true, nil
}

type credentialTestExecutor struct {
	failure error
	called  []string
	models  []string
}

func (*credentialTestExecutor) Identifier() string { return "antigravity" }

func (executor *credentialTestExecutor) Execute(_ context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	executor.called = append(executor.called, auth.ID)
	executor.models = append(executor.models, req.Model)
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[]}`)}, executor.failure
}

func (*credentialTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, fmt.Errorf("unexpected streaming credential test")
}

func (*credentialTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*credentialTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("unexpected token-count credential test")
}

func (*credentialTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected raw credential request")
}

func TestAuthFileTestPreservesDiagnosticErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		reason   string
		disabled bool
	}{
		{name: "forbidden", status: 403, reason: "PERMISSION_DENIED"},
		{name: "unauthorized", status: 401, reason: "UNAUTHENTICATED"},
		{name: "quota", status: 429, reason: "QUOTA_EXHAUSTED"},
		{name: "rate_limit", status: 429, reason: "RATE_LIMIT_EXCEEDED"},
		{name: "invalid_request", status: 400, reason: "INVALID_ARGUMENT"},
		{name: "success", status: 200},
		{name: "disabled_success", status: 200, disabled: true},
		{name: "disabled_unauthorized", status: 401, reason: "UNAUTHENTICATED", disabled: true},
		{name: "disabled_forbidden", status: 403, reason: "PERMISSION_DENIED", disabled: true},
		{name: "disabled_quota", status: 429, reason: "QUOTA_EXHAUSTED", disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 1)
			manager.SetPluginScheduler(credentialTestScheduler{})
			executor := &credentialTestExecutor{}
			if test.status != 200 {
				executor.failure = &coreauth.Error{HTTPStatus: test.status, Message: fmt.Sprintf(`{"error":{"code":%d,"message":"private diagnostic marker","details":[{"reason":%q}]}}`, test.status, test.reason)}
			}
			manager.RegisterExecutor(executor)
			model := "credential-diagnostics-" + test.name
			selectedID := model + "-selected.json"
			for _, authID := range []string{selectedID, model + "-other.json"} {
				registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				auth := &coreauth.Auth{ID: authID, FileName: authID, Provider: executor.Identifier()}
				if authID == selectedID && test.disabled {
					auth.Disabled = true
					auth.Status = coreauth.StatusDisabled
				}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			if test.disabled {
				delay := time.Hour
				manager.MarkResult(context.Background(), coreauth.Result{AuthID: selectedID, Provider: executor.Identifier(), Model: model, Error: &coreauth.Error{HTTPStatus: 429, Message: "previous upstream cooldown"}, RetryAfter: &delay})
			}
			before, _ := manager.GetByID(selectedID)
			if test.disabled && (before.ModelStates[model] == nil || !before.ModelStates[model].NextRetryAfter.After(time.Now())) {
				t.Fatal("fixture must have an active model cooldown")
			}
			handler := &Handler{
				authManager:    manager,
				cfg:            &config.Config{},
				envSecret:      "diagnostic-test-secret",
				failedAttempts: make(map[string]*attemptInfo),
			}
			router := gin.New()
			router.POST("/v8/management/credentials/test", handler.Middleware(), handler.TestAuthFile)
			unauthorized := httptest.NewRecorder()
			unauthorizedRequest := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/test?name="+selectedID, nil)
			unauthorizedRequest.RemoteAddr = "127.0.0.1:12345"
			router.ServeHTTP(unauthorized, unauthorizedRequest)
			if unauthorized.Code != http.StatusUnauthorized || len(executor.called) != 0 || strings.Contains(unauthorized.Body.String(), "private diagnostic marker") {
				t.Fatalf("unauthorized diagnostic status=%d calls=%v", unauthorized.Code, executor.called)
			}
			invoke := func() struct {
				StatusCode  int    `json:"status_code"`
				Error       string `json:"error"`
				Message     string `json:"message"`
				ErrorSource string `json:"error_source"`
			} {
				t.Helper()
				recorder := httptest.NewRecorder()
				payload := fmt.Sprintf(`{"model":%q}`, model)
				if test.name == "disabled_success" {
					payload = "{}"
				}
				request := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/test?name="+selectedID, strings.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Management-Key", "diagnostic-test-secret")
				request.RemoteAddr = "127.0.0.1:12345"
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("management HTTP status=%d, body=%s", recorder.Code, recorder.Body.String())
				}
				var body struct {
					StatusCode  int    `json:"status_code"`
					Error       string `json:"error"`
					Message     string `json:"message"`
					ErrorSource string `json:"error_source"`
				}
				if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
					t.Fatal(errDecode)
				}
				return body
			}
			body := invoke()
			if body.StatusCode != test.status {
				t.Fatalf("diagnostic status=%d error=%s, want %d", body.StatusCode, body.Error, test.status)
			}
			if len(executor.called) != 1 || executor.called[0] != selectedID {
				t.Fatalf("diagnostic called=%v, want selected credential exactly once", executor.called)
			}
			if executor.models[0] != model {
				t.Fatalf("test model=%s, want credential's registered model %s", executor.models[0], model)
			}
			if test.disabled {
				current, _ := manager.GetByID(selectedID)
				if current == nil || !current.Disabled || current.Status != before.Status || !current.NextRetryAfter.Equal(before.NextRetryAfter) || !current.ModelStates[model].NextRetryAfter.Equal(before.ModelStates[model].NextRetryAfter) {
					t.Fatalf("diagnostic changed disabled/cooldown state: %+v", current)
				}
			}
			if executor.failure == nil {
				if body.Error != "" {
					t.Fatalf("successful diagnostic error=%q", body.Error)
				}
				return
			}
			if body.Error != executor.failure.Error() {
				t.Fatalf("diagnostic error=%s, want original %s", body.Error, executor.failure)
			}
			if test.status == 400 {
				return
			}
			cached := invoke()
			if len(executor.called) != 2 || cached.StatusCode != test.status || cached.Error != executor.failure.Error() || cached.ErrorSource == "local_cooldown" {
				t.Fatalf("repeat diagnostic called=%v result=%+v, want a fresh upstream test", executor.called, cached)
			}
			_, publicErr := manager.Execute(context.Background(), []string{executor.Identifier()}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: selectedID}})
			if publicErr == nil || strings.Contains(publicErr.Error(), "private diagnostic marker") || strings.Contains(publicErr.Error(), test.reason) {
				t.Fatalf("public execution leaked diagnostic details: %v", publicErr)
			}
		})
	}
}
