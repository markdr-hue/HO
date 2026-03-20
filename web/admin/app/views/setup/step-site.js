/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Setup Step 4: Create first site.
 */

import { h } from '../../core/dom.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';

export function renderSite(container, setupData, onNext) {
  const nameInput = h('input', {
    className: 'input',
    placeholder: 'My Project',
    value: setupData.siteName || '',
  });

  const domainInput = h('input', {
    className: 'input',
    placeholder: 'e.g. mysite.com (or mysite.localhost)',
    value: setupData.siteDomain || '',
  });

  const charCount = h('div', { className: 'char-count' }, '0 characters');

  const descInput = h('textarea', {
    className: 'input',
    placeholder: 'Tell me about your project. What it\'s for, how it should look, what features you need...',
    rows: '5',
    value: setupData.siteDescription || '',
    oninput: () => {
      const len = descInput.value.length;
      charCount.textContent = `${len} character${len !== 1 ? 's' : ''}`;
    },
  });

  // Initialize char count if there's existing data
  if (setupData.siteDescription) {
    charCount.textContent = `${setupData.siteDescription.length} characters`;
  }

  function submit() {
    const name = nameInput.value.trim();
    if (!name) {
      toast.warning('Give your project a name');
      nameInput.focus();
      return;
    }
    setupData.siteName = name;
    setupData.siteDomain = domainInput.value.trim();
    setupData.siteDescription = descInput.value.trim();
    onNext();
  }

  // Enter on single-line inputs submits
  function onKey(e) {
    if (e.key === 'Enter') {
      e.preventDefault();
      submit();
    }
  }
  nameInput.addEventListener('keydown', onKey);
  domainInput.addEventListener('keydown', onKey);
  // Textarea: Ctrl/Cmd+Enter to submit (regular Enter adds newline)
  descInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      submit();
    }
  });

  const title = setupData.displayName
    ? `What are we building, ${setupData.displayName}?`
    : 'What are we building?';

  const content = h('div', {}, [
    h('div', { className: 'setup-card__header' }, [
      h('div', { className: 'setup-card__icon', innerHTML: icon('globe') }),
      h('h2', { className: 'setup-card__title' }, title),
      h('p', { className: 'setup-card__desc' }, 'Just a name and a rough idea \u2014 you can always change things later.'),
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, ['Project Name', h('span', { className: 'required' }, ' *')]),
      nameInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Domain'),
      domainInput,
      h('p', { className: 'form-hint' }, 'Optional \u2014 you can configure this later.'),
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Description'),
      descInput,
      charCount,
      h('p', { className: 'form-hint' }, 'Describe it however you want, like you\u2019re telling a friend.'),
    ]),
    h('div', { className: 'setup-actions setup-actions--center' }, [
      h('button', {
        className: 'btn btn--primary btn--lg',
        onClick: submit,
      }, 'Launch'),
    ]),
    h('p', { className: 'setup-hint' }, 'Press Enter to launch'),
  ]);

  container.appendChild(content);
  nameInput.focus();
}
