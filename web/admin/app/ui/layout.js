/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * App shell layout: sidebar + main content area.
 */

import { h } from '../core/dom.js';
import { createSidebar } from './sidebar.js';
import { icon } from './icon.js';

/**
 * Create the app layout and return references to sidebar and main content.
 * @returns {{ root: HTMLElement, main: HTMLElement, sidebar: HTMLElement }}
 */
export function createLayout() {
  const { sidebar, element: sidebarEl } = createSidebar();

  const main = h('div', { className: 'main-content' });

  // Mobile toggle button
  const mobileToggle = h('button', {
    className: 'mobile-toggle',
    innerHTML: icon('menu'),
    onClick: () => {
      sidebarEl.classList.toggle('mobile-open');
    },
  });

  const root = h('div', { className: 'app-layout' }, [
    mobileToggle,
    sidebarEl,
    main,
  ]);

  // Close sidebar on mobile when clicking outside
  main.addEventListener('click', () => {
    sidebarEl.classList.remove('mobile-open');
  });

  return { root, main, sidebar };
}
