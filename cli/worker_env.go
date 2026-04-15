// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"os"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

func buildWorkerLLMEnv(cfg *config.Config, model string, modelEnvKeys ...string) []string {
	provider := resolveWorkerEnvValue("SOURCEBRIDGE_WORKER_LLM_PROVIDER", cfg.LLM.Provider)
	baseURL := resolveWorkerEnvValue("SOURCEBRIDGE_WORKER_LLM_BASE_URL", cfg.LLM.BaseURL)
	apiKey := resolveWorkerEnvValue("SOURCEBRIDGE_WORKER_LLM_API_KEY", cfg.LLM.APIKey)
	model = resolveWorkerLLMModel(model, modelEnvKeys...)
	return []string{
		"SOURCEBRIDGE_WORKER_TEST_MODE=false",
		"SOURCEBRIDGE_WORKER_LLM_PROVIDER=" + provider,
		"SOURCEBRIDGE_WORKER_LLM_BASE_URL=" + baseURL,
		"SOURCEBRIDGE_WORKER_LLM_API_KEY=" + apiKey,
		"SOURCEBRIDGE_WORKER_LLM_MODEL=" + model,
	}
}

func resolveWorkerEnvValue(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func resolveWorkerLLMModel(fallback string, explicitKeys ...string) string {
	keys := append([]string{}, explicitKeys...)
	keys = append(keys,
		"SOURCEBRIDGE_WORKER_LLM_MODEL",
		"SOURCEBRIDGE_LLM_MODEL",
	)
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return fallback
}
