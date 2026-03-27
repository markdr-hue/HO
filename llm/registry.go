/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package llm

import (
	"fmt"
	"sync"
)

// Registry is a thread-safe registry of LLM providers.
type Registry struct {
	mu          sync.RWMutex
	providers   map[string]Provider
	defaultName string
}

// NewRegistry creates a new empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry. The first provider registered
// becomes the default unless overridden by SetDefault.
func (r *Registry) Register(name string, p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[name] = p
	if r.defaultName == "" {
		r.defaultName = name
	}
}

// Get returns a provider by name, or an error if not found.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("llm: provider %q not found", name)
	}
	return p, nil
}

// List returns the names of all registered providers.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// Default returns the default provider, or an error if none is registered.
func (r *Registry) Default() (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.defaultName == "" {
		return nil, fmt.Errorf("llm: no default provider set")
	}

	p, ok := r.providers[r.defaultName]
	if !ok {
		return nil, fmt.Errorf("llm: default provider %q not found", r.defaultName)
	}
	return p, nil
}

// SetDefault sets the default provider by name.
func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.providers[name]; !ok {
		return fmt.Errorf("llm: provider %q not found", name)
	}
	r.defaultName = name
	return nil
}
