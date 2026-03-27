/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * User management view.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, put, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderUsers(container) {
  clear(container);

  const headerChildren = [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Users'),
  ];
  if (state.isAdmin()) {
    headerChildren.push(h('button', {
      className: 'btn btn--primary',
      onClick: showAddModal,
    }, [
      h('span', { innerHTML: icon('plus') }),
      'Add User',
    ]));
  }

  const header = h('div', { className: 'context-panel__header context-panel__header--page flex items-center justify-between' }, headerChildren);
  const listContainer = h('div', { className: 'context-panel__body context-panel__body--page' });

  container.appendChild(header);
  container.appendChild(listContainer);

  async function loadUsers() {
    try {
      const users = await get('/admin/api/users');
      renderList(users);
    } catch (err) {
      toast.error('Failed to load users: ' + err.message);
    }
  }

  function renderList(users) {
    clear(listContainer);

    if (users.length === 0) {
      listContainer.appendChild(emptyState('No users found.'));
      return;
    }

    const cards = users.map(u => {
      const roleBadge = u.role === 'admin'
        ? h('span', { className: 'badge badge--success' }, 'Admin')
        : h('span', { className: 'badge' }, u.role);

      return h('div', { className: 'card mb-4' }, [
        h('div', { className: 'card__header' }, [
          h('div', { className: 'flex items-center gap-3' }, [
            h('span', { innerHTML: icon('user') }),
            h('div', {}, [
              h('h4', { className: 'card__title' }, u.display_name || u.username),
              ...(u.display_name ? [h('span', { className: 'text-xs text-secondary' }, u.username)] : []),
            ]),
          ]),
          roleBadge,
        ]),
        ...(state.isAdmin() ? [h('div', { className: 'flex items-center gap-2 mt-4' }, [
          h('button', {
            className: 'btn btn--ghost btn--sm',
            onClick: () => showEditModal(u),
          }, [
            h('span', { innerHTML: icon('edit') }),
            'Edit',
          ]),
          h('button', {
            className: 'btn btn--danger btn--sm',
            onClick: () => confirmDelete(u),
          }, [
            h('span', { innerHTML: icon('trash') }),
            'Delete',
          ]),
        ])] : []),
      ]);
    });

    cards.forEach(c => listContainer.appendChild(c));
  }

  function showAddModal() {
    const usernameInput = h('input', { className: 'input', placeholder: 'Username' });
    const displayNameInput = h('input', { className: 'input', placeholder: 'Display Name' });
    const passwordInput = h('input', { className: 'input', type: 'password', placeholder: 'Password' });
    const roleSelect = h('select', { className: 'input' }, [
      h('option', { value: 'user' }, 'User'),
      h('option', { value: 'admin' }, 'Admin'),
    ]);

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Username', h('span', { className: 'required' }, ' *')]),
        usernameInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Display Name'),
        displayNameInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Password', h('span', { className: 'required' }, ' *')]),
        passwordInput,
        h('p', { className: 'form-hint' }, 'Min 8 characters, with uppercase, number, and special character.'),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Role'),
        roleSelect,
      ]),
    ]);

    modal.show('Add User', content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Create User',
        className: 'btn btn--primary',
        onClick: async () => {
          // Validate required fields
          const username = usernameInput.value.trim();
          const password = passwordInput.value;
          let valid = true;

          usernameInput.classList.remove('input--error');
          passwordInput.classList.remove('input--error');

          if (!username) {
            toast.error('Username is required');
            usernameInput.classList.add('input--error');
            valid = false;
          }
          if (!password) {
            toast.error('Password is required');
            passwordInput.classList.add('input--error');
            valid = false;
          } else if (password.length < 8) {
            toast.error('Password must be at least 8 characters');
            passwordInput.classList.add('input--error');
            valid = false;
          } else if (!/[A-Z]/.test(password)) {
            toast.error('Password must contain an uppercase letter');
            passwordInput.classList.add('input--error');
            valid = false;
          } else if (!/[0-9]/.test(password)) {
            toast.error('Password must contain a number');
            passwordInput.classList.add('input--error');
            valid = false;
          } else if (!/[^A-Za-z0-9]/.test(password)) {
            toast.error('Password must contain a special character');
            passwordInput.classList.add('input--error');
            valid = false;
          }

          if (!valid) return false;

          try {
            await post('/admin/api/users', {
              username,
              display_name: displayNameInput.value.trim(),
              password,
              role: roleSelect.value,
            });
            toast.success('User created');
            loadUsers();
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]);
  }

  function showEditModal(user) {
    const displayNameInput = h('input', { className: 'input', value: user.display_name || '', placeholder: 'Display Name' });
    const passwordInput = h('input', { className: 'input', type: 'password', placeholder: 'New password (leave blank to keep current)' });
    const roleSelect = h('select', { className: 'input' }, [
      h('option', { value: 'admin', selected: user.role === 'admin' }, 'Admin'),
      h('option', { value: 'user', selected: user.role === 'user' }, 'User'),
    ]);

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Display Name'),
        displayNameInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Password'),
        passwordInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Role'),
        roleSelect,
      ]),
    ]);

    modal.show(`Edit User: ${user.username}`, content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Save Changes',
        className: 'btn btn--primary',
        onClick: async () => {
          try {
            const body = { role: roleSelect.value };
            const dn = displayNameInput.value.trim();
            if (dn) body.display_name = dn;
            if (passwordInput.value) {
              body.password = passwordInput.value;
            }
            await put(`/admin/api/users/${user.id}`, body);
            toast.success('User updated');
            loadUsers();
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]);
  }

  function confirmDelete(user) {
    modal.confirmDanger(
      'Delete User',
      `Are you sure you want to delete "${user.username}"?`,
      async () => {
        try {
          await del(`/admin/api/users/${user.id}`);
          toast.success('User deleted');
          loadUsers();
        } catch (err) {
          toast.error(err.message);
        }
      }
    );
  }

  loadUsers();
}
