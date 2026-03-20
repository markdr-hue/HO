/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Theme toggle with localStorage persistence.
 */

const THEME_KEY = 'ho_theme';

/**
 * Get the current theme.
 */
export function get() {
  return document.documentElement.getAttribute('data-theme') || 'dark';
}

/**
 * Set the theme explicitly.
 */
export function set(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  localStorage.setItem(THEME_KEY, theme);
}

/**
 * Toggle between dark and light.
 */
export function toggle() {
  const current = get();
  const next = current === 'dark' ? 'light' : 'dark';
  set(next);
  return next;
}

/**
 * Initialize theme from localStorage or default to dark.
 */
export function init() {
  const saved = localStorage.getItem(THEME_KEY);
  set(saved || 'dark');
}
