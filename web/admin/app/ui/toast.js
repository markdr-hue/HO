/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Toast notification system.
 */

let container = null;

function ensureContainer() {
  if (!container) {
    container = document.createElement('div');
    container.className = 'toast-container';
    document.body.appendChild(container);
  }
  return container;
}

/**
 * Show a toast notification.
 * @param {string} message - The message to display.
 * @param {'success'|'error'|'warning'|'info'} [type='info'] - Toast type.
 * @param {number} [duration=4000] - Duration in ms before auto-dismiss.
 * @param {Function} [onClick] - Optional click callback (navigates instead of just dismissing).
 */
export function show(message, type = 'info', duration = 4000, onClick = null) {
  const c = ensureContainer();

  const toast = document.createElement('div');
  toast.className = `toast toast--${type}`;
  if (onClick) toast.classList.add('toast--clickable');
  toast.textContent = message;

  c.appendChild(toast);

  // Trigger animation
  requestAnimationFrame(() => {
    toast.classList.add('visible');
  });

  // Auto dismiss
  const timer = setTimeout(() => dismiss(toast), duration);

  // Click to dismiss (or navigate)
  toast.addEventListener('click', () => {
    clearTimeout(timer);
    if (onClick) onClick();
    dismiss(toast);
  });
}

function dismiss(toast) {
  toast.classList.remove('visible');
  toast.addEventListener('transitionend', () => {
    if (toast.parentNode) {
      toast.parentNode.removeChild(toast);
    }
  });
  // Fallback: remove if transitionend never fires
  setTimeout(() => {
    if (toast.parentNode) toast.parentNode.removeChild(toast);
  }, 500);
}

export function success(msg) { show(msg, 'success'); }
export function error(msg) { show(msg, 'error', 6000); }
export function warning(msg) { show(msg, 'warning'); }
export function info(msg) { show(msg, 'info'); }
