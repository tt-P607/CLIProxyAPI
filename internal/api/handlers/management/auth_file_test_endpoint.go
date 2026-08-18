package management

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// testAuthFileRequest carries the optional upstream model for a test execution.
type testAuthFileRequest struct {
	Model string `json:"model"`
}

// TestAuthFile sends a minimal ping through the credential and reports availability.
//
// It pins execution to the requested auth via PinnedAuthMetadataKey so the test
// exercises exactly that credential regardless of scheduler selection.
func (h *Handler) TestAuthFile(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	auth := h.findAuthForDelete(name)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	var req testAuthFileRequest
	_ = c.ShouldBindJSON(&req)
	targetModel := strings.TrimSpace(req.Model)
	if targetModel == "" {
		targetModel = "gpt-4o-mini"
	}

	payload, _ := json.Marshal(map[string]any{
		"model":      targetModel,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})

	resp, errExec := h.authManager.Execute(c.Request.Context(), []string{strings.ToLower(auth.Provider)},
		cliproxyexecutor.Request{
			Model:   targetModel,
			Payload: payload,
		},
		cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatOpenAI,
			Metadata: map[string]any{
				cliproxyexecutor.PinnedAuthMetadataKey: auth.ID,
			},
		})

	if errExec != nil {
		log.WithError(errExec).WithField("auth_id", auth.ID).Debug("management TestAuthFile execution failed")
		c.JSON(http.StatusOK, gin.H{
			"status_code": 0,
			"message":     "execution failed",
			"error":       errExec.Error(),
		})
		return
	}

	respText := string(resp.Payload)
	if len(respText) > 2000 {
		respText = respText[:2000]
	}

	c.JSON(http.StatusOK, gin.H{
		"status_code": 200,
		"message":     "credential is available (execution successful)",
		"error":       respText,
	})
}
