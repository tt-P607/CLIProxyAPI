package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestKeyUsageBudgetAggregatesWindowsAndFiltersHistory(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	const ownKey = "synthetic-own-api-key"
	const otherKey = "synthetic-other-api-key"
	start := now.Add(-8 * 24 * time.Hour)
	record := func(key, model, authIndex string, timestamp time.Time, tokens int64) {
		t.Helper()
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      key,
			Model:       model,
			AuthIndex:   authIndex,
			RequestedAt: timestamp,
			Detail:      coreusage.Detail{TotalTokens: tokens, CachedTokens: 1000},
		})
	}
	// Exact lower bounds are excluded; exact now is included.
	record(ownKey, "[反]gemini-3.8-flash", "ag-own", now.Add(-5*time.Hour), 10)
	record(ownKey, "team/gemini-3.8-flash", "ag-own", now, 11)
	record(ownKey, "gemini-3.8-flash", "ag-own", now.Add(-24*time.Hour), 12)
	record(ownKey, "gemini-3.8-flash", "ag-own", now.Add(-7*24*time.Hour), 13)
	record(ownKey, "gemini-3.8-flash", "ag-own", start, 14)
	record(ownKey, "gemini-3.8-flash", "ag-own", now.Add(time.Minute), 15)
	record(ownKey, "claude-sonnet-5", "ag-own", now.Add(-time.Hour), 100)
	record(ownKey, "gpt-5.5", "ag-own", now.Add(-time.Hour), 200)
	record(ownKey, "gemini-3.8-flash", "other-auth", now.Add(-time.Hour), 300)
	record(otherKey, "gemini-3.8-flash", "ag-other", now.Add(-2*time.Hour), 7)
	stats.Record(context.Background(), coreusage.Record{
		APIKey: ownKey, Model: "gemini-alias", AuthIndex: "ag-own", RequestedAt: now.Add(-2 * time.Hour),
		Detail: coreusage.Detail{InputTokens: 2, CachedTokens: 99},
	})

	aliases := map[string]map[string]string{
		"ag-own":   {"gemini-alias": "gemini-3.8-flash"},
		"ag-other": {},
	}
	got := stats.KeyUsageBudget(now, aliases, []string{"gemini-"})
	if !got.GeneratedAt.Equal(now) || !got.StartedAt.Equal(start) {
		t.Fatalf("times = generated %v started %v", got.GeneratedAt, got.StartedAt)
	}
	if len(got.Keys) != 2 {
		t.Fatalf("key count = %d, want two independently aggregated keys", len(got.Keys))
	}
	want := []KeyBudget{
		{Fingerprint: budgetFingerprint(ownKey), Tokens5h: 13, Tokens24h: 23, Tokens7d: 35},
		{Fingerprint: budgetFingerprint(otherKey), Tokens5h: 7, Tokens24h: 7, Tokens7d: 7},
	}
	for i := range want {
		if got.Keys[i] != want[i] {
			t.Errorf("key[%d] = %+v, want %+v", i, got.Keys[i], want[i])
		}
	}
	raw, errMarshal := json.Marshal(got)
	if errMarshal != nil {
		t.Fatalf("marshal result: %v", errMarshal)
	}
	if strings.Contains(string(raw), ownKey) || strings.Contains(string(raw), otherKey) {
		t.Fatal("serialized usage budget exposed a raw API key")
	}
}

func TestKeyUsageBudgetEmptyAndZeroTime(t *testing.T) {
	stats := NewRequestStatistics()
	if got := stats.KeyUsageBudget(time.Time{}, nil, nil); !got.GeneratedAt.IsZero() || got.Keys == nil {
		t.Fatalf("zero-time result = %+v", got)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if got := stats.KeyUsageBudget(now, nil, nil); !got.GeneratedAt.Equal(now) || !got.StartedAt.IsZero() || len(got.Keys) != 0 {
		t.Fatalf("empty result = %+v", got)
	}
}

func TestKeyUsageBudgetUsesCallerModelFilters(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for model, tokens := range map[string]int64{"gemini-test": 10, "claude-test": 20, "team-alias": 30} {
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "synthetic-key", Model: model, AuthIndex: "selected-auth", RequestedAt: now,
			Detail: coreusage.Detail{TotalTokens: tokens},
		})
	}
	aliases := map[string]map[string]string{"selected-auth": {"team-alias": "claude-test"}}
	for _, test := range []struct {
		name     string
		prefixes []string
		want     int64
	}{
		{name: "non-Gemini models and aliases", prefixes: []string{" CLAUDE- "}, want: 50},
		{name: "multiple families", prefixes: []string{"gemini-", "claude-"}, want: 60},
		{name: "all models", want: 60},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := stats.KeyUsageBudget(now, aliases, test.prefixes)
			if len(got.Keys) != 1 || got.Keys[0].Tokens5h != test.want || got.Keys[0].Tokens24h != test.want || got.Keys[0].Tokens7d != test.want {
				t.Fatalf("result = %+v, want %d tokens in each window", got, test.want)
			}
		})
	}
}

func budgetFingerprint(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}
