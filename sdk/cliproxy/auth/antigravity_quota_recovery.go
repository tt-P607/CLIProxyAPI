package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const antigravityQuotaRecoveryLimit = 3

type antigravityQuotaRecoveryKey struct{}

type antigravityQuotaRecovery struct {
	active           bool
	retries          int
	failureStatus    int
	quotaExhausted   bool
	diagnostic       bool
	diagnosticAuthID string
	lastError        error
}

func withAntigravityQuotaRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, antigravityQuotaRecoveryKey{}, &antigravityQuotaRecovery{})
}

func quotaRecoveryFromContext(ctx context.Context) *antigravityQuotaRecovery {
	if ctx == nil {
		return nil
	}

	recovery, _ := ctx.Value(antigravityQuotaRecoveryKey{}).(*antigravityQuotaRecovery)
	return recovery
}

func isPinnedDiagnostic(ctx context.Context, authID string) bool {
	recovery := quotaRecoveryFromContext(ctx)
	return recovery != nil && recovery.diagnostic && recovery.diagnosticAuthID != "" && recovery.diagnosticAuthID == authID
}

func isAntigravityQuotaExhausted(provider string, err error) bool {
	if !strings.EqualFold(provider, "antigravity") || statusCodeFromError(err) != http.StatusTooManyRequests {
		return false
	}
	body := err.Error()
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		if strings.EqualFold(authErr.Code, "quota_exhausted") {
			return true
		}
		body = authErr.Message
	}
	var response struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &response) == nil {
		for _, detail := range response.Error.Details {
			if detail.Reason == "RATE_LIMIT_EXCEEDED" {
				return false
			}
			if detail.Reason == "QUOTA_EXHAUSTED" {
				return true
			}
		}
		body = response.Error.Message
	}
	return strings.Contains(body, "QUOTA_EXHAUSTED") || strings.Contains(strings.ToLower(body), "individual quota reached")
}

func (recovery *antigravityQuotaRecovery) observe(provider string, err error) {
	if recovery == nil || !strings.EqualFold(provider, "antigravity") {
		return
	}
	status := statusCodeFromError(err)
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		recovery.active = true
		recovery.failureStatus = status
		recovery.quotaExhausted = isAntigravityQuotaExhausted(provider, err)
		if recovery.diagnostic {
			recovery.lastError = err
		}
	}
}

func (recovery *antigravityQuotaRecovery) observeCooldown(err error, providers []string) {
	var cooldown *modelCooldownError
	provider := ""
	if errors.As(err, &cooldown) && cooldown != nil {
		provider = cooldown.provider
	} else {
		var unavailable *Error
		if !errors.As(err, &unavailable) || unavailable == nil || (unavailable.Code != "auth_unavailable" && unavailable.Code != "auth_not_found") {
			return
		}
	}
	if provider == "" && len(providers) == 1 {
		provider = providers[0]
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		recovery.observe(provider, cause)
	}
}

func (recovery *antigravityQuotaRecovery) pending() bool {
	return recovery != nil && recovery.active && recovery.retries < antigravityQuotaRecoveryLimit
}

func (recovery *antigravityQuotaRecovery) exhausted() bool {
	return recovery != nil && recovery.active && recovery.retries >= antigravityQuotaRecoveryLimit
}

func (recovery *antigravityQuotaRecovery) begin(ctx context.Context) error {
	if recovery == nil || !recovery.active {
		return nil
	}
	if recovery.exhausted() {
		return recovery.recoveryError()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	recovery.retries++
	logEntryWithRequestID(ctx).WithField("quota_recovery_attempt", recovery.retries).Info("antigravity quota recovery: retrying request")
	return nil
}

func newAntigravityQuotaRecoveryError() *Error {
	return &Error{Code: "upstream_quota_exhausted", Message: "当前用量已达到上游限额，请等待额度恢复后重试。", Retryable: true, HTTPStatus: http.StatusTooManyRequests}
}

func (recovery *antigravityQuotaRecovery) recoveryError() error {
	if recovery.diagnostic && recovery.lastError != nil {
		return recovery.lastError
	}
	if recovery.quotaExhausted {
		return newAntigravityQuotaRecoveryError()
	}
	if recovery.failureStatus == http.StatusTooManyRequests {
		return &Error{Code: "upstream_rate_limited", Message: "当前请求已达到上游速率限制，请稍后重试。", Retryable: true, HTTPStatus: http.StatusTooManyRequests}
	}
	return &Error{Code: "upstream_account_unavailable", Message: "当前模型服务暂时不可用，自动恢复未能完成请求，请稍后重试。", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
}

func (recovery *antigravityQuotaRecovery) publicError(err error) error {
	if recovery == nil || recovery.diagnostic || !recovery.active || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if status := statusCodeFromError(err); status >= 400 && status < 500 && status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return err
	}
	return recovery.recoveryError()
}
