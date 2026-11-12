package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type credentialTestExecutor struct {
	failure error
	called  []string
}

func (*credentialTestExecutor) Identifier() string { return "antigravity" }

func (executor *credentialTestExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	executor.called = append(executor.called, auth.ID)
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
		name   string
		status int
		reason string
	}{
		{name: "forbidden", status: 403, reason: "PERMISSION_DENIED"},
		{name: "quota", status: 429, reason: "QUOTA_EXHAUSTED"},
		{name: "rate_limit", status: 429, reason: "RATE_LIMIT_EXCEEDED"},
		{name: "invalid_request", status: 400, reason: "INVALID_ARGUMENT"},
		{name: "success", status: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 1)
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
				if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, FileName: authID, Provider: executor.Identifier()}); errRegister != nil {
					t.Fatal(errRegister)
				}
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
				request := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/test?name="+selectedID, strings.NewReader(fmt.Sprintf(`{"model":%q}`, model)))
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
			if len(executor.called) != 1 || !strings.Contains(cached.Error, "private diagnostic marker") {
				t.Fatalf("cached diagnostic called=%v error=%s", executor.called, cached.Error)
			}
			if test.status == 429 && (cached.StatusCode != 429 || cached.ErrorSource != "local_cooldown" || !strings.Contains(cached.Error, "model_cooldown")) {
				t.Fatalf("cached diagnostic=%+v, want explicit local cooldown and original cause", cached)
			}
			_, publicErr := manager.Execute(context.Background(), []string{executor.Identifier()}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: selectedID}})
			if publicErr == nil || strings.Contains(publicErr.Error(), "private diagnostic marker") || strings.Contains(publicErr.Error(), test.reason) {
				t.Fatalf("public execution leaked diagnostic details: %v", publicErr)
			}
		})
	}
}
