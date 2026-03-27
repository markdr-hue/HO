/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Shared UI helpers — keeps views DRY.
 */

import { h } from '../core/dom.js';

/** Plain-text empty state (no icons). */
export function emptyState(text) {
  return h('div', { className: 'empty-state' }, [
    h('p', {}, text),
  ]);
}

/** Human-readable byte size. */
export function formatBytes(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

/**
 * Build a public-facing URL and display label for a site.
 * @param {Object} site - Site object with `domain` property.
 * @param {Object} [systemStatus] - System status with `public_port`.
 * @returns {{ url: string, label: string }}
 */
export function formatPublicUrl(site, systemStatus) {
  if (!site.domain) {
    return { url: '#', label: 'No domain configured' };
  }
  const publicPort = (systemStatus && systemStatus.public_port) || 5000;
  const host = site.domain;
  const needsPort = host.includes('localhost') || host.includes('127.0.0.1');
  const url = needsPort ? `http://${host}:${publicPort}` : `http://${host}`;
  const label = needsPort ? `${host}:${publicPort}` : host;
  return { url, label };
}

/** Human-readable number with abbreviated large values. */
export function formatNum(n) {
  if (n == null || n === 0) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 10_000) return (n / 1_000).toFixed(1) + 'K';
  return n.toLocaleString();
}

/** Human-readable duration from seconds. */
export function formatInterval(seconds) {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86400)}d`;
}

/** Toggle switch — wraps a checkbox in the .toggle styling. */
export function toggle(checked, onChange, label) {
  const cb = h('input', { type: 'checkbox', checked });
  if (onChange) cb.addEventListener('change', () => onChange(cb.checked));
  const el = h('label', { className: 'toggle' }, [cb, label]);
  el._input = cb;
  return el;
}

/** Human-readable duration from milliseconds. */
export function formatDuration(ms) {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${seconds % 60}s`;
}
