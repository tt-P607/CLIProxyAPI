package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeAntigravityModelID(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"gemini-3-flash", "gemini-3-flash"},
		{"  GEMINI-3-FLASH  ", "gemini-3-flash"},
		{"", ""},
		{"   ", ""},
		{strings.Repeat("a", 256), strings.Repeat("a", 256)},
		{strings.Repeat("a", 257), ""},
	}
	for _, c := range cases {
		got := NormalizeAntigravityModelID(c.input)
		if got != c.expected {
			t.Errorf("NormalizeAntigravityModelID(%q) = %q, want %q", c.input, got, c.expected)
		}
	}
}

func TestParseAntigravityModelCapabilityHints(t *testing.T) {
	t.Run("valid payload with models and web search", func(t *testing.T) {
		body := []byte(`{"models":{"gemini-3-flash":{"displayName":"Gemini 3 Flash","disabled":true},"claude-sonnet-5-5-medium":{"displayName":"Claude Sonnet 5.5","disabled":false}},"webSearchModelIds":["gemini-3-flash"]}`)
		hints, ok := ParseAntigravityModelCapabilityHints(body)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if hints.ModelIDs == nil {
			t.Fatal("expected non-nil ModelIDs")
		}
		if len(hints.ModelIDs) != 2 {
			t.Errorf("got %d models, want 2", len(hints.ModelIDs))
		}
		if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
			t.Errorf("missing gemini-3-flash in ModelIDs")
		}
		if len(hints.WebSearchModelIDs) != 1 {
			t.Errorf("got %d webSearchModelIds, want 1", len(hints.WebSearchModelIDs))
		}
		if _, exists := hints.WebSearchModelIDs["gemini-3-flash"]; !exists {
			t.Errorf("missing gemini-3-flash in WebSearchModelIDs")
		}
		if hints.DisabledModelIDs == nil || len(hints.DisabledModelIDs) != 1 {
			t.Fatalf("disabled model set = %v, want one disabled model", hints.DisabledModelIDs)
		}
		if _, exists := hints.DisabledModelIDs["gemini-3-flash"]; !exists {
			t.Errorf("missing disabled gemini-3-flash in DisabledModelIDs")
		}
	})

	t.Run("empty catalog is authoritative empty", func(t *testing.T) {
		body := []byte(`{"models":{}}`)
		hints, ok := ParseAntigravityModelCapabilityHints(body)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if hints.ModelIDs == nil {
			t.Fatal("expected non-nil empty ModelIDs")
		}
		if len(hints.ModelIDs) != 0 {
			t.Errorf("expected 0 models, got %d", len(hints.ModelIDs))
		}
		if hints.DisabledModelIDs == nil || len(hints.DisabledModelIDs) != 0 {
			t.Errorf("disabled model flags should be known and empty, got %v", hints.DisabledModelIDs)
		}
	})

	t.Run("missing models field leaves ModelIDs nil", func(t *testing.T) {
		body := []byte(`{"webSearchModelIds":["gemini-3.5-flash-lite"]}`)
		hints, ok := ParseAntigravityModelCapabilityHints(body)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if hints.ModelIDs != nil {
			t.Fatal("expected nil ModelIDs for unknown catalog")
		}
		if hints.DisabledModelIDs != nil {
			t.Fatal("expected nil DisabledModelIDs for unknown catalog")
		}
		if len(hints.WebSearchModelIDs) != 1 {
			t.Errorf("got %d webSearchModelIds, want 1", len(hints.WebSearchModelIDs))
		}
	})

	t.Run("invalid json returns false", func(t *testing.T) {
		_, ok := ParseAntigravityModelCapabilityHints([]byte(`{not valid json`))
		if ok {
			t.Fatal("expected false for invalid JSON")
		}
	})

	t.Run("clones model IDs out of caller buffer", func(t *testing.T) {
		buf := []byte(`{"models":{"gemini-3-flash":{"disabled":true}},"webSearchModelIds":["gemini-3-flash"]}`)
		hints, ok := ParseAntigravityModelCapabilityHints(buf)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		for i := range buf {
			buf[i] = 'x'
		}
		if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
			t.Fatal("expected gemini-3-flash in ModelIDs after buffer mutation")
		}
		if _, exists := hints.WebSearchModelIDs["gemini-3-flash"]; !exists {
			t.Fatal("expected gemini-3-flash in WebSearchModelIDs after buffer mutation")
		}
		if _, exists := hints.DisabledModelIDs["gemini-3-flash"]; !exists {
			t.Fatal("expected gemini-3-flash in DisabledModelIDs after buffer mutation")
		}
	})

	t.Run("non-boolean disabled flag leaves flags unknown", func(t *testing.T) {
		for _, body := range []string{
			`{"models":{"gemini-3-flash":{"disabled":"true"}}}`,
			`{"models":{"gemini-3-flash":{"disabled":null}}}`,
		} {
			hints, ok := ParseAntigravityModelCapabilityHints([]byte(body))
			if !ok || hints.ModelIDs == nil {
				t.Fatal("expected a valid model catalog")
			}
			if hints.DisabledModelIDs != nil {
				t.Fatalf("invalid disabled flag should stay unknown, got %v", hints.DisabledModelIDs)
			}
		}
	})
}

