/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package llm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/security"
)

// SeedConfig is the structure of the seed.json file.
type SeedConfig struct {
	Providers []SeedProvider `json:"providers"`
}

// SeedProvider describes a provider to seed during first run.
type SeedProvider struct {
	Name           string      `json:"name"`
	Type           string      `json:"type"`
	APIKeyEnv      string      `json:"api_key_env,omitempty"`
	APIKey         string      `json:"api_key,omitempty"`
	BaseURL        string      `json:"base_url,omitempty"`
	RequiresAPIKey *bool       `json:"requires_api_key,omitempty"`
	Models         []SeedModel `json:"models"`
}

// SeedModel describes a model to seed for a provider.
type SeedModel struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name"`
	MaxTokens         int    `json:"max_tokens"`
	SupportsStreaming bool   `json:"supports_streaming"`
	SupportsTools     bool   `json:"supports_tools"`
}

// ProviderFactory is a function that creates a Provider from seed configuration.
type ProviderFactory func(name, providerType, apiKey, baseURL string) Provider

// LoadSeedWithFactory reads a seed.json file and seeds the database,
// using the provided factory function to create live Provider instances.
func LoadSeedWithFactory(path string, db *sql.DB, enc *security.Encryptor, registry *Registry, factory ProviderFactory) error {
	count, err := models.CountProviders(db)
	if err != nil {
		return fmt.Errorf("seed: count providers: %w", err)
	}
	if count > 0 {
		slog.Debug("seed: skipping, providers already exist", "count", count)
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("seed: no seed file found", "path", path)
			return nil
		}
		return fmt.Errorf("seed: read file: %w", err)
	}

	var cfg SeedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("seed: parse config: %w", err)
	}

	slog.Info("seed: seeding providers", "count", len(cfg.Providers))

	// Run all DB writes in a single transaction so a partial failure rolls back.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("seed: begin transaction: %w", err)
	}
	defer tx.Rollback() // no-op after commit

	for _, fp := range cfg.Providers {
		// Resolve API key: prefer env var, then literal
		apiKey := fp.APIKey
		if fp.APIKeyEnv != "" {
			if envVal := os.Getenv(fp.APIKeyEnv); envVal != "" {
				apiKey = envVal
			}
		}

		// Encrypt the API key if present
		var encryptedKey *string
		if apiKey != "" {
			encrypted, err := enc.Encrypt(apiKey)
			if err != nil {
				return fmt.Errorf("seed: encrypt api key for %s: %w", fp.Name, err)
			}
			encryptedKey = &encrypted
		}

		var baseURL *string
		if fp.BaseURL != "" {
			baseURL = &fp.BaseURL
		}

		// Create the provider in the database
		provider, err := models.CreateProviderTx(tx, fp.Name, fp.Type, encryptedKey, baseURL)
		if err != nil {
			return fmt.Errorf("seed: create provider %s: %w", fp.Name, err)
		}

		// Store requires_api_key in the config JSON column
		requiresKey := true // default: most providers require a key
		if fp.RequiresAPIKey != nil {
			requiresKey = *fp.RequiresAPIKey
		}
		configJSON, _ := json.Marshal(map[string]interface{}{
			"requires_api_key": requiresKey,
		})
		tx.Exec("UPDATE llm_providers SET config = ? WHERE id = ?", string(configJSON), provider.ID)

		slog.Info("seed: created provider", "name", fp.Name, "type", fp.Type, "id", provider.ID)

		// Create models for this provider (none marked as default;
		// the setup wizard is responsible for setting the default).
		for _, fm := range fp.Models {
			_, err := models.CreateModelTx(
				tx, provider.ID,
				fm.ID, fm.DisplayName,
				fm.MaxTokens,
				fm.SupportsStreaming,
				fm.SupportsTools,
				false,
			)
			if err != nil {
				return fmt.Errorf("seed: create model %s for %s: %w", fm.ID, fp.Name, err)
			}
			slog.Debug("seed: created model", "model", fm.ID, "provider", fp.Name)
		}

		// Register the provider in the live registry if factory available and API key present (or not required)
		if (apiKey != "" || !requiresKey) && registry != nil && factory != nil {
			liveProvider := factory(fp.Name, strings.ToLower(fp.Type), apiKey, fp.BaseURL)
			if liveProvider != nil {
				registry.Register(fp.Name, liveProvider)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("seed: commit transaction: %w", err)
	}

	slog.Info("seed: seeding complete")
	return nil
}
