package helps

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDerivedSessionProviderMappings(t *testing.T) {
	t.Parallel()

	metadata := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:test-root"}
	codexID := DerivedSessionUUID("codex", metadata)
	xaiID := DerivedSessionUUID("xai", metadata)
	if _, errParse := uuid.Parse(codexID); errParse != nil {
		t.Fatalf("Codex mapping %q is not a UUID: %v", codexID, errParse)
	}
	if _, errParse := uuid.Parse(xaiID); errParse != nil {
		t.Fatalf("xAI mapping %q is not a UUID: %v", xaiID, errParse)
	}
	if codexID == xaiID {
		t.Fatalf("provider namespaces produced the same UUID: %q", codexID)
	}
	if repeated := DerivedSessionUUID("codex", metadata); repeated != codexID {
		t.Fatalf("Codex mapping is not stable: first=%q repeated=%q", codexID, repeated)
	}

	antigravityID := DerivedAntigravitySessionID(metadata)
	if matched := regexp.MustCompile(`^-[0-9]+$`).MatchString(antigravityID); !matched {
		t.Fatalf("Antigravity mapping = %q, want negative decimal", antigravityID)
	}
	if repeated := DerivedAntigravitySessionID(metadata); repeated != antigravityID {
		t.Fatalf("Antigravity mapping is not stable: first=%q repeated=%q", antigravityID, repeated)
	}
}

func TestProviderSessionUUIDPrefersExecutionSession(t *testing.T) {
	t.Parallel()

	first := map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "connection-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:first-root",
	}
	second := map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "connection-1",
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:second-root",
	}
	firstID := ProviderSessionUUID("codex", first)
	secondID := ProviderSessionUUID("codex", second)
	if firstID == "" || firstID != secondID {
		t.Fatalf("execution session did not stabilize provider UUID: first=%q second=%q", firstID, secondID)
	}
	if firstID == DerivedSessionUUID("codex", first) {
		t.Fatalf("provider UUID did not prefer execution session: %q", firstID)
	}
}

func TestDerivedSessionProviderMappingsRequireIdentity(t *testing.T) {
	t.Parallel()

	if got := DerivedSessionUUID("codex", nil); got != "" {
		t.Fatalf("DerivedSessionUUID() = %q, want empty", got)
	}
	if got := DerivedAntigravitySessionID(nil); got != "" {
		t.Fatalf("DerivedAntigravitySessionID() = %q, want empty", got)
	}
}

func TestAntigravitySessionIDScopesConversationByKey(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Session-ID":      {"synthetic-key-one"},
		"X-Conversation-ID": {"synthetic-conversation-one"},
	}
	first := AntigravitySessionID(headers)
	if !regexp.MustCompile(`^-[0-9]+$`).MatchString(first) {
		t.Fatalf("conversation session mapping = %q, want negative decimal", first)
	}
	if got := AntigravitySessionID(headers); got != first {
		t.Fatalf("same conversation mapping changed: first=%q repeated=%q", first, got)
	}
	changedConversation := headers.Clone()
	changedConversation["X-Conversation-ID"] = []string{"synthetic-conversation-two"}
	if got := AntigravitySessionID(changedConversation); got == first {
		t.Fatal("different conversations received the same session mapping")
	}
	changedKey := headers.Clone()
	changedKey["X-Session-ID"] = []string{"synthetic-key-two"}
	if got := AntigravitySessionID(changedKey); got == first {
		t.Fatal("different caller keys received the same conversation mapping")
	}
	normalizedHeaders := http.Header{
		"x-session-id":      {" synthetic-key-one "},
		"x-conversation-id": {" synthetic-conversation-one "},
	}
	if got := AntigravitySessionID(normalizedHeaders); got != first {
		t.Fatalf("header casing or surrounding spaces changed mapping: got=%q want=%q", got, first)
	}
	metadata := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:unrelated-root"}
	if got := AntigravitySessionID(headers, metadata); got != first {
		t.Fatalf("derived payload identity overrode explicit conversation: got=%q want=%q", got, first)
	}
}

func TestAntigravitySessionIDPreservesDerivedFallback(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		headers http.Header
	}{
		{name: "no headers"},
		{name: "key only", headers: http.Header{"X-Session-ID": {"synthetic-key"}}},
		{name: "conversation only", headers: http.Header{"X-Conversation-ID": {"synthetic-conversation"}}},
		{name: "empty conversation", headers: http.Header{"X-Session-ID": {"synthetic-key"}, "X-Conversation-ID": {" "}}},
		{name: "empty key", headers: http.Header{"X-Session-ID": {" "}, "X-Conversation-ID": {"synthetic-conversation"}}},
		{name: "control-bearing conversation", headers: http.Header{"X-Session-ID": {"synthetic-key"}, "X-Conversation-ID": {"conversation\nvalue"}}},
		{name: "oversized conversation", headers: http.Header{"X-Session-ID": {"synthetic-key"}, "X-Conversation-ID": {strings.Repeat("x", 257)}}},
		{name: "multiple conversation values", headers: http.Header{"X-Session-ID": {"synthetic-key"}, "X-Conversation-ID": {"one", "two"}}},
		{name: "duplicate conversation casing", headers: http.Header{"X-Session-ID": {"synthetic-key"}, "X-Conversation-ID": {"one"}, "x-conversation-id": {"two"}}},
		{name: "multiple key values", headers: http.Header{"X-Session-ID": {"one", "two"}, "X-Conversation-ID": {"synthetic-conversation"}}},
		{name: "duplicate key casing", headers: http.Header{"X-Session-ID": {"one"}, "x-session-id": {"two"}, "X-Conversation-ID": {"synthetic-conversation"}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			metadata := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:original-root"}
			want := DerivedAntigravitySessionID(metadata)
			if got := AntigravitySessionID(testCase.headers, metadata); got != want {
				t.Fatalf("legacy derived mapping changed: got=%q want=%q", got, want)
			}
			if got := AntigravitySessionID(testCase.headers); got != "" {
				t.Fatalf("ambiguous or missing identity produced a conversation mapping: %q", got)
			}
		})
	}
}
