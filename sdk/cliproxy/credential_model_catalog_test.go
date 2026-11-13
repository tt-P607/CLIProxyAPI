package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCredentialModelCatalogUsesDisabledAntigravitySnapshot(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := antigravityTestAuth("catalog-disabled-account", upstream.URL)
	auth.Metadata["project_id"] = "catalog-project"
	auth.Disabled = true
	auth.Status = coreauth.StatusDisabled
	auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(
		"catalog-project", antigravityModelBaseURLs(auth)[0], antigravityModelCapabilityHints{
			ModelIDs:         map[string]struct{}{"upstream-b": {}, "upstream-a": {}},
			DisabledModelIDs: map[string]struct{}{"upstream-b": {}},
		},
	)
	registeredAuth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	defer GlobalModelRegistry().UnregisterClient(registeredAuth.ID)
	GlobalModelRegistry().RegisterClient(registeredAuth.ID, registeredAuth.Provider, []*ModelInfo{{ID: "runtime-only-model"}})

	source, modelIDs, disabledModelIDs := svc.credentialModelCatalog(registeredAuth)
	if source != "upstream" {
		t.Fatalf("source = %q, want upstream", source)
	}
	if !slices.Equal(modelIDs, []string{"upstream-a", "upstream-b"}) {
		t.Fatalf("model IDs = %v, want only the sorted account catalog", modelIDs)
	}
	if disabledModelIDs == nil {
		t.Fatal("expected persisted disabled flags to be known")
	}
	if _, exists := disabledModelIDs["upstream-b"]; !exists {
		t.Fatal("disabled upstream model was not preserved")
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("catalog lookup made %d upstream calls, want 0", calls)
	}
}

func TestAntigravityCapabilityHintsCloneDisabledModelIDs(t *testing.T) {
	hints := antigravityModelCapabilityHints{
		ModelIDs:         map[string]struct{}{"model": {}},
		DisabledModelIDs: map[string]struct{}{"model": {}},
	}
	cloned := hints.clone()
	delete(hints.DisabledModelIDs, "model")
	if _, exists := cloned.DisabledModelIDs["model"]; !exists {
		t.Fatal("clone did not preserve an independent disabled model set")
	}
	legacy := (antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"legacy": {}}}).clone()
	if legacy.DisabledModelIDs != nil {
		t.Fatalf("clone changed unknown legacy flags to known: %v", legacy.DisabledModelIDs)
	}
}

func TestCredentialModelCatalogDistinguishesKnownEmptyFromUnknown(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	knownEmpty := antigravityTestAuth("catalog-known-empty", "http://127.0.0.1:1")
	knownEmpty.Metadata["project_id"] = "known-empty-project"
	knownEmpty.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(
		"known-empty-project", antigravityModelBaseURLs(knownEmpty)[0], antigravityModelCapabilityHints{
			ModelIDs:         map[string]struct{}{},
			DisabledModelIDs: map[string]struct{}{},
		},
	)
	knownEmpty, errRegister := manager.Register(t.Context(), knownEmpty)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	unknown := antigravityTestAuth("catalog-unknown", "http://127.0.0.1:1")
	unknown, errRegister = manager.Register(t.Context(), unknown)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	defer GlobalModelRegistry().UnregisterClient(knownEmpty.ID)
	defer GlobalModelRegistry().UnregisterClient(unknown.ID)
	GlobalModelRegistry().RegisterClient(knownEmpty.ID, knownEmpty.Provider, []*ModelInfo{{ID: "static-or-runtime-model"}})
	GlobalModelRegistry().RegisterClient(unknown.ID, unknown.Provider, []*ModelInfo{{ID: "static-or-runtime-model"}})

	knownSource, knownModels, knownDisabled := svc.credentialModelCatalog(knownEmpty)
	if knownSource != "upstream" || knownModels == nil || len(knownModels) != 0 {
		t.Fatalf("known empty catalog = (%q, %v), want upstream with an empty array", knownSource, knownModels)
	}
	if knownDisabled == nil || len(knownDisabled) != 0 {
		t.Fatalf("known empty disabled set = %v, want a known empty set", knownDisabled)
	}
	unknownSource, unknownModels, unknownDisabled := svc.credentialModelCatalog(unknown)
	if unknownSource != "unknown" || len(unknownModels) != 0 || unknownDisabled != nil {
		t.Fatalf("unknown catalog = (%q, %v, %v), want unknown with no models or flags", unknownSource, unknownModels, unknownDisabled)
	}
}

func TestCredentialModelCatalogUsesRegisteredFallbackForUnsupportedProviders(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := &coreauth.Auth{ID: "catalog-registered-provider", Provider: "openai"}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	defer GlobalModelRegistry().UnregisterClient(auth.ID)
	GlobalModelRegistry().RegisterClient(auth.ID, auth.Provider, []*ModelInfo{{ID: "registered-z"}, {ID: "registered-a"}})

	source, modelIDs, disabledModelIDs := svc.credentialModelCatalog(auth)
	if source != "registered" || !slices.Equal(modelIDs, []string{"registered-a", "registered-z"}) {
		t.Fatalf("registered fallback = (%q, %v), want sorted runtime models", source, modelIDs)
	}
	if disabledModelIDs != nil {
		t.Fatalf("registered fallback must not claim upstream disabled flags: %v", disabledModelIDs)
	}

	withoutModels := &coreauth.Auth{ID: "catalog-no-registered-models", Provider: "openai"}
	withoutModels, errRegister = manager.Register(t.Context(), withoutModels)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	noSource, noModels, _ := svc.credentialModelCatalog(withoutModels)
	if noSource != "unknown" || len(noModels) != 0 {
		t.Fatalf("empty registered fallback = (%q, %v), want unknown with no models", noSource, noModels)
	}
}

func TestBuilderWiresCredentialModelCatalogIntoV8Server(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "builder-management-key")
	manager := coreauth.NewManager(nil, nil, nil)
	registeredAuth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
		ID:       "builder-catalog-auth",
		Provider: "openai",
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	defer GlobalModelRegistry().UnregisterClient(registeredAuth.ID)
	GlobalModelRegistry().RegisterClient(registeredAuth.ID, registeredAuth.Provider, []*ModelInfo{{ID: "builder-registered-model"}})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{AuthDir: t.TempDir(), LoggingToFile: false}).
		WithConfigPath(configPath).
		WithCoreAuthManager(manager).
		WithServerOptions(api.WithRequestLoggerFactory(nil)).
		Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v8/management/credentials/model-catalogs", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Management-Key", "builder-management-key")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "builder-management-key") {
		t.Fatalf("response leaked management key: %s", recorder.Body.String())
	}
	doc := ojson.ParseBytes(recorder.Body.Bytes())
	catalogs := doc.Get("catalogs")
	if !catalogs.IsArray() || len(catalogs.Array()) != 1 {
		t.Fatalf("catalogs = %s, want a one-entry array", recorder.Body.String())
	}
	entry := catalogs.Array()[0]
	if entry.Get("source").Str != "registered" {
		t.Fatalf("source = %q, want registered", entry.Get("source").Str)
	}
	models := entry.Get("models")
	if !models.IsArray() || len(models.Array()) != 1 || models.Array()[0].Get("id").Str != "builder-registered-model" {
		t.Fatalf("models = %s, want builder-registered-model", recorder.Body.String())
	}
}
