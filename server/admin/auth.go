/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/security"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	deps *Deps
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

// HandleLogin validates credentials and returns a JWT.
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	user, err := models.GetUserByUsername(h.deps.DB.DB, req.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !security.CheckPassword(req.Password, user.PasswordHash) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := h.deps.JWTManager.Generate(user.ID, user.Username, user.Role)
	if err != nil {
		h.deps.Logger.Error("failed to generate token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	h.deps.JWTManager.SetAuthCookie(w, token)

	writeJSON(w, http.StatusOK, loginResponse{
		Token: token,
		User:  *user,
	})
}

type setupRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
	ProviderID  int    `json:"provider_id,omitempty"`
	ModelID     string `json:"model_id,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
}

type setupResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

// HandleSetupCheck returns whether first-time setup is needed (no users exist).
// This endpoint is public (no JWT required) so the frontend can detect first-run.
func (h *AuthHandler) HandleSetupCheck(w http.ResponseWriter, r *http.Request) {
	count, err := models.CountUsers(h.deps.DB.DB)
	if err != nil {
		h.deps.Logger.Error("setup check: failed to count users", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": count == 0})
}

// setupProviderResponse is the shape returned by HandleProviderCatalog and Catalog.
type setupProviderResponse struct {
	ID             int                  `json:"id"`
	Name           string               `json:"name"`
	ProviderType   string               `json:"provider_type"`
	RequiresAPIKey bool                 `json:"requires_api_key"`
	HasAPIKey      bool                 `json:"has_api_key"`
	Models         []setupModelResponse `json:"models"`
}

type setupModelResponse struct {
	ID            int    `json:"id"`
	ModelID       string `json:"model_id"`
	DisplayName   string `json:"display_name"`
	IsDefault     bool   `json:"is_default"`
	SupportsTools bool   `json:"supports_tools"`
}

// HandleProviderCatalog returns providers and their models from the database
// for the setup wizard. This endpoint is public (no JWT required).
func (h *AuthHandler) HandleProviderCatalog(w http.ResponseWriter, r *http.Request) {
	providers, err := models.ListProviders(h.deps.DB.DB)
	if err != nil {
		h.deps.Logger.Error("setup providers: failed to list", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list providers")
		return
	}

	var result []setupProviderResponse
	for _, p := range providers {
		// Parse requires_api_key from the config JSON column
		requiresKey := true // default
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

		// Get models for this provider
		dbModels, err := models.ListModelsByProvider(h.deps.DB.DB, p.ID)
		if err != nil {
			h.deps.Logger.Error("setup providers: failed to list models", "provider", p.Name, "error", err)
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
			HasAPIKey:      p.APIKeyEncrypted != nil,
			Models:         modelList,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

// HandleSetup creates the initial admin user during first-run setup.
// Optionally updates an existing DB provider with the user's API key.
func (h *AuthHandler) HandleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	// Hash the password before acquiring the write lock (bcrypt is slow).
	hash, err := security.HashPassword(req.Password)
	if err != nil {
		h.deps.Logger.Error("setup: failed to hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// Run all setup DB writes in a single transaction.
	// The user-count check is INSIDE the transaction to prevent a TOCTOU race
	// where concurrent requests both see count==0 and create duplicate admins.
	tx, err := h.deps.DB.BeginWriteTx()
	if err != nil {
		h.deps.Logger.Error("setup: failed to begin transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		tx.Rollback()
		h.deps.Logger.Error("setup: failed to count users", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count > 0 {
		tx.Rollback()
		writeError(w, http.StatusConflict, "setup already completed")
		return
	}

	user, err := models.CreateUserTx(tx, req.Username, hash, "admin", req.DisplayName)
	if err != nil {
		tx.Rollback()
		h.deps.Logger.Error("setup: failed to create user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// Optionally configure the selected provider with an API key.
	if req.ProviderID > 0 {
		provider, pErr := models.GetProviderByID(h.deps.DB.DB, req.ProviderID)
		if pErr != nil {
			h.deps.Logger.Warn("setup: provider not found", "id", req.ProviderID, "error", pErr)
		} else {
			// Update API key if provided
			var encryptedKey *string
			if req.APIKey != "" {
				encrypted, encErr := h.deps.Encryptor.Encrypt(req.APIKey)
				if encErr != nil {
					h.deps.Logger.Error("setup: failed to encrypt api key", "error", encErr)
				} else {
					encryptedKey = &encrypted
				}
			}

			// Enable the provider and set the API key
			if _, err := tx.Exec(
				"UPDATE llm_providers SET name = ?, api_key_encrypted = ?, base_url = ?, is_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
				provider.Name, encryptedKey, provider.BaseURL, true, provider.ID,
			); err != nil {
				h.deps.Logger.Error("setup: failed to update provider", "error", err)
			}

			// Clear is_default on ALL models first, then set the chosen one.
			if _, err := tx.Exec("UPDATE llm_models SET is_default = 0"); err != nil {
				tx.Rollback()
				h.deps.Logger.Error("setup: failed to clear model defaults", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to configure provider")
				return
			}
			if req.ModelID != "" {
				if _, err := tx.Exec(
					"UPDATE llm_models SET is_default = 1 WHERE model_id = ? AND provider_id = ?",
					req.ModelID, provider.ID,
				); err != nil {
					tx.Rollback()
					h.deps.Logger.Error("setup: failed to set default model", "error", err)
					writeError(w, http.StatusInternalServerError, "failed to configure provider")
					return
				}
			} else {
				// No specific model chosen — default the first model of this provider.
				if _, err := tx.Exec(
					"UPDATE llm_models SET is_default = 1 WHERE id = (SELECT id FROM llm_models WHERE provider_id = ? ORDER BY id LIMIT 1)",
					provider.ID,
				); err != nil {
					tx.Rollback()
					h.deps.Logger.Error("setup: failed to set default model", "error", err)
					writeError(w, http.StatusInternalServerError, "failed to configure provider")
					return
				}
			}

			h.deps.Logger.Info("setup: configured provider",
				"provider", provider.Name,
				"id", strconv.Itoa(provider.ID),
			)

			// Register the provider in the live LLM registry (after commit).
			defer func() {
				if req.APIKey != "" && h.deps.ProviderFactory != nil {
					var baseURLStr string
					if provider.BaseURL != nil {
						baseURLStr = *provider.BaseURL
					}
					liveProvider := h.deps.ProviderFactory(provider.Name, provider.ProviderType, req.APIKey, baseURLStr)
					if liveProvider != nil {
						h.deps.LLMRegistry.Register(provider.Name, liveProvider)
						h.deps.Logger.Info("setup: registered provider in live registry", "provider", provider.Name)
					}
				}
			}()
		}
	}

	if err := tx.Commit(); err != nil {
		h.deps.Logger.Error("setup: failed to commit transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to complete setup")
		return
	}

	// Generate a token for immediate login.
	token, err := h.deps.JWTManager.Generate(user.ID, user.Username, user.Role)
	if err != nil {
		h.deps.Logger.Error("setup: failed to generate token", "error", err)
		writeError(w, http.StatusInternalServerError, "user created but failed to generate token")
		return
	}

	h.deps.Logger.Info("setup completed", "username", user.Username)
	h.deps.JWTManager.SetAuthCookie(w, token)

	writeJSON(w, http.StatusCreated, setupResponse{
		Token: token,
		User:  *user,
	})
}

// HandleLogout clears the auth cookie.
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	security.ClearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
