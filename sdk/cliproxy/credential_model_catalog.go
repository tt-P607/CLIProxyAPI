package cliproxy

import (
	"slices"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *Service) credentialModelCatalog(auth *coreauth.Auth) (string, []string, map[string]struct{}) {
	if s == nil || auth == nil {
		return "unknown", nil, nil
	}

	if strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		hints, known := s.authModelCatalogHints(auth)
		if !known {
			return "unknown", nil, nil
		}
		modelIDs := make([]string, 0, len(hints.ModelIDs))
		for modelID := range hints.ModelIDs {
			modelIDs = append(modelIDs, modelID)
		}
		slices.Sort(modelIDs)
		return "upstream", modelIDs, hints.DisabledModelIDs
	}

	if auth.ID == "" {
		return "unknown", nil, nil
	}
	registered := GlobalModelRegistry().GetModelsForClient(auth.ID)
	modelIDs := make([]string, 0, len(registered))
	for _, model := range registered {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		modelIDs = append(modelIDs, model.ID)
	}
	if len(modelIDs) == 0 {
		return "unknown", nil, nil
	}
	slices.Sort(modelIDs)
	return "registered", modelIDs, nil
}
