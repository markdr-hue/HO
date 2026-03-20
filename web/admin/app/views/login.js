/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Login page view.
 */

import { h, render } from '../core/dom.js';
import { post } from '../core/http.js';
import { navigate } from '../core/router.js';

import * as toast from '../ui/toast.js';

export function renderLogin(container) {
  const usernameInput = h('input', {
    className: 'input',
    type: 'text',
    placeholder: 'Username',
    autocomplete: 'username',
  });

  const passwordInput = h('input', {
    className: 'input',
    type: 'password',
    placeholder: 'Password',
    autocomplete: 'current-password',
  });

  const submitBtn = h('button', {
    className: 'btn btn--primary w-full',
    type: 'submit',
  }, 'Sign in');

  const form = h('form', {
    onSubmit: async (e) => {
      e.preventDefault();
      submitBtn.disabled = true;
      submitBtn.textContent = 'Signing in...';

      try {
        const data = await post('/admin/api/auth/login', {
          username: usernameInput.value.trim(),
          password: passwordInput.value,
        });

        localStorage.setItem('ho_token', data.token);
        localStorage.setItem('ho_user', JSON.stringify(data.user));
        navigate('/dashboard');
        // Force full reload to re-init app shell
        window.location.reload();
      } catch (err) {
        toast.error(err.message || 'Login failed');
        submitBtn.disabled = false;
        submitBtn.textContent = 'Sign in';
      }
    },
  }, [
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Username'),
      usernameInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Password'),
      passwordInput,
    ]),
    h('div', { className: 'mt-4' }, [submitBtn]),
  ]);

  const brandIcon = h('img', { src: '/ho_white.svg', className: 'sidebar__brand-img', style: { margin: '0 auto' } });

  const card = h('div', { className: 'login-card' }, [
    h('div', { className: 'login-card__header' }, [
      brandIcon,
      h('h2', { className: 'login-card__title' }, 'Sign in to HO'),
      h('p', { className: 'text-secondary text-sm mt-2' }, 'Enter your credentials to continue'),
    ]),
    form,
  ]);

  const page = h('div', { className: 'login-container' }, [card]);
  render(container, page);

  usernameInput.focus();
}
