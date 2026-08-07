package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

// organizationInvoiceStandardCategoryEntry describes one standard invoice
// category: a stable category key, the display name written into CSV exports,
// a fixed sort order, confirmed model names, and unambiguous family prefixes.
type organizationInvoiceStandardCategoryEntry struct {
	key       string
	name      string
	sortOrder int
	models    []string
	prefixes  []string
}

// organizationInvoiceStandardCategories is the authoritative model catalog.
// Exact names take priority, then unambiguous brand/family prefixes are used so
// established variants such as gpt-4o and glm-5-turbo do not become fallback
// categories merely because they were omitted from a finance-provided list.
// The default settlement factor stays OrganizationSettlementFactorScale
// (1.0000) for every category; finance configures real factors per effective
// month after release rather than baking reference ratios into code.
var organizationInvoiceStandardCategories = []organizationInvoiceStandardCategoryEntry{
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

func normalizeOrganizationInvoiceModelName(modelName string) string {
	return strings.ToLower(strings.TrimSpace(modelName))
}

// buildOrganizationInvoiceModelCategoryIndex maps every normalized catalog
// model name to its standard category. It fails if a model is assigned to more
// than one category, enforcing the "one model, one standard category"
// invariant at startup.
func buildOrganizationInvoiceModelCategoryIndex() (map[string]organizationInvoiceCategory, error) {
	index := make(map[string]organizationInvoiceCategory)
	for _, category := range organizationInvoiceStandardCategories {
		for _, model := range category.models {
			normalized := normalizeOrganizationInvoiceModelName(model)
			if _, exists := index[normalized]; exists {
				return nil, fmt.Errorf("organization invoice model %q is assigned to more than one category", model)
			}
			index[normalized] = organizationInvoiceCategory{
				key:       category.key,
				name:      category.name,
				sortOrder: category.sortOrder,
			}
		}
	}
	return index, nil
}

var organizationInvoiceModelCategoryIndex = func() map[string]organizationInvoiceCategory {
	index, err := buildOrganizationInvoiceModelCategoryIndex()
	if err != nil {
		panic(err)
	}
	return index
}()

// organizationInvoiceCategoryForModel resolves a logged model name to its
// invoice category. Catalog models match exactly first (after normalization),
// followed by the catalog's unambiguous family prefixes. Any other model falls
// back to a stable per-model category keyed by the sha256 of its normalized
// name. The original model name is preserved for display; only the category
// key is derived from the normalized form.
func organizationInvoiceCategoryForModel(modelName string) organizationInvoiceCategory {
	normalized := normalizeOrganizationInvoiceModelName(modelName)
	if category, ok := organizationInvoiceModelCategoryIndex[normalized]; ok {
		return category
	}
	for _, category := range organizationInvoiceStandardCategories {
		for _, prefix := range category.prefixes {
			if strings.HasPrefix(normalized, prefix) {
				return organizationInvoiceCategory{
					key:       category.key,
					name:      category.name,
					sortOrder: category.sortOrder,
				}
			}
		}
	}
	hash := sha256.Sum256([]byte(normalized))
	displayName := strings.TrimSpace(modelName)
	if displayName == "" {
		displayName = "Unknown model"
	}
	return organizationInvoiceCategory{
		key:       organizationInvoiceFallbackCategoryPrefix + hex.EncodeToString(hash[:]),
		name:      displayName,
		fallback:  true,
		sortOrder: math.MaxInt,
	}
}
