/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site switcher component — searchable dropdown with favorites, recents, and all sites.
 */

import { h, clear } from '../core/dom.js';
import { icon } from './icon.js';
import { navigate, getActiveSiteId } from '../core/router.js';
import * as state from '../core/state.js';

const FAV_KEY = 'ho_fav_sites';
const RECENT_KEY = 'ho_recent_sites';
const ACTIVE_KEY = 'ho_active_site';
const MAX_RECENTS = 5;

// --- localStorage helpers ---

function getFavorites() {
  try {
    return JSON.parse(localStorage.getItem(FAV_KEY)) || [];
  } catch (err) { console.warn('Failed to parse favorites:', err); return []; }
}

function setFavorites(ids) {
  localStorage.setItem(FAV_KEY, JSON.stringify(ids));
}

function toggleFavorite(siteId) {
  const favs = getFavorites();
  const idx = favs.indexOf(siteId);
  if (idx >= 0) {
    favs.splice(idx, 1);
  } else {
    favs.push(siteId);
    if (favs.length > 10) favs.shift();
  }
  setFavorites(favs);
  return favs;
}

export function getRecents() {
  try {
    return JSON.parse(localStorage.getItem(RECENT_KEY)) || [];
  } catch (err) { console.warn('Failed to parse recents:', err); return []; }
}

export function addRecent(siteId) {
  let recents = getRecents();
  recents = recents.filter(id => id !== siteId);
  recents.unshift(siteId);
  if (recents.length > MAX_RECENTS) recents.length = MAX_RECENTS;
  localStorage.setItem(RECENT_KEY, JSON.stringify(recents));
}

export function getActiveSite() {
  try {
    const val = localStorage.getItem(ACTIVE_KEY);
    return val ? parseInt(val) : null;
  } catch (err) { console.warn('Failed to parse active site:', err); return null; }
}

export function setActiveSite(siteId) {
  if (siteId != null) {
    localStorage.setItem(ACTIVE_KEY, String(siteId));
  } else {
    localStorage.removeItem(ACTIVE_KEY);
  }
}

/**
 * Create the site switcher component.
 * @returns {{ element: HTMLElement, update: () => void }}
 */
