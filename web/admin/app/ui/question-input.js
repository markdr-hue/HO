/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Shared question input builder.
 * Renders the appropriate input UI for a question (fields, multiple-choice,
 * single-choice, open text, secret) and returns getValue/hasValue closures.
 *
 * Used by: chat cards (cards.js), site questions, and global questions views.
 */

import { h } from '../core/dom.js';

/**
 * Parse a JSON-or-string field into an array, with fallback for legacy
 * comma-separated strings on options.
 */
function parseJsonArray(raw, commaFallback = false) {
  if (!raw) return [];
  try {
    const parsed = typeof raw === 'string' ? JSON.parse(raw) : raw;
    if (Array.isArray(parsed)) return parsed;
  } catch {
    if (commaFallback && typeof raw === 'string') {
      return raw.split(',').map(s => s.trim()).filter(Boolean);
    }
  }
  return [];
}

/**
 * Build the input UI for a question.
 *
 * @param {Object} questionData — { fields, options, type, secret_name }
 * @param {Object} [opts]
 * @param {Function} [opts.onInput] — called whenever the user changes a value
 * @param {string} [opts.wrapClass] — CSS class for the outer wrapper div
 * @returns {{ inputEl: HTMLElement, getValue: () => string, hasValue: () => boolean }}
 */
export function buildQuestionInput(questionData, { onInput, wrapClass } = {}) {
  const { fields, options, type, secret_name } = questionData;
  const isSecret = type === 'secret';

  const parsedFields = parseJsonArray(fields);
  const parsedOpts = parseJsonArray(options, true);

  const inputEl = h('div', wrapClass ? { className: wrapClass } : {});
  let getValue, hasValue;

  if (parsedFields.length > 0) {
    // Multi-field structured input
    const fieldInputs = {};
    for (const field of parsedFields) {
      const input = h('input', {
        type: field.type === 'secret' ? 'password' : 'text',
        className: 'input input--sm',
        placeholder: field.label || field.name,
        onInput: () => onInput?.(),
      });
      fieldInputs[field.name] = input;
      inputEl.appendChild(h('div', { className: 'chat-card__field-row' }, [
        h('label', { className: 'chat-card__field-label' }, field.label || field.name),
        input,
      ]));
    }
    getValue = () => {
      const values = {};
      let has = false;
      for (const field of parsedFields) {
        const val = fieldInputs[field.name].value.trim();
        if (val) has = true;
        values[field.name] = val;
      }
      return has ? JSON.stringify(values) : '';
    };
    hasValue = () => parsedFields.some(f => fieldInputs[f.name].value.trim());

  } else if (parsedOpts.length > 0 && type === 'multiple_choice') {
    // Multiple choice toggle buttons
    inputEl.appendChild(h('p', { className: 'text-sm text-secondary', style: { margin: '0 0 8px' } }, 'You can select multiple answers'));
    const selected = new Set();
    const optContainer = h('div', { className: 'chat-card__options' });
    for (const opt of parsedOpts) {
      const label = typeof opt === 'string' ? opt : opt.label || opt;
      const btn = h('button', {
        className: 'btn btn--sm btn--ghost chat-card__option-btn',
        onClick: () => {
          if (selected.has(label)) { selected.delete(label); btn.classList.remove('btn--active'); }
          else { selected.add(label); btn.classList.add('btn--active'); }
          onInput?.();
        },
      }, label);
      optContainer.appendChild(btn);
    }
    inputEl.appendChild(optContainer);
    getValue = () => selected.size > 0 ? [...selected].join(', ') : '';
    hasValue = () => selected.size > 0;

  } else if (parsedOpts.length > 0) {
    // Single choice buttons
    let selectedValue = '';
    const allBtns = [];
    const optContainer = h('div', { className: 'chat-card__options' });
    for (const opt of parsedOpts) {
      const label = typeof opt === 'string' ? opt : opt.label || opt;
      const btn = h('button', {
        className: 'btn btn--sm btn--ghost chat-card__option-btn',
        onClick: () => {
          allBtns.forEach(b => b.classList.remove('btn--active'));
          btn.classList.add('btn--active');
          selectedValue = label;
          onInput?.();
        },
      }, label);
      allBtns.push(btn);
      optContainer.appendChild(btn);
    }
    inputEl.appendChild(optContainer);
    getValue = () => selectedValue;
    hasValue = () => selectedValue !== '';

  } else {
    // Open text / secret input
    if (isSecret && secret_name) {
      inputEl.appendChild(h('p', { className: 'text-sm text-secondary', style: { opacity: 0.7, marginBottom: '4px' } },
        `This value will be encrypted as '${secret_name}'.`));
    }
    const input = h('input', {
      type: isSecret ? 'password' : 'text',
      className: 'input input--sm',
      placeholder: isSecret ? 'Enter secret value...' : 'Type your answer...',
      onInput: () => onInput?.(),
    });
    inputEl.appendChild(input);
    getValue = () => input.value.trim();
    hasValue = () => input.value.trim() !== '';
  }

  return { inputEl, getValue, hasValue };
}
