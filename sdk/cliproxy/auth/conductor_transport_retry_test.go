package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"testing"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func windowsCodexTLSHandshakeError() error {
	return &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: fmt.Errorf("tls: TLS handshake: %w", &net.OpError{
			Op:     "read",
			Net:    "tcp",
			Source: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
			Addr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2},
			Err:    errors.New("wsarecv: A connection attempt failed because the connected party did not properly respond after a period of time, or established connection failed because connected host has failed to respond."),
		}),
	}
}

func dialRefusedError() error {
	return &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
}

func TestManager_ShouldRetryAfterError_RetriesPreHTTPTransportFailure(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 0, 0)
	model := "gpt-transport-retry-" + uuid.NewString()
	authID := "transport-retry-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "windows tls handshake", err: windowsCodexTLSHandshakeError(), want: true},
		{name: "dial refused", err: dialRefusedError(), want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "unauthorized", err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "certificate", err: &url.Error{Op: "Post", URL: "https://chatgpt.com/backend-api/codex/responses", Err: x509.UnknownAuthorityError{}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, shouldRetry := m.shouldRetryAfterError(tc.err, 0, []string{"codex"}, model, 0)
			if shouldRetry != tc.want {
				t.Fatalf("shouldRetryAfterError() = (%v, %t), want retry %t", wait, shouldRetry, tc.want)
			}
			if tc.want && wait != 0 {
				t.Fatalf("shouldRetryAfterError() wait = %v, want 0", wait)
			}
			if tc.want {
				if _, shouldRetry = m.shouldRetryAfterError(tc.err, 1, []string{"codex"}, model, 0); shouldRetry {
					t.Fatal("transport retried after the configured additional round")
				}
			}
		})
	}
}

func TestManager_MarkResult_PreHTTPTransportFailureDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	model := "gpt-6-astra"
	cases := []struct {
		name string
		err  *Error
	}{
		{name: "typed tls handshake", err: resultErrorFromError(windowsCodexTLSHandshakeError())},
		{name: "connection reset message", err: &Error{Message: "connection reset"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-transport-" + uuid.NewString(), Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    tc.err,
			})
			assertNoCooldown(t, m, auth.ID, model)
		})
	}
}