export function createSiteSwitcher() {
  let isOpen = false;
  let searchTerm = '';

  const btn = h('button', { className: 'site-switcher__btn' });
  const dropdown = h('div', { className: 'site-switcher__dropdown' });
  const element = h('div', { className: 'site-switcher' }, [btn, dropdown]);


  function renderButton() {
    clear(btn);
    const activeSiteId = getActiveSiteId() || getActiveSite();
    const sites = state.get('sites') || [];
    const runningSites = state.get('runningSites') || [];
    const site = sites.find(s => s.id === activeSiteId);

    if (site) {
      const isRunning = runningSites.includes(site.id);
      btn.appendChild(h('span', {
        className: `status-dot${isRunning ? ' status-dot--active' : ''}`,
      }));
      btn.appendChild(h('span', { className: 'site-switcher__btn-name' }, site.name));
    } else {
      btn.appendChild(h('span', { innerHTML: icon('globe'), className: 'site-switcher__btn-icon' }));
      btn.appendChild(h('span', { className: 'site-switcher__btn-name' }, 'Select Project'));
    }

    btn.appendChild(h('span', {
      innerHTML: icon('chevron-down'),
      className: 'site-switcher__chevron',
    }));

    // Activity indicator: show dot if other sites have recent activity
    const siteActivity = state.get('siteActivity') || {};
    const now = Date.now();
    const currentSiteId = getActiveSiteId();
    const hasOtherActivity = Object.entries(siteActivity).some(([id, ts]) => {
      return parseInt(id) !== currentSiteId && (now - ts) < 30000;
    });

    if (hasOtherActivity) {
      btn.appendChild(h('span', { className: 'site-switcher__activity-dot' }));
    }
  }

  function renderDropdown() {
    clear(dropdown);
    const sites = state.get('sites') || [];
    const runningSites = state.get('runningSites') || [];
    const favIds = getFavorites();
    const recentIds = getRecents();
    const term = searchTerm.toLowerCase();

    // Search input
    const searchInput = h('input', {
      className: 'site-switcher__search',
      placeholder: 'Search projects...',
      value: searchTerm,
      onInput: (e) => {
        searchTerm = e.target.value;
        renderDropdown();
      },
      onKeydown: (e) => {
        if (e.key === 'Escape') {
          close();
        }
      },
    });
    dropdown.appendChild(searchInput);

    // Filter sites by search
    const filtered = term
      ? sites.filter(s =>
          s.name.toLowerCase().includes(term) ||
          (s.domain && s.domain.toLowerCase().includes(term))
        )
      : sites;

    const container = h('div', { className: 'site-switcher__list' });

    if (!term) {
      // Favorites section
      const favSites = favIds.map(id => sites.find(s => s.id === id)).filter(Boolean);
      if (favSites.length > 0) {
        container.appendChild(h('div', { className: 'site-switcher__section-label' }, 'Favorites'));
        for (const site of favSites) {
          container.appendChild(createSiteRow(site, runningSites, favIds, true));
        }
      }

      // Recents section
      const recentSites = recentIds
        .filter(id => !favIds.includes(id))
        .map(id => sites.find(s => s.id === id))
        .filter(Boolean);
      if (recentSites.length > 0) {
        container.appendChild(h('div', { className: 'site-switcher__section-label' }, 'Recent'));
        for (const site of recentSites) {
          container.appendChild(createSiteRow(site, runningSites, favIds, false));
        }
      }

      // All sites section
      if (sites.length > 0) {
        container.appendChild(h('div', { className: 'site-switcher__section-label' }, 'All Projects'));
      }
    }

    for (const site of filtered) {
      container.appendChild(createSiteRow(site, runningSites, favIds, false));
    }

    if (filtered.length === 0) {
      container.appendChild(h('div', { className: 'site-switcher__empty' }, 'No projects found'));
    }

    dropdown.appendChild(container);

    // New site button
    dropdown.appendChild(h('button', {
      className: 'site-switcher__new-btn',
      onClick: (e) => {
        e.stopPropagation();
        close();
        navigate('/sites');
      },
    }, [
      h('span', { innerHTML: icon('plus') }),
      '  New Project',
    ]));

    // Focus search
    requestAnimationFrame(() => searchInput.focus());
  }

  function createSiteRow(site, runningSites, favIds, isFavSection) {
    const isRunning = runningSites.includes(site.id);
    const isFav = favIds.includes(site.id);

    const row = h('div', {
      className: 'site-switcher__row',
      onClick: () => {
        addRecent(site.id);
        setActiveSite(site.id);
        close();
        navigate(`/sites/${site.id}/home`);
      },
    }, [
      h('span', {
        className: `status-dot${isRunning ? ' status-dot--active' : ''}`,
      }),
      h('span', { className: 'site-switcher__row-name' }, site.name),
      site.domain
        ? h('span', { className: 'site-switcher__row-domain' }, site.domain)
        : null,
      h('button', {
        className: `site-switcher__star${isFav ? ' active' : ''}`,
        title: isFav ? 'Remove from favorites' : 'Add to favorites',
        innerHTML: icon(isFav ? 'star-filled' : 'star'),
        onClick: (e) => {
          e.stopPropagation();
          toggleFavorite(site.id);
          renderDropdown();
        },
      }),
    ]);

    return row;
  }

  function open() {
    isOpen = true;
    searchTerm = '';
    dropdown.classList.add('visible');
    element.classList.add('open');
    renderDropdown();
    // Click outside to close
    setTimeout(() => document.addEventListener('click', handleOutsideClick), 0);
  }

  function close() {
    isOpen = false;
    dropdown.classList.remove('visible');
    element.classList.remove('open');
    document.removeEventListener('click', handleOutsideClick);
  }

  function handleOutsideClick(e) {
    if (!element.contains(e.target)) {
      close();
    }
  }

  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    if (isOpen) {
      close();
    } else {
      open();
    }
  });

  function update() {
    renderButton();
  }

  // Initial render
  renderButton();

  return { element, update };
}
