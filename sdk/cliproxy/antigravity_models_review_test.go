package cliproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityAuthModelCatalogUsesValidatedSnapshots(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	for _, test := range []struct {
		name          string
		persisted     bool
		disabled      bool
		wrongProject  bool
		wrongEndpoint bool
		models        map[string]struct{}
		wantKnown     bool
	}{
		{name: "unknown"},
		{name: "empty", persisted: true, models: map[string]struct{}{}, wantKnown: true},
		{name: "known", persisted: true, models: map[string]struct{}{"upstream-z": {}, "upstream-a": {}}, wantKnown: true},
		{name: "disabled_snapshot", persisted: true, disabled: true, models: map[string]struct{}{"upstream-a": {}}, wantKnown: true},
		{name: "other_project", persisted: true, wrongProject: true, models: map[string]struct{}{"upstream-a": {}}},
		{name: "other_endpoint", persisted: true, wrongEndpoint: true, models: map[string]struct{}{"upstream-a": {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := antigravityTestAuth("catalog-view-"+test.name, "http://127.0.0.1:1")
			auth.Metadata["project_id"] = "catalog-project"
			auth.Disabled = test.disabled
			if test.persisted {
				project, endpoint := "catalog-project", antigravityModelBaseURLs(auth)[0]
				if test.wrongProject {
					project = "other-project"
				}
				if test.wrongEndpoint {
					endpoint = "http://127.0.0.1:2"
				}
				auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(project, endpoint, antigravityModelCapabilityHints{ModelIDs: test.models})
			}
			manager := coreauth.NewManager(nil, nil, nil)
			current, errRegister := manager.Register(t.Context(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			svc := &Service{cfg: &config.Config{}, coreManager: manager}
			models, known := svc.authModelCatalog(current)
			if known != test.wantKnown {
				t.Fatalf("known=%v, want %v", known, test.wantKnown)
			}
			if known && len(models) != len(test.models) {
				t.Fatalf("models=%v, want %v", models, test.models)
			}
			if !slices.IsSorted(models) {
				t.Fatalf("catalog must be sorted: %v", models)
			}
			if len(models) > 0 {
				models[0] = "mutated"
				fresh, _ := svc.authModelCatalog(current)
				if slices.Contains(fresh, "mutated") {
					t.Fatal("model view mutated the cached catalog")
				}
			}
		})
	}
}

func TestAntigravityStaleAuthCannotRestoreExcludedModel(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth, _ := svc.coreManager.Register(t.Context(), antigravityTestAuth("stale-auth-models", "http://127.0.0.1:1"))
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	const model = "gemini-3.1-flash-lite"
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{model: {}}, revision: 1}
	key := svc.antigravityCapabilityKey(auth)
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: hints, expiresAt: time.Now().Add(antigravityCapabilityCacheTTL)}
	antigravityCapabilityMu.Unlock()
	oldEpoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)
	updated := auth.Clone()
	updated.Attributes["excluded_models"] = model
	updated, _ = svc.coreManager.Update(t.Context(), updated)
	svc.registerModelsForAuth(t.Context(), updated)
	svc.WaitAntigravityProbes()
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, oldEpoch)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
		t.Fatal("stale publisher reinstated excluded model")
	}
	// Even when a stale queued task captures the new registry epoch it must not
	// publish the old account-level model settings.
	currentEpoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, currentEpoch)
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
		t.Fatal("stale auth accepted with current registry epoch")
	}
}

func TestAntigravityRemovedAccountsReleaseProbeSlots(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	started := make(chan struct{}, modelRegistrationMaxWorkersPerCategory)
	healthy := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer token-healthy" {
			healthy <- struct{}{}
			_, _ = w.Write([]byte(`{"models":{}}`))
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	t.Cleanup(func() { cancel(); svc.WaitAntigravityProbes(); server.Close() })
	for i := range modelRegistrationMaxWorkersPerCategory {
		auth, _ := svc.coreManager.Register(ctx, antigravityTestAuth(fmt.Sprint("removed-", i), server.URL))
		svc.registerModelsForAuth(ctx, auth)
	}
	for range modelRegistrationMaxWorkersPerCategory {
		<-started
	}
	for i := range modelRegistrationMaxWorkersPerCategory {
		svc.applyCoreAuthRemoval(ctx, fmt.Sprint("removed-", i))
	}
	auth, _ := svc.coreManager.Register(ctx, antigravityTestAuth("healthy", server.URL))
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(ctx, auth)
	select {
	case <-healthy:
	case <-time.After(time.Second):
		t.Fatal("removed accounts still occupy all probe slots")
	}
}

type antigravityReviewTransport func(*http.Request) (*http.Response, error)

func (f antigravityReviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAntigravityRetrySchedulerPreservesJitterDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
		start := time.Now()
		type observation struct {
			id      string
			elapsed time.Duration
		}
		observed := make(chan observation, 2)
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(r *http.Request) (*http.Response, error) {
			observed <- observation{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer token-"), time.Since(start)}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":{}}`)), Request: r}, nil
		})
		defer func() { http.DefaultTransport = transport }()
		deadlines := map[string]time.Duration{"early": 70 * time.Second, "late": 110 * time.Second}
		for id, deadline := range deadlines {
			auth, _ := svc.coreManager.Register(t.Context(), antigravityTestAuth(id, "https://daily.invalid"))
			defer GlobalModelRegistry().UnregisterClient(auth.ID)
			key := svc.antigravityCapabilityKey(auth) + fmt.Sprintf("/%x", sha256.Sum256([]byte("token-"+id)))
			antigravityCapabilityMu.Lock()
			antigravityAuthFailureCache[key] = antigravityFailureState{failures: 1, lastFailure: start, nextRetry: start.Add(deadline)}
			antigravityCapabilityMu.Unlock()
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); svc.runAntigravityModelRefresh(ctx) }()
		synctest.Wait()
		time.Sleep(130 * time.Second) // Synthetic time: exercise actual scheduler, not just delay arithmetic.
		synctest.Wait()
		cancel()
		<-done
		if len(observed) != 2 {
			t.Fatalf("observed %d retries", len(observed))
		}
		for range 2 {
			got := <-observed
			if got.elapsed != deadlines[got.id] {
				t.Errorf("%s retried at %s, want %s", got.id, got.elapsed, deadlines[got.id])
			}
		}
	})
}
