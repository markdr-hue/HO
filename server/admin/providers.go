/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
)

// ProvidersHandler handles LLM provider CRUD and test endpoints.
type ProvidersHandler struct {
	deps *Deps
}

type createProviderRequest struct {
	Name         string  `json:"name"`
	ProviderType string  `json:"provider_type"`
	APIKey       string  `json:"api_key"`
	BaseURL      *string `json:"base_url"`
}

type updateProviderRequest struct {
	Name      string  `json:"name"`
	APIKey    string  `json:"api_key,omitempty"`
	BaseURL   *string `json:"base_url"`
	IsEnabled bool    `json:"is_enabled"`
}

// Catalog returns enabled providers (with API keys) bundled with their models.
// Used by the site creation modal to pick a model.
func (h *ProvidersHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	providers, err := models.ListProviders(h.deps.DB.DB)
	if err != nil {
		h.deps.Logger.Error("failed to list providers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list providers")
		return
	}

	var result []setupProviderResponse
	for _, p := range providers {
		if !p.IsEnabled || p.APIKeyEncrypted == nil {
			continue
		}

		requiresKey := true
		if p.Config != "" {
			var cfg map[string]interface{}
			if json.Unmarshal([]byte(p.Config), &cfg) == nil {
				if v, ok := cfg["requires_api_key"]; ok {
					if b, ok := v.(bool); ok {
						requiresKey = b
					}
				}
			}
		}

		dbModels, err := models.ListModelsByProvider(h.deps.DB.DB, p.ID)
		if err != nil {
			continue
		}

		var modelList []setupModelResponse
		for _, m := range dbModels {
			modelList = append(modelList, setupModelResponse{
				ID:            m.ID,
				ModelID:       m.ModelID,
				DisplayName:   m.DisplayName,
				IsDefault:     m.IsDefault,
				SupportsTools: m.SupportsTools,
			})
		}

		result = append(result, setupProviderResponse{
			ID:             p.ID,
			Name:           p.Name,
			ProviderType:   p.ProviderType,
			RequiresAPIKey: requiresKey,
			HasAPIKey:      true,
			Models:         modelList,
		})
	}

	if result == nil {
		result = []setupProviderResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

// List returns all providers.
func (h *ProvidersHandler) List(w http.ResponseWriter, r *http.Request) {
	providers, err := models.ListProviders(h.deps.DB.DB)
	if err != nil {
		h.deps.Logger.Error("failed to list providers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list providers")
		return
	}

	if providers == nil {
		providers = []models.LLMProvider{}
	}

	writeJSON(w, http.StatusOK, providers)
}

// Create creates a new LLM provider.
func (h *ProvidersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" || req.ProviderType == "" {
		writeError(w, http.StatusBadRequest, "name and provider_type are required")
		return
	}

	// OpenAI-compatible providers (Gemini, Z.ai, Ollama, etc.) require a base_url
	// so they don't silently route to api.openai.com.
	if req.ProviderType == "openai" && (req.BaseURL == nil || *req.BaseURL == "") {
		writeError(w, http.StatusBadRequest, "base_url is required for OpenAI-compatible providers")
		return
	}

	var encryptedKey *string
	if req.APIKey != "" {
		encrypted, err := h.deps.Encryptor.Encrypt(req.APIKey)
		if err != nil {
			h.deps.Logger.Error("failed to encrypt api key", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to encrypt api key")
			return
		}
		encryptedKey = &encrypted
	}

	provider, err := models.CreateProvider(h.deps.DB.DB, req.Name, req.ProviderType, encryptedKey, req.BaseURL)
	if err != nil {
		h.deps.Logger.Error("failed to create provider", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create provider")
		return
	}

	// Register in live LLM registry so Test and Brain work immediately.
	if req.APIKey != "" && h.deps.ProviderFactory != nil {
		var baseURLStr string
		if req.BaseURL != nil {
			baseURLStr = *req.BaseURL
		}
		liveProvider := h.deps.ProviderFactory(req.Name, req.ProviderType, req.APIKey, baseURLStr)
		if liveProvider != nil {
			h.deps.LLMRegistry.Register(req.Name, liveProvider)
		}
	}

	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventProviderAdded, 0, map[string]interface{}{
			"provider_id": provider.ID,
			"name":        provider.Name,
		}))
	}

	writeJSON(w, http.StatusCreated, provider)
}