func TestExecuteRetriesPreHTTPTransportFailureWithoutCooling(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(1, 0, 0)
	executor := &transportThenSuccessExecutor{
		identifier: "codex",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager.RegisterExecutor(executor)

	model := "gpt-6-astra-" + uuid.NewString()
	authID := "codex-transport-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success after transport retry", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
	assertNoCooldown(t, manager, authID, model)
}

func TestExecuteDoesNotPoisonCredentialOnPreHTTPTransportFailure(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	executor := &transportThenSuccessExecutor{
		identifier: "codex",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager.RegisterExecutor(executor)

	model := "gpt-6-astra-" + uuid.NewString()
	authID := "codex-transport-poison-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	req := cliproxyexecutor.Request{Model: model}
	if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("first Execute() error = nil, want transport failure")
	} else if shouldRetrySchedulerPick(errExecute) {
		t.Fatalf("first Execute() = %v, want raw transport error rather than auth_unavailable", errExecute)
	}
	assertNoCooldown(t, manager, authID, model)

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("second Execute() error = %v, want success on the still-available credential", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("second Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
}

func TestHomeExecuteRetriesPreHTTPTransportFailure(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a"}}
	executor := &transportThenSuccessExecutor{
		identifier: "home-retry-contract",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, 0, 0)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	resp, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success after Home transport retry", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
}

type transportThenSuccessExecutor struct {
	identifier string
	fail       error

	mu    sync.Mutex
	calls int
}

func (e *transportThenSuccessExecutor) Identifier() string { return e.identifier }

func (e *transportThenSuccessExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, e.fail
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *transportThenSuccessExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.recordCall() == 1 {
		return nil, e.fail
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*transportThenSuccessExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *transportThenSuccessExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, e.fail
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*transportThenSuccessExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *transportThenSuccessExecutor) recordCall() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.calls
}

func (e *transportThenSuccessExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type affinityRetryOutcome struct {
	err    error
	chunks []cliproxyexecutor.StreamChunk
}

type affinityRetryExecutor struct {
	mu         sync.Mutex
	identifier string
	outcomes   []affinityRetryOutcome
	authIDs    []string
}

func (e *affinityRetryExecutor) Identifier() string { return e.identifier }

func (e *affinityRetryExecutor) next(auth *Auth) affinityRetryOutcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.authIDs = append(e.authIDs, auth.ID)
	index := len(e.authIDs) - 1
	if index >= len(e.outcomes) {
		return affinityRetryOutcome{}
	}
	return e.outcomes[index]
}

func (e *affinityRetryExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	outcome := e.next(auth)
	if outcome.err != nil {
		return cliproxyexecutor.Response{}, outcome.err
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *affinityRetryExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	outcome := e.next(auth)
	if outcome.err != nil {
		return nil, outcome.err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, len(outcome.chunks))
	for _, chunk := range outcome.chunks {
		chunks <- chunk
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*affinityRetryExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *affinityRetryExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (*affinityRetryExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *affinityRetryExecutor) attemptedAuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

type boundAuthPluginScheduler struct {
	boundSource string
	requests    []pluginapi.SchedulerPickRequest
}

func (*boundAuthPluginScheduler) SchedulerWantsAcrossPriorities() bool { return true }

func (s *boundAuthPluginScheduler) PickAuth(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	s.requests = append(s.requests, req)
	for _, candidate := range req.Candidates {
		if candidate.Attributes["source"] == s.boundSource {
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: candidate.ID}, true, nil
		}
	}
	return pluginapi.SchedulerPickResponse{}, true, &Error{
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "Deadline expired before operation could complete",
	}
}

func newAffinityRetryManager(t *testing.T, retryRounds int, outcomes ...affinityRetryOutcome) (*Manager, *affinityRetryExecutor, *boundAuthPluginScheduler, string) {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	provider := "antigravity"
	model := "gemini-3.8-flash-" + uuid.NewString()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(retryRounds, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			provider: {{
				Status: http.StatusServiceUnavailable,
				Match:  []string{"Deadline expired before operation could complete"},
				Action: RequestScopedActionContinue,
			}},
		},
	})
	registerRetryRoundLocalAuths(t, manager, provider, model, map[string]int{"auth-a": retryRounds, "auth-b": retryRounds})
	for _, auth := range []*Auth{
		{ID: "auth-a", Provider: provider, Status: StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "source": "bound-A"}},
		{ID: "auth-b", Provider: provider, Status: StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "source": "other-B"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	executor := &affinityRetryExecutor{identifier: provider, outcomes: outcomes}
	manager.RegisterExecutor(executor)
	scheduler := &boundAuthPluginScheduler{boundSource: "bound-A"}
	manager.SetPluginScheduler(scheduler)
	return manager, executor, scheduler, model
}

func affinityRetryOptions(sessionID string, stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Stream: stream, Headers: http.Header{"X-Session-Id": []string{sessionID}}}
}

func assertAffinityAuthIDs(t *testing.T, executor *affinityRetryExecutor, want ...string) {
	t.Helper()
	got := executor.attemptedAuthIDs()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("attempted auth IDs = %v, want %v", got, want)
	}
}

func assertAffinitySchedulerInputs(t *testing.T, scheduler *boundAuthPluginScheduler, sessionID string) {
	t.Helper()
	if len(scheduler.requests) == 0 {
		t.Fatal("plugin scheduler was not called")
	}
	for _, request := range scheduler.requests {
		if got := http.Header(request.Options.Headers).Get("X-Session-ID"); got != sessionID {
			t.Fatalf("scheduler X-Session-ID = %q, want %q", got, sessionID)
		}
	}
}

func TestExecutePluginAffinityVetoPreservesBoundAuthAcrossRetryRounds(t *testing.T) {
	const sessionID = "affinity-regression-session"
	deadlineErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "Deadline expired before operation could complete"}

	t.Run("503 then success", func(t *testing.T) {
		manager, executor, scheduler, model := newAffinityRetryManager(t, 1,
			affinityRetryOutcome{err: deadlineErr}, affinityRetryOutcome{})
		resp, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, false))
		if errExecute != nil || string(resp.Payload) != "ok" {
			t.Fatalf("Execute() = (%q, %v), want success", resp.Payload, errExecute)
		}
		assertAffinityAuthIDs(t, executor, "auth-a", "auth-a")
		assertAffinitySchedulerInputs(t, scheduler, sessionID)
		assertNoCooldown(t, manager, "auth-a", model)
	})

	t.Run("ordinary EOF then success", func(t *testing.T) {
		manager, executor, scheduler, model := newAffinityRetryManager(t, 1,
			affinityRetryOutcome{err: io.ErrUnexpectedEOF}, affinityRetryOutcome{})
		if _, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, false)); errExecute != nil {
			t.Fatalf("Execute() error = %v, want success", errExecute)
		}
		assertAffinityAuthIDs(t, executor, "auth-a", "auth-a")
		assertAffinitySchedulerInputs(t, scheduler, sessionID)
		assertNoCooldown(t, manager, "auth-a", model)
	})

	t.Run("retry budget preserves upstream 503 and next request binding", func(t *testing.T) {
		manager, executor, scheduler, model := newAffinityRetryManager(t, 2,
			affinityRetryOutcome{err: deadlineErr}, affinityRetryOutcome{err: deadlineErr},
			affinityRetryOutcome{err: deadlineErr}, affinityRetryOutcome{})
		_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, false))
		var statusErr interface{ StatusCode() int }
		if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != http.StatusServiceUnavailable || errExecute.Error() != deadlineErr.Error() {
			t.Fatalf("exhausted Execute() error = %v, want original upstream 503", errExecute)
		}
		assertAffinityAuthIDs(t, executor, "auth-a", "auth-a", "auth-a")
		assertNoCooldown(t, manager, "auth-a", model)

		if _, errNext := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, false)); errNext != nil {
			t.Fatalf("next Execute() error = %v, want success", errNext)
		}
		assertAffinityAuthIDs(t, executor, "auth-a", "auth-a", "auth-a", "auth-a")
		assertAffinitySchedulerInputs(t, scheduler, sessionID)
	})

	for _, terminalErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(terminalErr.Error()+" prevents replay", func(t *testing.T) {
			manager, executor, _, model := newAffinityRetryManager(t, 1, affinityRetryOutcome{err: terminalErr})
			_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, false))
			if !errors.Is(errExecute, terminalErr) {
				t.Fatalf("Execute() error = %v, want %v", errExecute, terminalErr)
			}
			assertAffinityAuthIDs(t, executor, "auth-a")
		})
	}
}

