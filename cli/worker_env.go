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
	env := []string{
		"SOURCEBRIDGE_WORKER_TEST_MODE=false",
		"SOURCEBRIDGE_WORKER_LLM_PROVIDER=" + provider,
		"SOURCEBRIDGE_WORKER_LLM_BASE_URL=" + baseURL,
		"SOURCEBRIDGE_WORKER_LLM_API_KEY=" + apiKey,
		"SOURCEBRIDGE_WORKER_LLM_MODEL=" + model,
	}
	return append(env, buildWorkerStorageEnv(cfg)...)
}

func buildWorkerStorageEnv(cfg *config.Config) []string {
	return []string{
		"SOURCEBRIDGE_STORAGE_SURREAL_URL=" + resolveWorkerEnvValue("SOURCEBRIDGE_STORAGE_SURREAL_URL", cfg.Storage.SurrealURL),
		"SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE=" + resolveWorkerEnvValue("SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE", cfg.Storage.SurrealNamespace),
		"SOURCEBRIDGE_STORAGE_SURREAL_DATABASE=" + resolveWorkerEnvValue("SOURCEBRIDGE_STORAGE_SURREAL_DATABASE", cfg.Storage.SurrealDatabase),
		"SOURCEBRIDGE_STORAGE_SURREAL_USER=" + resolveWorkerEnvValue("SOURCEBRIDGE_STORAGE_SURREAL_USER", cfg.Storage.SurrealUser),
		"SOURCEBRIDGE_STORAGE_SURREAL_PASS=" + resolveWorkerEnvValue("SOURCEBRIDGE_STORAGE_SURREAL_PASS", cfg.Storage.SurrealPass),
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
