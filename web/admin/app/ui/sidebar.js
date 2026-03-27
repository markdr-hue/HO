/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Sidebar component with navigation, site switcher, and theme toggle.
 */

import { h, clear } from '../core/dom.js';
import { icon } from './icon.js';
import { navigate, currentPath, getActiveSiteId } from '../core/router.js';
import * as theme from '../core/theme.js';
import * as state from '../core/state.js';
import { createSiteSwitcher } from './site-switcher.js';


/**
 * Create the sidebar component.
 */
export function createSidebar() {
  const nav = h('div', { className: 'sidebar__nav' });
  const footer = h('div', { className: 'sidebar__footer' });

  const brandIcon = h('img', { src: '/ho_white.svg', className: 'sidebar__brand-img' });
  const brand = h('div', { className: 'sidebar__brand' }, [
    brandIcon,
    h('span', { className: 'sidebar__brand-name' }, 'Humans. Out.'),
  ]);

  const element = h('div', { className: 'sidebar' }, [
    brand,
    nav,
    footer,
  ]);

  // Create site switcher (persists across renders)
  const switcher = createSiteSwitcher();

  // Track references for targeted updates
  let mainSectionEl = null;
  let siteNavSectionEl = null;
  let globalSectionEl = null;
  function renderNav() {
    clear(nav);
    const current = currentPath();
    const activeSiteId = getActiveSiteId();

    // Main navigation
    mainSectionEl = h('div', { className: 'sidebar__section' });

    const mainLinks = [
      { path: '/dashboard', icon: 'home', label: 'Dashboard' },
      { path: '/sites', icon: 'globe', label: 'Projects' },
    ];

    for (const link of mainLinks) {
      const isActive = current === link.path || (link.path !== '/sites' && current.startsWith(link.path + '/'));
      // Special: Sites link is active only on exact /sites, not on /sites/:id
      const sitesExact = link.path === '/sites' && current === '/sites';
      const el = h('a', {
        className: `sidebar__link${isActive || sitesExact ? ' active' : ''}`,
        onClick: (e) => {
          e.preventDefault();
          navigate(link.path);
          element.classList.remove('mobile-open');
        },
      }, [
        h('span', { innerHTML: icon(link.icon) }),
        h('span', {}, link.label),
      ]);
      mainSectionEl.appendChild(el);
    }

    nav.appendChild(mainSectionEl);

    // Site switcher
    nav.appendChild(switcher.element);
    switcher.update();

    // Active site navigation (only shown when viewing a site)
    siteNavSectionEl = h('div', { className: 'sidebar__section sidebar__site-nav' });

    if (activeSiteId) {
      const siteLinks = [
        { id: 'home', icon: 'home', label: 'Home' },
        { id: 'pages', icon: 'file-text', label: 'Pages' },
        { id: 'layouts', icon: 'layers', label: 'Layouts' },
        { id: 'assets', icon: 'image', label: 'Assets' },
        { id: 'tables', icon: 'database', label: 'Tables' },
        { id: 'endpoints', icon: 'zap', label: 'Endpoints' },
        { id: 'files', icon: 'upload', label: 'Files' },
        { id: 'webhooks', icon: 'link', label: 'Webhooks' },
        { id: 'tasks', icon: 'clock', label: 'Tasks' },
        { id: 'questions', icon: 'help-circle', label: 'Questions' },

        { id: 'secrets', icon: 'lock', label: 'Secrets' },
        { id: 'providers', icon: 'shield', label: 'Service Providers' },
        { id: 'analytics', icon: 'bar-chart', label: 'Analytics' },
        { id: 'diagnostics', icon: 'activity', label: 'Diagnostics' },
        { id: 'logs', icon: 'file-text', label: 'Logs' },
        { id: 'settings', icon: 'settings', label: 'Settings' },
      ];

      const activeTab = current.split('/')[3] || 'home';

      for (const link of siteLinks) {
        const isActive = activeTab === link.id;
        const children = [
          h('span', { innerHTML: icon(link.icon) }),
          h('span', {}, link.label),
        ];
        // Add pending questions badge (per-site count)
        if (link.id === 'questions') {
          const map = state.get('pendingQuestions') || {};
          const count = map[activeSiteId] || 0;
          if (count > 0) {
            children.push(h('span', { className: 'sidebar__badge sidebar__badge--pulse' }, String(count)));
          }
        }
        const el = h('a', {
          className: `sidebar__link sidebar__link--site${isActive ? ' active' : ''}`,
          onClick: (e) => {
            e.preventDefault();
            navigate(`/sites/${activeSiteId}/${link.id}`);
            element.classList.remove('mobile-open');
          },
        }, children);
        siteNavSectionEl.appendChild(el);
      }
    }

    nav.appendChild(siteNavSectionEl);

    // Global section
    globalSectionEl = h('div', { className: 'sidebar__section' });
    globalSectionEl.appendChild(h('div', { className: 'sidebar__section-label' }, 'Global'));

    const globalLinks = [
      { path: '/providers', icon: 'brain', label: 'Brain Providers' },
      { path: '/chat-providers', icon: 'chat', label: 'Chat Providers' },
      { path: '/templates', icon: 'copy', label: 'Templates' },
      { path: '/service-providers', icon: 'shield', label: 'Service Providers' },
      { path: '/questions', icon: 'help-circle', label: 'Questions' },
      { path: '/users', icon: 'user', label: 'Users' },
      { path: '/usage', icon: 'activity', label: 'Usage' },
      { path: '/settings', icon: 'settings', label: 'Settings' },
    ];

    for (const link of globalLinks) {
      const isActive = current === link.path;
      const el = h('a', {
        className: `sidebar__link${isActive ? ' active' : ''}`,
        onClick: (e) => {
          e.preventDefault();
          navigate(link.path);
          element.classList.remove('mobile-open');
        },
      }, [
        h('span', { innerHTML: icon(link.icon) }),
        h('span', {}, link.label),
      ]);

      globalSectionEl.appendChild(el);
    }

    nav.appendChild(globalSectionEl);

    // Footer with theme toggle + Cmd+K hint
    clear(footer);
    const currentTheme = theme.get();
    const themeBtn = h('button', {
      className: 'btn btn--ghost btn--sm',
      innerHTML: icon(currentTheme === 'dark' ? 'sun' : 'moon'),
      title: `Switch to ${currentTheme === 'dark' ? 'light' : 'dark'} mode`,
      onClick: () => {
        theme.toggle();
        renderNav();
      },
    });
    footer.appendChild(themeBtn);

    const socialLinks = h('span', { className: 'sidebar__social' }, [
      h('a', {
        href: 'https://github.com/markdr-hue/HO',
        target: '_blank',
        rel: 'noopener noreferrer',
        className: 'sidebar__social-link',
        title: 'GitHub',
        innerHTML: icon('github'),
      }),
      h('a', {
        href: 'https://discord.com/invite/VRdYgDQ2qr',
        target: '_blank',
        rel: 'noopener noreferrer',
        className: 'sidebar__social-link',
        title: 'Discord',
        innerHTML: icon('discord'),
      }),
    ]);
    footer.appendChild(socialLinks);

    const sysStatus = state.get('systemStatus');
    if (sysStatus && sysStatus.version) {
      footer.appendChild(h('span', { className: 'sidebar__version' }, `v${sysStatus.version}`));
    }

  }

  // Re-render nav on hash change
  window.addEventListener('hashchange', renderNav);

  // Watch for state changes that affect sidebar
  state.watch('sites', () => {
    // Full renderNav needed if site nav section is visible (to update site links)
    // Otherwise just update the switcher dropdown
    if (getActiveSiteId()) {
      renderNav();
    } else {
      switcher.update();
    }
  });
  state.watch('runningSites', () => switcher.update());
  state.watch('siteActivity', () => switcher.update());
  state.watch('pendingQuestions', renderNav);

  renderNav();

  return { sidebar: { renderNav }, element };
}