func TestExecuteStreamPluginAffinityVetoBootstrapAndDeliveredChunk(t *testing.T) {
	const sessionID = "affinity-stream-regression-session"
	bootstrapErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "Deadline expired before operation could complete"}

	t.Run("bootstrap error then success", func(t *testing.T) {
		manager, executor, scheduler, model := newAffinityRetryManager(t, 1,
			affinityRetryOutcome{chunks: []cliproxyexecutor.StreamChunk{{Err: bootstrapErr}}},
			affinityRetryOutcome{chunks: []cliproxyexecutor.StreamChunk{{Payload: []byte("ok")}}})
		result, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, true))
		if errStream != nil || result == nil {
			t.Fatalf("ExecuteStream() = (%v, %v), want successful stream", result, errStream)
		}
		chunk := <-result.Chunks
		if string(chunk.Payload) != "ok" || chunk.Err != nil {
			t.Fatalf("stream chunk = (%q, %v), want ok", chunk.Payload, chunk.Err)
		}
		assertAffinityAuthIDs(t, executor, "auth-a", "auth-a")
		assertAffinitySchedulerInputs(t, scheduler, sessionID)
		assertNoCooldown(t, manager, "auth-a", model)
	})

	t.Run("delivered chunk error does not replay", func(t *testing.T) {
		streamErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "Deadline expired before operation could complete"}
		manager, executor, scheduler, model := newAffinityRetryManager(t, 1,
			affinityRetryOutcome{chunks: []cliproxyexecutor.StreamChunk{{Payload: []byte("first")}, {Err: streamErr}}})
		result, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, affinityRetryOptions(sessionID, true))
		if errStream != nil || result == nil {
			t.Fatalf("ExecuteStream() = (%v, %v), want stream before terminal chunk error", result, errStream)
		}
		first := <-result.Chunks
		last := <-result.Chunks
		if string(first.Payload) != "first" || !errors.Is(last.Err, streamErr) {
			t.Fatalf("stream chunks = (%q, %v), (%q, %v), want delivered payload then original error", first.Payload, first.Err, last.Payload, last.Err)
		}
		assertAffinityAuthIDs(t, executor, "auth-a")
		assertAffinitySchedulerInputs(t, scheduler, sessionID)
	})
}
