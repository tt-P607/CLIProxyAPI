package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	credentialModelCatalogSourceUpstream   = "upstream"
	credentialModelCatalogSourceRegistered = "registered"
	credentialModelCatalogSourceUnknown    = "unknown"
)

// CredentialModelCatalogResolver returns a read-only runtime view for one credential.
// A nil disabledModelIDs set means disabled flags are unknown.
type CredentialModelCatalogResolver func(*coreauth.Auth) (source string, modelIDs []string, disabledModelIDs map[string]struct{})

type credentialModelCatalogModel struct {
	ID       string `json:"id"`
	Disabled *bool  `json:"disabled,omitempty"`
}

type credentialModelCatalogEntry struct {
	Name      string                        `json:"name"`
	AuthIndex string                        `json:"auth_index"`
	Provider  string                        `json:"provider"`
	Source    string                        `json:"source"`
	Models    []credentialModelCatalogModel `json:"models"`
	sortID    string                        `json:"-"`
}

type credentialModelCatalogResponse struct {
	Catalogs []credentialModelCatalogEntry `json:"catalogs"`
}

// SetCredentialModelCatalogResolver injects the SDK's per-credential catalog lookup.
func (h *Handler) SetCredentialModelCatalogResolver(resolver CredentialModelCatalogResolver) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.modelCatalogResolver = resolver
	h.mu.Unlock()
}

// GetCredentialModelCatalogs lists each known credential with its current read-only model catalog.
func (h *Handler) GetCredentialModelCatalogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	manager := h.authManager
	resolver := h.modelCatalogResolver
	h.mu.Unlock()

	auths := []*coreauth.Auth{}
	if manager != nil {
		auths = manager.List()
	}
	catalogs := make([]credentialModelCatalogEntry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}

		name := strings.TrimSpace(auth.FileName)
		if name == "" {
			name = auth.ID
		}
		entry := credentialModelCatalogEntry{
			Name:      name,
			AuthIndex: lockedAuthIndex(auth),
			Provider:  strings.TrimSpace(auth.Provider),
			Source:    credentialModelCatalogSourceUnknown,
			Models:    make([]credentialModelCatalogModel, 0),
			sortID:    auth.ID,
		}
		if resolver != nil {
			source, modelIDs, disabledModelIDs := resolver(auth)
			switch source {
			case credentialModelCatalogSourceUpstream:
				entry.Source = source
			case credentialModelCatalogSourceRegistered:
				if len(modelIDs) > 0 {
					entry.Source = source
				}
			}
			if entry.Source != credentialModelCatalogSourceUnknown {
				normalizedModelIDs := make([]string, 0, len(modelIDs))
				for _, modelID := range modelIDs {
					if modelID = strings.TrimSpace(modelID); modelID != "" {
						normalizedModelIDs = append(normalizedModelIDs, modelID)
					}
				}
				sort.Strings(normalizedModelIDs)
				seen := make(map[string]struct{}, len(modelIDs))
				for _, modelID := range normalizedModelIDs {
					if _, exists := seen[modelID]; exists {
						continue
					}
					seen[modelID] = struct{}{}
					model := credentialModelCatalogModel{ID: modelID}
					if entry.Source == credentialModelCatalogSourceUpstream && disabledModelIDs != nil {
						disabled := false
						_, disabled = disabledModelIDs[modelID]
						model.Disabled = &disabled
					}
					entry.Models = append(entry.Models, model)
				}
				if entry.Source == credentialModelCatalogSourceRegistered && len(entry.Models) == 0 {
					entry.Source = credentialModelCatalogSourceUnknown
				}
			}
		}
		catalogs = append(catalogs, entry)
	}

	sort.Slice(catalogs, func(i, j int) bool {
		left, right := catalogs[i], catalogs[j]
		if left.Name != right.Name {
			leftName, rightName := strings.ToLower(left.Name), strings.ToLower(right.Name)
			if leftName != rightName {
				return leftName < rightName
			}
			return left.Name < right.Name
		}
		if left.AuthIndex != right.AuthIndex {
			return left.AuthIndex < right.AuthIndex
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		return left.sortID < right.sortID
	})
	c.JSON(http.StatusOK, credentialModelCatalogResponse{Catalogs: catalogs})
}
