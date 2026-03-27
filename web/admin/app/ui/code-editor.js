/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Lightweight CodeMirror 6 wrapper.
 * Lazy-loads from CDN on first use. Supports CSS, JS, HTML syntax highlighting,
 * line numbers, bracket matching, and dark/light theme.
 */

const CDN = 'https://esm.sh';
let cmModules = null;

async function loadCodeMirror() {
  if (cmModules) return cmModules;

  const [
    { EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection, highlightActiveLine, rectangularSelection },
    { EditorState },
    { defaultKeymap, history, historyKeymap, indentWithTab },
    { syntaxHighlighting, defaultHighlightStyle, bracketMatching, foldGutter, indentOnInput },
    { closeBrackets, closeBracketsKeymap },
    { javascript },
    { css },
    { html },
  ] = await Promise.all([
    import(`${CDN}/@codemirror/view@6`),
    import(`${CDN}/@codemirror/state@6`),
    import(`${CDN}/@codemirror/commands@6`),
    import(`${CDN}/@codemirror/language@6`),
    import(`${CDN}/@codemirror/autocomplete@6`),
    import(`${CDN}/@codemirror/lang-javascript@6`),
    import(`${CDN}/@codemirror/lang-css@6`),
    import(`${CDN}/@codemirror/lang-html@6`),
  ]);

  cmModules = {
    EditorView, EditorState, keymap, lineNumbers, highlightActiveLineGutter,
    highlightSpecialChars, drawSelection, highlightActiveLine, rectangularSelection,
    defaultKeymap, history, historyKeymap, indentWithTab,
    syntaxHighlighting, defaultHighlightStyle, bracketMatching, foldGutter, indentOnInput,
    closeBrackets, closeBracketsKeymap,
    javascript, css, html,
  };
  return cmModules;
}

function detectLanguage(filename, cm) {
  if (!filename) return [];
  const ext = filename.split('.').pop().toLowerCase();
  switch (ext) {
    case 'js': return [cm.javascript()];
    case 'css': return [cm.css()];
    case 'html': case 'htm': return [cm.html()];
    case 'json': return [cm.javascript()]; // JSON highlighting via JS
    default: return [];
  }
}

/**
 * Create a CodeMirror editor instance.
 * @param {HTMLElement} container - DOM element to mount the editor in
 * @param {Object} opts
 * @param {string} opts.value - Initial content
 * @param {string} opts.filename - Filename for language detection
 * @param {Function} opts.onChange - Called with new content on each change
 * @param {number} opts.minHeight - Minimum height in px (default 300)
 * @returns {Promise<{getValue, setValue, destroy, view}>}
 */
export async function createEditor(container, { value = '', filename = '', onChange, minHeight = 300 } = {}) {
  const cm = await loadCodeMirror();

  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';

  const theme = cm.EditorView.theme({
    '&': {
      fontSize: '13px',
      fontFamily: '"SF Mono", "Fira Code", "Consolas", monospace',
      minHeight: minHeight + 'px',
      border: '1px solid var(--color-border)',
      borderRadius: '6px',
      overflow: 'hidden',
    },
    '.cm-content': { padding: '8px 0' },
    '.cm-gutters': {
      background: isDark ? '#1e1e2e' : '#f5f5f5',
      borderRight: '1px solid var(--color-border)',
      color: isDark ? '#6c7086' : '#999',
    },
    '.cm-activeLine': { background: isDark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.03)' },
    '.cm-activeLineGutter': { background: isDark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.05)' },
    '&.cm-focused': { outline: '2px solid var(--color-primary)', outlineOffset: '-1px' },
    '.cm-selectionBackground': { background: isDark ? 'rgba(139,148,158,0.3)' : 'rgba(0,120,215,0.15)' },
  }, { dark: isDark });

  const langExts = detectLanguage(filename, cm);

  const updateListener = cm.EditorView.updateListener.of((update) => {
    if (update.docChanged && onChange) {
      onChange(update.state.doc.toString());
    }
  });

  const extensions = [
    cm.lineNumbers(),
    cm.highlightActiveLineGutter(),
    cm.highlightSpecialChars(),
    cm.history(),
    cm.foldGutter(),
    cm.drawSelection(),
    cm.indentOnInput(),
    cm.syntaxHighlighting(cm.defaultHighlightStyle, { fallback: true }),
    cm.bracketMatching(),
    cm.closeBrackets(),
    cm.highlightActiveLine(),
    cm.rectangularSelection(),
    cm.keymap.of([
      ...cm.closeBracketsKeymap,
      ...cm.defaultKeymap,
      ...cm.historyKeymap,
      cm.indentWithTab,
    ]),
    ...langExts,
    theme,
    updateListener,
  ];

  const state = cm.EditorState.create({ doc: value, extensions });
  const view = new cm.EditorView({ state, parent: container });

  return {
    getValue: () => view.state.doc.toString(),
    setValue: (newValue) => {
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: newValue },
      });
    },
    destroy: () => view.destroy(),
    view,
  };
}