// Get returns a single provider by ID.
func (h *ProvidersHandler) Get(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	provider, err := models.GetProviderByID(h.deps.DB.DB, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	writeJSON(w, http.StatusOK, provider)
}

// Update updates an existing provider.
func (h *ProvidersHandler) Update(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	var req updateProviderRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// Get the existing provider for re-registration.
	existing, err := models.GetProviderByID(h.deps.DB.DB, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	// OpenAI-compatible providers require a base_url.
	if existing.ProviderType == "openai" {
		effectiveBaseURL := req.BaseURL
		if effectiveBaseURL == nil {
			effectiveBaseURL = existing.BaseURL
		}
		if effectiveBaseURL == nil || *effectiveBaseURL == "" {
			writeError(w, http.StatusBadRequest, "base_url is required for OpenAI-compatible providers")
			return
		}
	}

	// If a new API key is provided, encrypt it; otherwise keep the existing one.
	var encryptedKey *string
	plainAPIKey := req.APIKey
	if plainAPIKey != "" {
		encrypted, err := h.deps.Encryptor.Encrypt(plainAPIKey)
		if err != nil {
			h.deps.Logger.Error("failed to encrypt api key", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to encrypt api key")
			return
		}
		encryptedKey = &encrypted
	} else {
		encryptedKey = existing.APIKeyEncrypted
	}

	if err := models.UpdateProvider(h.deps.DB.DB, providerID, req.Name, encryptedKey, req.BaseURL, req.IsEnabled); err != nil {
		h.deps.Logger.Error("failed to update provider", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update provider")
		return
	}

	provider, err := models.GetProviderByID(h.deps.DB.DB, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "provider updated but failed to reload")
		return
	}

	// Re-register in live LLM registry with updated credentials.
	if h.deps.ProviderFactory != nil {
		var apiKey string
		if plainAPIKey != "" {
			apiKey = plainAPIKey
		} else if existing.APIKeyEncrypted != nil {
			decrypted, decErr := h.deps.Encryptor.Decrypt(*existing.APIKeyEncrypted)
			if decErr == nil {
				apiKey = decrypted
			}
		}
		if apiKey != "" {
			var baseURLStr string
			if req.BaseURL != nil {
				baseURLStr = *req.BaseURL
			} else if provider.BaseURL != nil {
				baseURLStr = *provider.BaseURL
			}
			name := req.Name
			if name == "" {
				name = provider.Name
			}
			liveProvider := h.deps.ProviderFactory(name, provider.ProviderType, apiKey, baseURLStr)
			if liveProvider != nil {
				h.deps.LLMRegistry.Register(name, liveProvider)
			}
		}
	}

	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventProviderUpdated, 0, map[string]interface{}{
			"provider_id": provider.ID,
			"name":        provider.Name,
		}))
	}

	writeJSON(w, http.StatusOK, provider)
}

