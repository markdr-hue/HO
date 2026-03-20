/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Setup Step 2: Create admin account.
 */

import { h } from '../../core/dom.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';

export function renderAccount(container, setupData, onNext) {
  const usernameInput = h('input', {
    className: 'input',
    type: 'text',
    placeholder: 'admin',
    value: setupData.username || '',
    autocomplete: 'username',
  });

  const passwordInput = h('input', {
    className: 'input',
    type: 'password',
    placeholder: 'Choose a password',
    autocomplete: 'new-password',
  });

  const confirmInput = h('input', {
    className: 'input',
    type: 'password',
    placeholder: 'Confirm password',
    autocomplete: 'new-password',
  });

  // Password visibility toggles
  function createToggle(input) {
    let visible = false;
    const btn = h('button', {
      className: 'password-toggle',
      type: 'button',
      innerHTML: icon('eye-off'),
      tabIndex: -1,
      onClick: (e) => {
        e.preventDefault();
        visible = !visible;
        input.type = visible ? 'text' : 'password';
        btn.innerHTML = icon(visible ? 'eye' : 'eye-off');
      },
    });
    return btn;
  }

  const pwdWrapper = h('div', { className: 'password-wrapper' }, [
    passwordInput,
    createToggle(passwordInput),
  ]);

  const confirmWrapper = h('div', { className: 'password-wrapper' }, [
    confirmInput,
    createToggle(confirmInput),
  ]);

  // Strength meter bars
  const bars = [0, 1, 2, 3].map(() => h('div', { className: 'password-strength__bar' }));
  const strengthMeter = h('div', { className: 'password-strength' }, bars);
  const strengthText = h('div', { className: 'form-hint' });

  // Interactive requirements checklist
  const REQS = [
    { key: 'length', label: '8+ chars', test: (p) => p.length >= 8 },
    { key: 'upper', label: 'Uppercase', test: (p) => /[A-Z]/.test(p) },
    { key: 'number', label: 'Number', test: (p) => /[0-9]/.test(p) },
    { key: 'special', label: 'Special', test: (p) => /[^A-Za-z0-9]/.test(p) },
  ];

  const reqEls = REQS.map((req) => {
    const iconEl = h('span', { className: 'password-req__icon' });
    const el = h('div', { className: 'password-req', 'data-req': req.key }, [
      iconEl,
      h('span', {}, req.label),
    ]);
    return { el, iconEl, test: req.test };
  });

  const reqList = h('div', { className: 'password-reqs' }, reqEls.map(r => r.el));

  function checkStrength(pwd) {
    let score = 0;

    reqEls.forEach((req) => {
      const met = req.test(pwd);
      if (met) score++;
      req.el.className = `password-req${met ? ' met' : ''}`;
      req.iconEl.innerHTML = met ? icon('check') : '';
    });

    bars.forEach((bar, i) => {
      bar.className = 'password-strength__bar';
      if (i < score) {
        if (score <= 1) bar.classList.add('filled');
        else if (score <= 2) bar.classList.add('medium');
        else bar.classList.add('strong');
      }
    });

    const labels = ['', 'Weak', 'Fair', 'Good', 'Strong'];
    strengthText.textContent = pwd ? labels[score] || '' : '';

    return score;
  }

  passwordInput.addEventListener('input', () => {
    checkStrength(passwordInput.value);
  });

  function validate() {
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    const confirm = confirmInput.value;

    if (!username) {
      toast.warning('Username is required');
      usernameInput.focus();
      return false;
    }
    if (password.length < 8) {
      toast.warning('Password must be at least 8 characters');
      passwordInput.focus();
      return false;
    }
    if (!/[A-Z]/.test(password)) {
      toast.warning('Password needs an uppercase letter');
      passwordInput.focus();
      return false;
    }
    if (!/[0-9]/.test(password)) {
      toast.warning('Password needs a number');
      passwordInput.focus();
      return false;
    }
    if (!/[^A-Za-z0-9]/.test(password)) {
      toast.warning('Password needs a special character');
      passwordInput.focus();
      return false;
    }
    if (password !== confirm) {
      toast.warning('Passwords don\u2019t match');
      confirmInput.focus();
      return false;
    }
    return true;
  }

  function submit() {
    if (validate()) {
      setupData.username = usernameInput.value.trim();
      setupData.password = passwordInput.value;
      onNext();
    }
  }

  // Enter key on any input submits
  function onKey(e) {
    if (e.key === 'Enter') {
      e.preventDefault();
      submit();
    }
  }
  usernameInput.addEventListener('keydown', onKey);
  passwordInput.addEventListener('keydown', onKey);
  confirmInput.addEventListener('keydown', onKey);

  const greeting = setupData.displayName
    ? `Nice to meet you ${setupData.displayName}`
    : 'Create your admin account';

  const content = h('div', {}, [
    h('div', { className: 'setup-card__header' }, [
      h('div', { className: 'setup-card__icon', innerHTML: icon('user') }),
      h('h2', { className: 'setup-card__title' }, greeting),
      h('p', { className: 'setup-card__desc' }, 'This is your admin login for managing everything.'),
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, ['Username', h('span', { className: 'required' }, ' *')]),
      usernameInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, ['Password', h('span', { className: 'required' }, ' *')]),
      pwdWrapper,
      strengthMeter,
      strengthText,
      reqList,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, ['Confirm Password', h('span', { className: 'required' }, ' *')]),
      confirmWrapper,
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
  usernameInput.focus();
}
