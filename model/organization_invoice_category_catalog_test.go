package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationInvoiceCatalogModelsMapToExpectedCategory(t *testing.T) {
	expected := []organizationInvoiceStandardCategoryEntry{
		{key: "claude", name: "Claude", sortOrder: 10, models: []string{
			"claude-fable-5", "claude-sonnet-5", "claude-opus-5",
			"claude-opus-4.8", "claude-opus-4.7", "claude-opus-4.6",
			"claude-sonnet-4.6", "claude-haiku-4.5",
		}, prefixes: []string{"claude-"}},
		{key: "gpt", name: "GPT", sortOrder: 20, models: []string{
			"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
			"gpt-5.5", "gpt-5.4", "gpt-image-2", "gpt-5.3-codex",
		}, prefixes: []string{"gpt-", "chatgpt-"}},
		{key: "gemini", name: "Gemini", sortOrder: 30, models: []string{
			"gemini-3-flash-preview", "gemini-3.1-pro-preview",
			"gemini-3.5-flash", "gemini-3.6-flash",
		}, prefixes: []string{"gemini-"}},
		{key: "minimax", name: "Minimax（阿里云）", sortOrder: 40, models: []string{
			"MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.7-Highspeed",
		}, prefixes: []string{"minimax-"}},
		{key: "deepseek", name: "Deepseek", sortOrder: 50, models: []string{
			"deepseek-v4-flash", "deepseek-v4-pro",
		}, prefixes: []string{"deepseek-"}},
		{key: "kimi", name: "Kimi（阿里云）", sortOrder: 60, models: []string{
			"Kimi-3", "Kimi-2.6", "Kimi-2.7-code",
		}, prefixes: []string{"kimi-", "moonshotai/kimi-"}},
		{key: "glm", name: "GLM（阿里云）", sortOrder: 70, models: []string{
			"glm-5.2", "glm-5.1", "glm-5",
		}, prefixes: []string{"glm-", "chatglm-", "chatglm_"}},
		{key: "qwen", name: "Qwen（阿里云）", sortOrder: 80, models: []string{
			"qwen3.7-max", "qwen3.7-plus",
		}, prefixes: []string{"qwen"}},
		{key: "vector", name: "向量", sortOrder: 90, models: []string{
			"text-embedding-3-large", "text-embedding-3-small", "text-embedding-v4",
			"ctg-ac-ultra-latest", "ctg-og-ultra-latest",
		}, prefixes: []string{"text-embedding-"}},
	}
	require.Equal(t, expected, organizationInvoiceStandardCategories)

	for _, category := range expected {
		for _, model := range category.models {
			got := organizationInvoiceCategoryForModel(model)
			assert.Equalf(t, category.key, got.key, "model %q", model)
			assert.Equalf(t, category.name, got.name, "model %q", model)
			assert.Falsef(t, got.fallback, "model %q should not be a fallback", model)
		}
	}
}

func TestOrganizationInvoiceCategoryIgnoresCaseAndSurroundingSpaces(t *testing.T) {
	for _, input := range []string{"Claude-Opus-4.6", "  claude-opus-4.6  ", "CLAUDE-OPUS-4.6"} {
		got := organizationInvoiceCategoryForModel(input)
		assert.Equal(t, "claude", got.key, input)
		assert.False(t, got.fallback, input)
	}
	for _, input := range []string{" MiniMax-M2.7 ", "MINIMAX-M2.7"} {
		got := organizationInvoiceCategoryForModel(input)
		assert.Equal(t, "minimax", got.key, input)
	}
	for _, input := range []string{" Qwen3.7-Max ", "QWEN3.7-MAX"} {
		got := organizationInvoiceCategoryForModel(input)
		assert.Equal(t, "qwen", got.key, input)
	}
}

func TestOrganizationInvoiceCategoryMapsUnambiguousFamilyModels(t *testing.T) {
	tests := map[string]string{
		"gpt-4o":                 "gpt",
		"gpt-5.4-mini":           "gpt",
		"chatgpt-4o-latest":      "gpt",
		"claude-3-haiku":         "claude",
		"claude-opus-4":          "claude",
		"gemini-1.5-pro":         "gemini",
		"minimax-abab":           "minimax",
		"deepseek-r1":            "deepseek",
		"kimi-k2":                "kimi",
		"moonshotai/kimi-k2":     "kimi",
		"glm-4":                  "glm",
		"glm-5-turbo":            "glm",
		"glm-5.2-air":            "glm",
		"chatglm_turbo":          "glm",
		"qwen-max":               "qwen",
		"qwen3-72b":              "qwen",
		"text-embedding-ada-002": "vector",
	}
	for model, expectedKey := range tests {
		got := organizationInvoiceCategoryForModel(model)
		assert.Equalf(t, expectedKey, got.key, "model %q", model)
		assert.Falsef(t, got.fallback, "model %q should be standard", model)
	}
}

func TestOrganizationInvoiceCategoryFallbackIsStableAndSafe(t *testing.T) {
	left := organizationInvoiceCategoryForModel("custom/model:v1")
	right := organizationInvoiceCategoryForModel(" CUSTOM/MODEL:V1 ")
	assert.Equal(t, left.key, right.key)
	assert.Equal(t, "custom/model:v1", left.name)
	assert.True(t, strings.HasPrefix(left.key, organizationInvoiceFallbackCategoryPrefix))
	assert.Len(t, left.key, len(organizationInvoiceFallbackCategoryPrefix)+64)
	assert.NotContains(t, left.key, "/")
}

func TestOrganizationInvoiceCategoryDoesNotInferFamiliesFromSubstrings(t *testing.T) {
	for _, model := range []string{
		"vendor/gpt-4o",
		"custom-claude-compatible",
		"my-qwen-model",
		"embedding-text-v1",
	} {
		got := organizationInvoiceCategoryForModel(model)
		assert.Truef(t, got.fallback, "model %q should remain a fallback", model)
		assert.Truef(t, strings.HasPrefix(got.key, organizationInvoiceFallbackCategoryPrefix), "model %q", model)
	}
}

func TestOrganizationInvoiceCatalogHasNoDuplicateModelOwnership(t *testing.T) {
	index, err := buildOrganizationInvoiceModelCategoryIndex()
	require.NoError(t, err)

	seen := make(map[string]string)
	totalModels := 0
	for _, category := range organizationInvoiceStandardCategories {
		for _, model := range category.models {
			totalModels++
			normalized := normalizeOrganizationInvoiceModelName(model)
			owner, exists := seen[normalized]
			require.Falsef(t, exists, "model %q owned by both %q and %q", model, owner, category.key)
			seen[normalized] = category.key
		}
	}
	assert.Len(t, index, totalModels)
}

func TestOrganizationInvoiceCatalogVectorMembers(t *testing.T) {
	for _, model := range []string{
		"text-embedding-3-large", "text-embedding-3-small", "text-embedding-v4",
		"ctg-ac-ultra-latest", "ctg-og-ultra-latest",
	} {
		got := organizationInvoiceCategoryForModel(model)
		assert.Equalf(t, "vector", got.key, "model %q", model)
		assert.False(t, got.fallback, model)
	}
}
