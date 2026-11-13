package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestV8CredentialModelCatalogsRouteAndAuthentication(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	var resolverCalls int
	server := newTestServerWithOptions(t,
		WithCredentialModelCatalogResolver(func(auth *coreauth.Auth) (string, []string, map[string]struct{}) {
			resolverCalls++
			if auth.ID == "catalog-route-legacy" {
				return "upstream", []string{"legacy-model"}, nil
			}
			if auth.ID != "catalog-route-auth" {
				t.Errorf("resolver auth ID = %q, want catalog-route-auth", auth.ID)
			}
			return "upstream", []string{"model-z", "model-a"}, map[string]struct{}{"model-z": {}}
		}),
	)

	manager := coreauth.NewManager(nil, nil, nil)
	registeredAuth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
		ID:       "catalog-route-auth",
		FileName: "account.json",
		Provider: "antigravity",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		Metadata: map[string]any{
			"access_token": "credential-secret-token",
			"email":        "private@example.com",
			"project_id":   "private-project",
		},
		Attributes: map[string]string{"path": "private/path/account.json"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	_, errRegister = manager.Register(t.Context(), &coreauth.Auth{
		ID:       "catalog-route-legacy",
		FileName: "legacy.json",
		Provider: "antigravity",
		Metadata: map[string]any{"email": "legacy-private@example.com"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	server.mgmt.SetAuthManager(manager)

	request := httptest.NewRequest(http.MethodGet, "/v8/management/credentials/model-catalogs", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	unauthorized := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s", unauthorized.Code, http.StatusUnauthorized, unauthorized.Body.String())
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver called before authentication: %d", resolverCalls)
	}

	request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials/model-catalogs", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Management-Key", "test-management-key")
	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if resolverCalls != 2 {
		t.Fatalf("resolver calls = %d, want 2", resolverCalls)
	}
	for _, secret := range []string{"credential-secret-token", "private@example.com", "legacy-private@example.com", "private-project", "private/path/account.json"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response leaked credential metadata %q: %s", secret, response.Body.String())
		}
	}

	doc := ojson.ParseBytes(response.Body.Bytes())
	catalogs := doc.Get("catalogs")
	if !catalogs.IsArray() || len(catalogs.Array()) != 2 {
		t.Fatalf("catalogs = %s, want a two-entry array", response.Body.String())
	}
	entry := catalogs.Array()[0]
	if got := entry.Get("name").Str; got != "account.json" {
		t.Errorf("name = %q, want account.json", got)
	}
	if got := entry.Get("auth_index").Str; got != registeredAuth.EnsureIndex() {
		t.Errorf("auth_index = %q, want %q", got, registeredAuth.EnsureIndex())
	}
	if got := entry.Get("provider").Str; got != "antigravity" {
		t.Errorf("provider = %q, want antigravity", got)
	}
	if got := entry.Get("source").Str; got != "upstream" {
		t.Errorf("source = %q, want upstream", got)
	}
	models := entry.Get("models")
	if !models.IsArray() || len(models.Array()) != 2 {
		t.Fatalf("models = %s, want a two-entry array", response.Body.String())
	}
	if got := models.Array()[0].Get("id").Str; got != "model-a" {
		t.Errorf("first model ID = %q, want model-a", got)
	}
	if got := models.Array()[0].Get("disabled").Bool(); got {
		t.Errorf("model-a disabled = true, want false")
	}
	if got := models.Array()[1].Get("id").Str; got != "model-z" {
		t.Errorf("second model ID = %q, want model-z", got)
	}
	if got := models.Array()[1].Get("disabled").Bool(); !got {
		t.Errorf("model-z disabled = false, want true")
	}
	legacyEntry := catalogs.Array()[1]
	if got := legacyEntry.Get("name").Str; got != "legacy.json" {
		t.Errorf("second catalog name = %q, want legacy.json", got)
	}
	legacyModels := legacyEntry.Get("models")
	if !legacyModels.IsArray() || len(legacyModels.Array()) != 1 {
		t.Fatalf("legacy models = %s, want one model", response.Body.String())
	}
	if legacyModels.Array()[0].Get("disabled").Exists() {
		t.Fatal("legacy model with unknown flags must omit disabled")
	}

	server.mgmt.SetAuthManager(nil)
	emptyRequest := httptest.NewRequest(http.MethodGet, "/v8/management/credentials/model-catalogs", nil)
	emptyRequest.RemoteAddr = "127.0.0.1:12345"
	emptyRequest.Header.Set("X-Management-Key", "test-management-key")
	emptyResponse := httptest.NewRecorder()
	server.engine.ServeHTTP(emptyResponse, emptyRequest)
	if emptyResponse.Code != http.StatusOK {
		t.Fatalf("empty catalog status = %d, want %d; body=%s", emptyResponse.Code, http.StatusOK, emptyResponse.Body.String())
	}
	emptyCatalogs := ojson.ParseBytes(emptyResponse.Body.Bytes()).Get("catalogs")
	if !emptyCatalogs.IsArray() || len(emptyCatalogs.Array()) != 0 {
		t.Fatalf("empty catalogs = %s, want []", emptyResponse.Body.String())
	}
}