// Delete removes a provider.
func (h *ProvidersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	if err := models.DeleteProvider(h.deps.DB.DB, providerID); err != nil {
		h.deps.Logger.Error("failed to delete provider", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete provider")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Test tests connectivity to a provider by calling its Ping method.
func (h *ProvidersHandler) Test(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	dbProvider, err := models.GetProviderByID(h.deps.DB.DB, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	// Try to get the provider from the live registry.
	liveProvider, regErr := h.deps.LLMRegistry.Get(dbProvider.Name)
	if regErr != nil {
		// Provider not in live registry -- create on-the-fly from DB credentials.
		if dbProvider.APIKeyEncrypted == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   "no API key configured for this provider",
			})
			return
		}
		apiKey, decErr := h.deps.Encryptor.Decrypt(*dbProvider.APIKeyEncrypted)
		if decErr != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   "failed to decrypt API key",
			})
			return
		}
		if h.deps.ProviderFactory == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   "provider factory not available",
			})
			return
		}
		var baseURLStr string
		if dbProvider.BaseURL != nil {
			baseURLStr = *dbProvider.BaseURL
		}
		liveProvider = h.deps.ProviderFactory(dbProvider.Name, dbProvider.ProviderType, apiKey, baseURLStr)
		if liveProvider == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   "unsupported provider type: " + dbProvider.ProviderType,
			})
			return
		}
		// Cache it in the registry for future use.
		h.deps.LLMRegistry.Register(dbProvider.Name, liveProvider)
	}

	// Get the default model for this provider from DB
	dbModels, _ := models.ListModelsByProvider(h.deps.DB.DB, providerID)
	pingModel := ""
	for _, m := range dbModels {
		if m.IsDefault {
			pingModel = m.ModelID
			break
		}
	}
	if pingModel == "" && len(dbModels) > 0 {
		pingModel = dbModels[0].ModelID
	}
	if pingModel == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "no models configured for this provider",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := liveProvider.Ping(ctx, pingModel); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

// Models returns the models available for a provider.
func (h *ProvidersHandler) Models(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	dbModels, err := models.ListModelsByProvider(h.deps.DB.DB, providerID)
	if err != nil {
		h.deps.Logger.Error("failed to list models", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list models")
		return
	}

	if dbModels == nil {
		dbModels = []models.LLMModel{}
	}

	writeJSON(w, http.StatusOK, dbModels)
}

// CreateModel adds a new model to a provider.
func (h *ProvidersHandler) CreateModel(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	var req struct {
		ModelID           string `json:"model_id"`
		DisplayName       string `json:"display_name"`
		MaxTokens         int    `json:"max_tokens"`
		SupportsStreaming bool   `json:"supports_streaming"`
		SupportsTools     bool   `json:"supports_tools"`
		IsDefault         bool   `json:"is_default"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ModelID == "" {
		writeError(w, http.StatusBadRequest, "model_id is required")
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.ModelID
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	// If setting as default, clear other defaults for this provider first.
	if req.IsDefault {
		h.deps.DB.ExecWrite("UPDATE llm_models SET is_default = 0 WHERE provider_id = ?", providerID)
	}

	model, err := models.CreateModel(h.deps.DB.DB, providerID, req.ModelID, req.DisplayName, req.MaxTokens, req.SupportsStreaming, req.SupportsTools, req.IsDefault)
	if err != nil {
		h.deps.Logger.Error("failed to create model", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create model")
		return
	}

	writeJSON(w, http.StatusCreated, model)
}

// DeleteModel removes a model from a provider.
func (h *ProvidersHandler) DeleteModel(w http.ResponseWriter, r *http.Request) {
	modelID, err := strconv.Atoi(chi.URLParam(r, "modelID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid model ID")
		return
	}

	if err := models.DeleteModel(h.deps.DB.DB, modelID); err != nil {
		h.deps.Logger.Error("failed to delete model", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete model")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// SetDefaultModel sets a model as the default for its provider.
func (h *ProvidersHandler) SetDefaultModel(w http.ResponseWriter, r *http.Request) {
	providerID, err := strconv.Atoi(chi.URLParam(r, "providerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}

	modelID, err := strconv.Atoi(chi.URLParam(r, "modelID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid model ID")
		return
	}

	// Clear all defaults for this provider, then set the chosen one.
	if _, err := h.deps.DB.ExecWrite("UPDATE llm_models SET is_default = 0 WHERE provider_id = ?", providerID); err != nil {
		h.deps.Logger.Error("failed to clear default models", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update default model")
		return
	}

	if _, err := h.deps.DB.ExecWrite("UPDATE llm_models SET is_default = 1 WHERE id = ? AND provider_id = ?", modelID, providerID); err != nil {
		h.deps.Logger.Error("failed to set default model", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to set default model")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "default set"})
}
