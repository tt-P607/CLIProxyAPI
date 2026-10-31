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
	active  bool
	retries int
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
	if recovery != nil && isAntigravityQuotaExhausted(provider, err) {
		recovery.active = true
	}
}

func (recovery *antigravityQuotaRecovery) observeCooldown(err error, providers []string) {
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) || cooldown == nil {
		return
	}
	provider := cooldown.provider
	if provider == "" && len(providers) == 1 {
		provider = providers[0]
	}
	recovery.observe(provider, err)
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
		return newAntigravityQuotaRecoveryError()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	recovery.retries++
	logEntryWithRequestID(ctx).WithField("quota_recovery_attempt", recovery.retries).Info("antigravity quota recovery: retrying request")
	return nil
}

func newAntigravityQuotaRecoveryError() *Error {
	return &Error{Code: "upstream_quota_exhausted", Message: "当前模型可用额度不足，自动恢复未能完成请求，请稍后重试。", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
}

func (recovery *antigravityQuotaRecovery) publicError(err error) error {
	if recovery == nil || !recovery.active || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if status := statusCodeFromError(err); status >= 400 && status < 500 && status != http.StatusTooManyRequests {
		return err
	}
	return newAntigravityQuotaRecoveryError()
}
