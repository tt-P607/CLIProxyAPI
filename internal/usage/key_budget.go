package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// KeyBudget contains token totals for one client API key fingerprint.
type KeyBudget struct {
	Fingerprint string `json:"fingerprint"`
	Tokens5h    int64  `json:"tokens_5h"`
	Tokens24h   int64  `json:"tokens_24h"`
	Tokens7d    int64  `json:"tokens_7d"`
}

// UsageBudget is the host-side usage summary exposed to plugins.
type UsageBudget struct {
	GeneratedAt time.Time   `json:"generated_at"`
	StartedAt   time.Time   `json:"started_at"`
	Keys        []KeyBudget `json:"keys"`
}

// GeminiUsageBudget aggregates visible Gemini usage associated with the given
// Antigravity auth indexes. Model aliases must map to Gemini model names.
func (s *RequestStatistics) GeminiUsageBudget(now time.Time, authAliases map[string]map[string]string) UsageBudget {
	result := UsageBudget{GeneratedAt: now, Keys: []KeyBudget{}}
	if s == nil || now.IsZero() {
		return result
	}

	type totals struct {
		fiveHours  int64
		twentyFour int64
		sevenDays  int64
	}
	byFingerprint := make(map[string]*totals)
	cutoff5h := now.Add(-5 * time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)
	cutoff7d := now.Add(-7 * 24 * time.Hour)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for apiKey, api := range s.apis {
		if api == nil || apiKey == "" {
			continue
		}
		for modelName, model := range api.Models {
			if model == nil {
				continue
			}
			for _, detail := range model.Details {
				authModelAliases, eligibleAuth := authAliases[detail.AuthIndex]
				if !eligibleAuth || !isGeminiBudgetModel(modelName, authModelAliases) || detail.Timestamp.IsZero() || detail.Timestamp.After(now) {
					continue
				}
				if result.StartedAt.IsZero() || detail.Timestamp.Before(result.StartedAt) {
					result.StartedAt = detail.Timestamp
				}
				fingerprint := sha256.Sum256([]byte(apiKey))
				fingerprintText := hex.EncodeToString(fingerprint[:])
				keyTotals := byFingerprint[fingerprintText]
				if keyTotals == nil {
					keyTotals = &totals{}
					byFingerprint[fingerprintText] = keyTotals
				}
				tokens := normaliseTokenStats(detail.Tokens).TotalTokens
				if tokens < 0 {
					tokens = 0
				}
				if detail.Timestamp.After(cutoff5h) {
					keyTotals.fiveHours += tokens
				}
				if detail.Timestamp.After(cutoff24h) {
					keyTotals.twentyFour += tokens
				}
				if detail.Timestamp.After(cutoff7d) {
					keyTotals.sevenDays += tokens
				}
			}
		}
	}

	result.Keys = make([]KeyBudget, 0, len(byFingerprint))
	for fingerprint, keyTotals := range byFingerprint {
		result.Keys = append(result.Keys, KeyBudget{
			Fingerprint: fingerprint,
			Tokens5h:    keyTotals.fiveHours,
			Tokens24h:   keyTotals.twentyFour,
			Tokens7d:    keyTotals.sevenDays,
		})
	}
	sort.Slice(result.Keys, func(i, j int) bool {
		return result.Keys[i].Fingerprint < result.Keys[j].Fingerprint
	})
	return result
}

func isGeminiBudgetModel(model string, aliases map[string]string) bool {
	model = normalizeBudgetModelName(model)
	if strings.HasPrefix(strings.ToLower(model), "gemini-") {
		return true
	}
	canonical, ok := aliases[strings.ToLower(model)]
	return ok && strings.HasPrefix(strings.ToLower(normalizeBudgetModelName(canonical)), "gemini-")
}

func normalizeBudgetModelName(model string) string {
	model = strings.TrimSpace(model)
	for strings.HasPrefix(model, "[") {
		end := strings.IndexByte(model, ']')
		if end < 0 {
			break
		}
		model = strings.TrimSpace(model[end+1:])
	}
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = strings.TrimSpace(model[slash+1:])
	}
	return model
}