func TestPersistedAntigravityCapabilityHintsRoundTrip(t *testing.T) {
	hints := AntigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{
			"gemini-3-flash": {},
			"gemini-4-argon": {},
		},
		WebSearchModelIDs: map[string]struct{}{
			"gemini-3-flash": {},
		},
		DisabledModelIDs: map[string]struct{}{
			"gemini-4-argon": {},
		},
	}
	project := "aicode-consumers"
	endpointWithUser := "https://user:password@cloudcode-pa.googleapis.com"
	expectedEndpoint := "https://cloudcode-pa.googleapis.com"

	serialized := BuildPersistedAntigravityCapabilityHints(project, endpointWithUser, hints)
	if ep, ok := serialized["endpoint"].(string); !ok || ep != expectedEndpoint {
		t.Errorf("expected endpoint %q, got %q", expectedEndpoint, ep)
	}

	metadata := map[string]any{
		AntigravityPersistedHintsMetaKey: serialized,
	}

	parsed, ok := ParsePersistedAntigravityCapabilityHints(metadata, project, expectedEndpoint)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if len(parsed.ModelIDs) != 2 {
		t.Errorf("got %d models, want 2", len(parsed.ModelIDs))
	}
	if len(parsed.WebSearchModelIDs) != 1 {
		t.Errorf("got %d web search models, want 1", len(parsed.WebSearchModelIDs))
	}
	if parsed.DisabledModelIDs == nil || len(parsed.DisabledModelIDs) != 1 {
		t.Fatalf("disabled model IDs = %v, want one disabled model", parsed.DisabledModelIDs)
	}
	if _, exists := parsed.DisabledModelIDs["gemini-4-argon"]; !exists {
		t.Fatal("disabled model ID was not preserved")
	}

	// Mismatched project
	if _, okMismatch := ParsePersistedAntigravityCapabilityHints(metadata, "other-project", expectedEndpoint); okMismatch {
		t.Error("expected mismatch for different project")
	}

	// Mismatched endpoint
	if _, okMismatch := ParsePersistedAntigravityCapabilityHints(metadata, project, "https://other.com"); okMismatch {
		t.Error("expected mismatch for different endpoint")
	}

	// Nil ModelIDs (unknown catalog) must not serialize as authoritative empty catalog
	if gotNil := BuildPersistedAntigravityCapabilityHints(project, expectedEndpoint, AntigravityModelCapabilityHints{}); gotNil != nil {
		t.Errorf("expected nil persisted hints for unknown catalog, got %v", gotNil)
	}

	legacy := map[string]any{
		AntigravityPersistedHintsMetaKey: map[string]any{
			"project_id": project,
			"endpoint":   expectedEndpoint,
			"model_ids":  []string{"legacy-model"},
		},
	}
	legacyHints, ok := ParsePersistedAntigravityCapabilityHints(legacy, project, expectedEndpoint)
	if !ok || legacyHints.DisabledModelIDs != nil {
		t.Fatalf("legacy disabled flags should remain unknown: hints=%+v ok=%v", legacyHints, ok)
	}
	legacyPersisted := BuildPersistedAntigravityCapabilityHints(project, expectedEndpoint, legacyHints)
	if _, exists := legacyPersisted["disabled_model_ids"]; exists {
		t.Fatal("legacy unknown flags must not be persisted as a known empty set")
	}
}

func TestFetchAntigravityAvailableModels(t *testing.T) {
	t.Run("successful probe", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("unexpected authorization header: %s", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}},"webSearchModelIds":["gemini-3-flash"]}`))
		}))
		defer server.Close()

		hints, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{server.URL},
			AccessToken: "test-token",
			ProjectID:   "aicode-consumers",
		})
		if status != AntigravityProbeStatusSuccess {
			t.Fatalf("expected success, got %v", status)
		}
		if len(hints.ModelIDs) != 1 {
			t.Errorf("got %d models, want 1", len(hints.ModelIDs))
		}
	})

	t.Run("auth error 401", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{server.URL},
			AccessToken: "bad-token",
		})
		if status != AntigravityProbeStatusAuthError {
			t.Fatalf("expected auth error, got %v", status)
		}
	})

	t.Run("empty base urls returns transient error", func(t *testing.T) {
		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    nil,
			AccessToken: "token",
		})
		if status != AntigravityProbeStatusTransientError {
			t.Fatalf("expected transient error, got %v", status)
		}
	})

	t.Run("empty access token returns auth error", func(t *testing.T) {
		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{"https://example.com"},
			AccessToken: "",
		})
		if status != AntigravityProbeStatusAuthError {
			t.Fatalf("expected auth error, got %v", status)
		}
	})

	t.Run("redirect is not followed", func(t *testing.T) {
		redirectTargetVisited := false
		targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectTargetVisited = true
			w.WriteHeader(http.StatusOK)
		}))
		defer targetServer.Close()

		sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, targetServer.URL, http.StatusFound)
		}))
		defer sourceServer.Close()

		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{sourceServer.URL},
			AccessToken: "token",
		})
		if redirectTargetVisited {
			t.Fatal("redirect target should not have been visited")
		}
		if status != AntigravityProbeStatusTransientError {
			t.Fatalf("expected transient error for 302 without redirect, got %v", status)
		}
	})
}
