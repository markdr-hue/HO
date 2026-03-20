/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Modal dialog component.
 */

import { h, clear } from '../core/dom.js';
import { icon } from './icon.js';

let currentOverlay = null;
let currentKeyHandler = null;

/**
 * Show a modal dialog.
 * @param {string} title
 * @param {HTMLElement|string} content - Body content
 * @param {Array<{label: string, className?: string, onClick: Function}>} [actions]
 * @returns {Function} close function
 */
export function show(title, content, actions = []) {
  close(); // Close any existing modal

  const closeBtn = h('button', {
    className: 'btn btn--icon',
    onClick: close,
    innerHTML: icon('x'),
  });

  const header = h('div', { className: 'modal__header' }, [
    h('h3', { className: 'modal__title' }, title),
    closeBtn,
  ]);

  const body = h('div', { className: 'modal__body' });
  if (typeof content === 'string') {
    body.textContent = content;
  } else if (content instanceof HTMLElement) {
    body.appendChild(content);
  }

  const footer = h('div', { className: 'modal__footer' },
    actions.map(a =>
      h('button', {
        className: a.className || 'btn',
        onClick: async () => {
          const result = await Promise.resolve(a.onClick());
          if (result !== false) close();
        },
      }, a.label)
    )
  );

  const modal = h('div', { className: 'modal' }, [header, body, footer]);

  const overlay = h('div', { className: 'modal-overlay' }, [modal]);
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });

  document.body.appendChild(overlay);
  currentOverlay = overlay;

  // Animate in
  requestAnimationFrame(() => {
    overlay.classList.add('visible');
  });

  // ESC to close
  currentKeyHandler = (e) => {
    if (e.key === 'Escape') close();
  };
  document.addEventListener('keydown', currentKeyHandler);

  return close;
}

/**
 * Close the current modal.
 */
export function close() {
  if (currentKeyHandler) {
    document.removeEventListener('keydown', currentKeyHandler);
    currentKeyHandler = null;
  }
  if (currentOverlay) {
    currentOverlay.classList.remove('visible');
    const overlay = currentOverlay;
    setTimeout(() => {
      if (overlay.parentNode) {
        overlay.parentNode.removeChild(overlay);
      }
    }, 200);
    currentOverlay = null;
  }
}

/**
 * Show a confirmation dialog.
 */
export function confirm(title, message, onConfirm) {
  const body = h('p', { className: 'text-secondary' }, message);
  return show(title, body, [
    { label: 'Cancel', className: 'btn', onClick: () => {} },
    { label: 'Confirm', className: 'btn btn--primary', onClick: onConfirm },
  ]);
}

/**
 * Show a danger confirmation dialog.
 */
export function confirmDanger(title, message, onConfirm) {
  const body = h('p', { className: 'text-secondary' }, message);
  return show(title, body, [
    { label: 'Cancel', className: 'btn', onClick: () => {} },
    { label: 'Delete', className: 'btn btn--danger', onClick: onConfirm },
  ]);
}
