package pluginhost

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type rpcHostUsageBudgetRequest struct {
	Provider      *string  `json:"provider,omitempty"`
	ModelPrefixes []string `json:"model_prefixes,omitempty"`
}

func (h *Host) callHostUsageBudget(request []byte) ([]byte, error) {
	var req rpcHostUsageBudgetRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host usage budget request: %w", errUnmarshal)
	}
	if req.Provider == nil && req.ModelPrefixes == nil {
		// Empty requests preserve the filtering used by existing plugin binaries.
		provider := "antigravity"
		req.Provider = &provider
		req.ModelPrefixes = []string{"gemini-"}
	}
	provider := ""
	if req.Provider != nil {
		provider = strings.TrimSpace(*req.Provider)
	}
	for _, prefix := range req.ModelPrefixes {
		if strings.TrimSpace(prefix) == "" {
			return nil, fmt.Errorf("host usage budget model prefixes must not be blank")
		}
	}

	authAliases := make(map[string]map[string]string)
	if manager := h.currentAuthManager(); manager != nil {
		for _, auth := range manager.List() {
			if auth == nil || (provider != "" && !strings.EqualFold(strings.TrimSpace(auth.Provider), provider)) {
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
	return marshalRPCResult(usage.GetRequestStatistics().KeyUsageBudget(now, authAliases, req.ModelPrefixes))
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
