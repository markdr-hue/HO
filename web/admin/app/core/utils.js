/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Shared utility functions — keeps views DRY.
 */

/**
 * Safely parse a JSON string, returning a fallback on failure.
 * @param {string} raw - The JSON string to parse.
 * @param {*} [fallback=null] - Value returned if parsing fails or raw is falsy.
 * @returns {*} Parsed value or fallback.
 */
export function safeJsonParse(raw, fallback = null) {
  if (!raw || typeof raw !== 'string') return fallback;
  try { return JSON.parse(raw); } catch { return fallback; }
}

/**
 * Check whether an SSE event payload belongs to the given site.
 * Replaces the repeated `!data || String(data.site_id) !== String(siteId)` guard.
 * @param {Object} data - Event payload.
 * @param {string|number} siteId - The current site ID.
 * @returns {boolean} True if the event is for this site.
 */
export function isForSite(data, siteId) {
  return !!data && String(data.site_id) === String(siteId);
}
