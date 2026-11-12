package pluginhost

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type rpcHostUsageBudgetRequest struct{}

func (h *Host) callHostUsageBudget(request []byte) ([]byte, error) {
	var req rpcHostUsageBudgetRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host usage budget request: %w", errUnmarshal)
	}

	authAliases := make(map[string]map[string]string)
	if manager := h.currentAuthManager(); manager != nil {
		for _, auth := range manager.List() {
			if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
				continue
			}
			authIndex := strings.TrimSpace(auth.Index)
			if authIndex == "" {
				continue
			}
			aliases := make(map[string]string)
			for _, alias := range coreauth.OAuthModelAliasesFromAttributes(auth.Attributes) {
				aliases[normalizeBudgetAlias(alias.Alias)] = alias.Name
			}
			authAliases[authIndex] = aliases
		}
	}

	now := time.Now().UTC()
	return marshalRPCResult(usage.GetRequestStatistics().GeminiUsageBudget(now, authAliases))
}

func normalizeBudgetAlias(alias string) string {
	alias = strings.TrimSpace(alias)
	for strings.HasPrefix(alias, "[") {
		end := strings.IndexByte(alias, ']')
		if end < 0 {
			break
		}
		alias = strings.TrimSpace(alias[end+1:])
	}
	if slash := strings.LastIndexByte(alias, '/'); slash >= 0 {
		alias = strings.TrimSpace(alias[slash+1:])
	}
	return strings.ToLower(alias)
}
