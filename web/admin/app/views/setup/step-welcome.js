/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Setup Step 1: Welcome splash with name input.
 */

import { h } from '../../core/dom.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';

export function renderWelcome(container, setupData, onNext) {
  function submit() {
    const name = nameInput.value.trim();
    if (!name) {
      toast.warning('Please enter your name');
      nameInput.focus();
      return;
    }
    setupData.displayName = name;
    onNext();
  }

  const nameInput = h('input', {
    className: 'input',
    type: 'text',
    placeholder: 'Your first name',
    value: setupData.displayName || '',
    style: { textAlign: 'center', maxWidth: '280px', margin: '0 auto' },
    onkeydown: (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        submit();
      }
    },
  });

  const content = h('div', {}, [
    h('div', { className: 'setup-card__header' }, [
      h('div', { className: 'setup-card__icon', innerHTML: icon('zap') }),
      h('h2', { className: 'setup-card__title' }, 'Welcome to HO'),
      h('p', { className: 'setup-card__desc' },
        'One binary. Describe what you want. Done.'
      ),
    ]),
    h('div', { className: 'form-group text-center mt-6' }, [
      h('label', { style: { marginBottom: '8px', display: 'block' } }, [
        'What should I call you?',
        h('span', { className: 'required' }, ' *'),
      ]),
      nameInput,
    ]),
    h('div', { className: 'setup-actions setup-actions--center' }, [
      h('button', {
        className: 'btn btn--primary btn--lg',
        onClick: submit,
      }, 'Continue'),
    ]),
    h('p', { className: 'setup-hint' }, 'Press Enter to continue'),
  ]);

  container.appendChild(content);
  nameInput.focus();
}
