/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Executor is satisfied by both *sql.DB and *sql.Tx, allowing model
// functions to work inside or outside a transaction.
type Executor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Query(query string, args ...interface{}) (*sql.Rows, error)
}

type LLMProvider struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	ProviderType    string    `json:"provider_type"`
	APIKeyEncrypted *string   `json:"-"`
	BaseURL         *string   `json:"base_url"`
	IsEnabled       bool      `json:"is_enabled"`
	Config          string    `json:"config"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RequiresAPIKey returns whether this provider needs an API key.
// Defaults to true if the config JSON is missing or unparseable.
func (p *LLMProvider) RequiresAPIKey() bool {
	if p.Config == "" {
		return true
	}
	var cfg struct {
		RequiresAPIKey *bool `json:"requires_api_key"`
	}
	if json.Unmarshal([]byte(p.Config), &cfg) != nil || cfg.RequiresAPIKey == nil {
		return true
	}
	return *cfg.RequiresAPIKey
}

type LLMModel struct {
	ID                int       `json:"id"`
	ProviderID        int       `json:"provider_id"`
	ModelID           string    `json:"model_id"`
	DisplayName       string    `json:"display_name"`
	MaxTokens         int       `json:"max_tokens"`
	SupportsStreaming bool      `json:"supports_streaming"`
	SupportsTools     bool      `json:"supports_tools"`
	IsDefault         bool      `json:"is_default"`
	Config            string    `json:"config"`
	CreatedAt         time.Time `json:"created_at"`
}

func CreateProvider(db *sql.DB, name, providerType string, apiKeyEncrypted *string, baseURL *string) (*LLMProvider, error) {
	return CreateProviderTx(db, name, providerType, apiKeyEncrypted, baseURL)
}

// CreateProviderTx creates a provider using any Executor (DB or Tx).
func CreateProviderTx(exec Executor, name, providerType string, apiKeyEncrypted *string, baseURL *string) (*LLMProvider, error) {
	result, err := exec.Exec(
		"INSERT INTO llm_providers (name, provider_type, api_key_encrypted, base_url) VALUES (?, ?, ?, ?)",
		name, providerType, apiKeyEncrypted, baseURL,
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	p := &LLMProvider{}
	err = exec.QueryRow(
		"SELECT id, name, provider_type, api_key_encrypted, base_url, is_enabled, config, created_at, updated_at FROM llm_providers WHERE id = ?",
		int(id),
	).Scan(&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func GetProviderByID(db *sql.DB, id int) (*LLMProvider, error) {
	p := &LLMProvider{}
	err := db.QueryRow(
		"SELECT id, name, provider_type, api_key_encrypted, base_url, is_enabled, config, created_at, updated_at FROM llm_providers WHERE id = ?",
		id,
	).Scan(&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func GetProviderByName(db *sql.DB, name string) (*LLMProvider, error) {
	p := &LLMProvider{}
	err := db.QueryRow(
		"SELECT id, name, provider_type, api_key_encrypted, base_url, is_enabled, config, created_at, updated_at FROM llm_providers WHERE name = ?",
		name,
	).Scan(&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func ListProviders(db *sql.DB) ([]LLMProvider, error) {
	rows, err := db.Query(
		"SELECT id, name, provider_type, api_key_encrypted, base_url, is_enabled, config, created_at, updated_at FROM llm_providers ORDER BY id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []LLMProvider
	for rows.Next() {
		var p LLMProvider
		if err := rows.Scan(&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, nil
}

func CountProviders(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM llm_providers").Scan(&count)
	return count, err
}

func UpdateProvider(db *sql.DB, id int, name string, apiKeyEncrypted *string, baseURL *string, isEnabled bool) error {
	_, err := db.Exec(
		"UPDATE llm_providers SET name = ?, api_key_encrypted = ?, base_url = ?, is_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		name, apiKeyEncrypted, baseURL, isEnabled, id,
	)
	return err
}

func DeleteProvider(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM llm_providers WHERE id = ?", id)
	return err
}

// Model operations

func CreateModel(db *sql.DB, providerID int, modelID, displayName string, maxTokens int, supportsStreaming, supportsTools, isDefault bool) (*LLMModel, error) {
	return CreateModelTx(db, providerID, modelID, displayName, maxTokens, supportsStreaming, supportsTools, isDefault)
}

// CreateModelTx creates a model using any Executor (DB or Tx).
func CreateModelTx(exec Executor, providerID int, modelID, displayName string, maxTokens int, supportsStreaming, supportsTools, isDefault bool) (*LLMModel, error) {
	result, err := exec.Exec(
		"INSERT INTO llm_models (provider_id, model_id, display_name, max_tokens, supports_streaming, supports_tools, is_default) VALUES (?, ?, ?, ?, ?, ?, ?)",
		providerID, modelID, displayName, maxTokens, supportsStreaming, supportsTools, isDefault,
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	return &LLMModel{
		ID: int(id), ProviderID: providerID, ModelID: modelID,
		DisplayName: displayName, MaxTokens: maxTokens,
		SupportsStreaming: supportsStreaming, SupportsTools: supportsTools,
		IsDefault: isDefault,
	}, nil
}

func ListModelsByProvider(db *sql.DB, providerID int) ([]LLMModel, error) {
	rows, err := db.Query(
		"SELECT id, provider_id, model_id, display_name, max_tokens, supports_streaming, supports_tools, is_default, config, created_at FROM llm_models WHERE provider_id = ? ORDER BY id",
		providerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []LLMModel
	for rows.Next() {
		var m LLMModel
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.DisplayName, &m.MaxTokens, &m.SupportsStreaming, &m.SupportsTools, &m.IsDefault, &m.Config, &m.CreatedAt); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, nil
}

func DeleteModel(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM llm_models WHERE id = ?", id)
	return err
}

// GetModelByID returns a single model by its primary key.
func GetModelByID(db *sql.DB, id int) (*LLMModel, error) {
	m := &LLMModel{}
	err := db.QueryRow(
		`SELECT id, provider_id, model_id, display_name, max_tokens, supports_streaming, supports_tools, is_default, config, created_at
		 FROM llm_models WHERE id = ?`, id,
	).Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.DisplayName, &m.MaxTokens, &m.SupportsStreaming, &m.SupportsTools, &m.IsDefault, &m.Config, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func GetDefaultModel(db *sql.DB) (*LLMModel, *LLMProvider, error) {
	m := &LLMModel{}
	p := &LLMProvider{}
	err := db.QueryRow(`
		SELECT m.id, m.provider_id, m.model_id, m.display_name, m.max_tokens, m.supports_streaming, m.supports_tools, m.is_default, m.config, m.created_at,
		       p.id, p.name, p.provider_type, p.api_key_encrypted, p.base_url, p.is_enabled, p.config, p.created_at, p.updated_at
		FROM llm_models m
		JOIN llm_providers p ON p.id = m.provider_id
		WHERE m.is_default = 1 AND p.is_enabled = 1
		ORDER BY m.id ASC
		LIMIT 1
	`).Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.DisplayName, &m.MaxTokens, &m.SupportsStreaming, &m.SupportsTools, &m.IsDefault, &m.Config, &m.CreatedAt,
		&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	return m, p, nil
}

// GetModelForSite returns the model and provider assigned to a site.
func GetModelForSite(db *sql.DB, siteID int) (*LLMModel, *LLMProvider, error) {
	m := &LLMModel{}
	p := &LLMProvider{}
	err := db.QueryRow(`
		SELECT m.id, m.provider_id, m.model_id, m.display_name, m.max_tokens, m.supports_streaming, m.supports_tools, m.is_default, m.config, m.created_at,
		       p.id, p.name, p.provider_type, p.api_key_encrypted, p.base_url, p.is_enabled, p.config, p.created_at, p.updated_at
		FROM sites s
		JOIN llm_models m ON m.id = s.llm_model_id
		JOIN llm_providers p ON p.id = m.provider_id
		WHERE s.id = ?
	`, siteID).Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.DisplayName, &m.MaxTokens, &m.SupportsStreaming, &m.SupportsTools, &m.IsDefault, &m.Config, &m.CreatedAt,
		&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	return m, p, nil
}

// GetFallbackModel returns an alternative enabled model on a different provider
// than the one specified by excludeProviderID. Prefers tool-capable models.
// Returns sql.ErrNoRows if no suitable fallback exists.
func GetFallbackModel(db *sql.DB, excludeProviderID int) (*LLMModel, *LLMProvider, error) {
	m := &LLMModel{}
	p := &LLMProvider{}
	err := db.QueryRow(`
		SELECT m.id, m.provider_id, m.model_id, m.display_name, m.max_tokens, m.supports_streaming, m.supports_tools, m.is_default, m.config, m.created_at,
		       p.id, p.name, p.provider_type, p.api_key_encrypted, p.base_url, p.is_enabled, p.config, p.created_at, p.updated_at
		FROM llm_models m
		JOIN llm_providers p ON p.id = m.provider_id
		WHERE p.is_enabled = 1 AND p.id != ? AND m.supports_tools = 1
		ORDER BY m.is_default DESC, m.id ASC
		LIMIT 1
	`, excludeProviderID).Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.DisplayName, &m.MaxTokens, &m.SupportsStreaming, &m.SupportsTools, &m.IsDefault, &m.Config, &m.CreatedAt,
		&p.ID, &p.Name, &p.ProviderType, &p.APIKeyEncrypted, &p.BaseURL, &p.IsEnabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	return m, p, nil
}
