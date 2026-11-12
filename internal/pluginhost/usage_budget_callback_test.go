package pluginhost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestHostUsageBudgetCallFromPlugin(t *testing.T) {
	stats := usage.GetRequestStatistics()
	stats.Reset()
	t.Cleanup(stats.Reset)
	wasEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() { usage.SetStatisticsEnabled(wasEnabled) })

	const apiKey = "synthetic-callback-api-key"
	auth := &coreauth.Auth{ID: "synthetic-ag", Provider: "antigravity", Disabled: true}
	host := New()
	manager := coreauth.NewManager(nil, nil, nil)
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register synthetic auth: %v", errRegister)
	}
	otherAuth, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "synthetic-claude", Provider: "claude"})
	if errRegister != nil {
		t.Fatalf("register synthetic non-Antigravity auth: %v", errRegister)
	}
	stats.Record(context.Background(), coreusage.Record{
		APIKey: apiKey, Model: "[反]gemini-3.8-flash", AuthIndex: registered.Index, RequestedAt: time.Now().UTC().Add(-time.Hour),
		Detail: coreusage.Detail{TotalTokens: 42, CachedTokens: 100},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey: apiKey, Model: "gemini-3.8-flash", AuthIndex: otherAuth.Index, RequestedAt: time.Now().UTC().Add(-time.Hour),
		Detail: coreusage.Detail{TotalTokens: 1000},
	})
	host.SetAuthManager(manager)

	raw, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostUsageBudget, []byte(`{}`))
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil || !envelope.OK {
		t.Fatalf("invalid callback envelope: %v", errDecode)
	}
	var result usage.UsageBudget
	if errDecode := json.Unmarshal(envelope.Result, &result); errDecode != nil {
		t.Fatalf("decode callback result: %v", errDecode)
	}
	if len(result.Keys) != 1 || result.Keys[0].Tokens5h != 42 || result.Keys[0].Tokens24h != 42 || result.Keys[0].Tokens7d != 42 {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(string(raw), apiKey) {
		t.Fatal("callback response exposed a raw API key")
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostUsageBudget, []byte(`{`)); errCall == nil {
		t.Fatal("malformed callback request should be rejected")
	}
}
