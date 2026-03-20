/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Token usage display.
 */

import { h, clear } from '../../core/dom.js';
import { get } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderUsage(container) {
  clear(container);

  const header = h('div', { className: 'context-panel__header context-panel__header--page' }, [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Usage'),
  ]);

  const body = h('div', { className: 'context-panel__body context-panel__body--page' });

  const statsContainer = h('div', { className: 'stat-cards' });
  const logsContainer = h('div');

  body.appendChild(statsContainer);
  body.appendChild(logsContainer);

  container.appendChild(header);
  container.appendChild(body);

  try {
    const [status, sites] = await Promise.all([
      get('/admin/api/system/status'),
      get('/admin/api/sites'),
    ]);

    clear(statsContainer);
    statsContainer.appendChild(
      h('div', { className: 'stat-card' }, [
        h('div', { className: 'stat-card__label' }, 'Active Brains'),
        h('div', { className: 'stat-card__value' }, String(status.brain_running)),
        h('div', { className: 'stat-card__sub' }, `of ${status.site_count} sites`),
      ])
    );
    statsContainer.appendChild(
      h('div', { className: 'stat-card' }, [
        h('div', { className: 'stat-card__label' }, 'Providers'),
        h('div', { className: 'stat-card__value' }, String(status.provider_count)),
        h('div', { className: 'stat-card__sub' }, 'configured'),
      ])
    );
    statsContainer.appendChild(
      h('div', { className: 'stat-card' }, [
        h('div', { className: 'stat-card__label' }, 'Goroutines'),
        h('div', { className: 'stat-card__value' }, String(status.num_goroutines)),
        h('div', { className: 'stat-card__sub' }, `${status.go_version}`),
      ])
    );
    statsContainer.appendChild(
      h('div', { className: 'stat-card' }, [
        h('div', { className: 'stat-card__label' }, 'Uptime'),
        h('div', { className: 'stat-card__value' }, status.uptime),
        h('div', { className: 'stat-card__sub' }, `${status.goos}/${status.goarch}`),
      ])
    );

    clear(logsContainer);
    if (sites.length > 0) {
      logsContainer.appendChild(h('h3', { className: 'mb-4 mt-6' }, 'Usage by Project'));

      for (const site of sites) {
        try {
          const stats = await get(`/admin/api/logs/${site.id}/llm/stats`);
          const totalTokens = (stats.total_input_tokens || 0) + (stats.total_output_tokens || 0);

          const details = [];
          details.push(`${stats.total_calls || 0} LLM calls`);
          if (stats.total_errors > 0) details.push(`${stats.total_errors} errors`);

          // Model breakdown
          const modelInfo = (stats.by_model || [])
            .slice(0, 3)
            .map(m => `${m.model} (${m.calls})`)
            .join(', ');

          const siteCard = h('div', { className: 'card mb-4' }, [
            h('div', { className: 'card__header' }, [
              h('h4', { className: 'card__title' }, site.name),
              h('span', { className: 'badge badge--accent' }, `${totalTokens.toLocaleString()} tokens`),
            ]),
            h('p', { className: 'text-sm text-secondary' }, details.join(' \u00b7 ')),
            modelInfo ? h('p', { className: 'text-xs text-tertiary mt-1' }, modelInfo) : null,
          ].filter(Boolean));
          logsContainer.appendChild(siteCard);
        } catch {
          // Skip if stats fail for a site
        }
      }
    } else {
      logsContainer.appendChild(emptyState('No usage data yet. Usage data will appear once projects start using AI.'));
    }
  } catch (err) {
    toast.error('Failed to load usage data: ' + err.message);
  }
}
