/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Shared model picker — provider + model select pair.
 */

import { h } from '../core/dom.js';

/**
 * Create a provider/model picker from a provider catalog.
 * @param {Array} providers - Provider catalog from /admin/api/providers/catalog.
 * @param {Object} [opts]
 * @param {number} [opts.currentModelId] - Pre-select the provider/model matching this ID.
 * @param {boolean} [opts.disabled] - Disable both selects.
 * @returns {{ providerSelect: HTMLSelectElement, modelSelect: HTMLSelectElement, getSelectedModel: () => Object|null }}
 */
export function createModelPicker(providers, opts = {}) {
  const { currentModelId = null, disabled = false } = opts;

  let selectedProvider = null;
  let selectedModel = null;

  const providerSelect = h('select', { className: 'input', disabled });
  const modelSelect = h('select', { className: 'input', disabled });

  providers.forEach((p) => {
    providerSelect.appendChild(h('option', { value: String(p.id) }, p.name));
  });

  function updateModels() {
    modelSelect.innerHTML = '';
    selectedModel = null;
    if (!selectedProvider || !selectedProvider.models) return;

    selectedProvider.models.forEach((m) => {
      const opt = h('option', { value: String(m.id) }, m.display_name);
      const shouldSelect = currentModelId ? m.id === currentModelId : m.is_default;
      if (shouldSelect) {
        opt.selected = true;
        selectedModel = m;
      }
      modelSelect.appendChild(opt);
    });

    if (!selectedModel && selectedProvider.models.length > 0) {
      selectedModel = selectedProvider.models[0];
      modelSelect.options[0].selected = true;
    }
  }

  // Find initial provider
  if (currentModelId) {
    for (const p of providers) {
      if (p.models && p.models.some(m => m.id === currentModelId)) {
        selectedProvider = p;
        break;
      }
    }
  }
  if (!selectedProvider && providers.length > 0) {
    selectedProvider = providers[0];
  }
  if (selectedProvider) {
    providerSelect.value = String(selectedProvider.id);
  }

  providerSelect.addEventListener('change', () => {
    const id = parseInt(providerSelect.value, 10);
    selectedProvider = providers.find(p => p.id === id) || null;
    updateModels();
  });

  modelSelect.addEventListener('change', () => {
    if (!selectedProvider) return;
    const mid = parseInt(modelSelect.value, 10);
    selectedModel = selectedProvider.models.find(m => m.id === mid) || null;
  });

  updateModels();

  return {
    providerSelect,
    modelSelect,
    getSelectedModel() { return selectedModel; },
  };
}
