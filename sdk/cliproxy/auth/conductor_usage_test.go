package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestContextWithRequestedModelAliasRequestFingerprint(t *testing.T) {
	headers := http.Header{"X-Session-ID": []string{" abc "}}
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{Headers: headers}, "model-a")
	const want = "ba7816bf8f01cfea414140de5dae2223"
	if got := coreusage.RequestFingerprintFromContext(ctx); got != want {
		t.Fatalf("request fingerprint = %q, want %q", got, want)
	}

	modelBCtx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{Headers: http.Header{"x-session-id": []string{"abc"}}}, "model-b")
	if got := coreusage.RequestFingerprintFromContext(modelBCtx); got != want {
		t.Fatalf("fingerprint for another model = %q, want %q", got, want)
	}

	headers.Set("X-Session-ID", "changed")
	if got := coreusage.RequestFingerprintFromContext(ctx); got != want {
		t.Fatalf("fingerprint changed with headers after context creation: %q", got)
	}
}

func TestContextWithRequestedModelAliasRejectsInvalidRequestFingerprintHeaders(t *testing.T) {
	for name, headers := range map[string]http.Header{
		"missing":      {},
		"empty":        {"X-Session-ID": []string{"  "}},
		"repeat":       {"X-Session-ID": []string{"abc", "abc"}},
		"case_repeat":  {"X-Session-ID": []string{"abc"}, "x-session-id": []string{"abc"}},
		"empty_repeat": {"X-Session-ID": []string{"abc"}, "x-session-id": nil},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := coreusage.WithRequestFingerprint(context.Background(), http.Header{"X-Session-ID": []string{"inherited"}})
			ctx = contextWithRequestedModelAlias(ctx, cliproxyexecutor.Options{Headers: headers}, "model")
			if got := coreusage.RequestFingerprintFromContext(ctx); got != "" {
				t.Fatalf("request fingerprint = %q, want empty", got)
			}
		})
	}
}

func TestContextWithRequestedModelAliasIncludesStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream_false", true: "stream_true"}[stream], func(t *testing.T) {
			ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
				Stream: stream,
			}, "fallback-model")

			if got := coreusage.StreamFromContext(ctx); got != stream {
				t.Fatalf("stream = %v, want %v", got, stream)
			}
		})
	}
}

func TestContextWithRequestedModelAliasIncludesReasoningEffort(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:  "client-model",
			cliproxyexecutor.ReasoningEffortMetadataKey: "medium",
			cliproxyexecutor.ServiceTierMetadataKey:     "auto",
			cliproxyexecutor.GenerateMetadataKey:        false,
		},
	}, "fallback-model")

	if got := coreusage.RequestedModelAliasFromContext(ctx); got != "client-model" {
		t.Fatalf("requested model alias = %q, want %q", got, "client-model")
	}
	if got := coreusage.ReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", got, "medium")
	}
	gotServiceTier := coreusage.ServiceTierFromContext(ctx)
	if gotServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", gotServiceTier, "auto")
	}
	if got := coreusage.GenerateFromContext(ctx); got {
		t.Fatalf("generate = %v, want false", got)
	}
}

func TestContextWithRequestedModelAliasDefaultsGenerateTrue(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "client-model",
		},
	}, "fallback-model")

	if got := coreusage.GenerateFromContext(ctx); !got {
		t.Fatalf("generate = %v, want true", got)
	}
}

func TestContextWithRequestedModelAliasPreservesExistingGenerateFalse(t *testing.T) {
	ctx := coreusage.WithGenerate(context.Background(), false)
	ctx = contextWithRequestedModelAlias(ctx, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "client-model",
		},
	}, "fallback-model")

	if got := coreusage.GenerateFromContext(ctx); got {
		t.Fatalf("generate = %v, want false", got)
	}
}
