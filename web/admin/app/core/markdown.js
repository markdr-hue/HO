/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Minimal markdown renderer. Converts a subset of markdown to HTML.
 * Supports: headers, bold, italic, code blocks, inline code, links, lists, paragraphs.
 */

/**
 * Render markdown text to an HTML string.
 */
export function render(text) {
  if (!text) return '';

  // Normalize line endings
  let md = text.replace(/\r\n/g, '\n');

  // Escape HTML
  md = escapeHtml(md);

  // Code blocks (``` ... ```)
  md = md.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
    return `<pre><code class="lang-${lang}">${code.trim()}</code></pre>`;
  });

  // Inline code
  md = md.replace(/`([^`]+)`/g, '<code>$1</code>');

  // Headers
  md = md.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
  md = md.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  md = md.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  md = md.replace(/^# (.+)$/gm, '<h1>$1</h1>');

  // Bold + italic
  md = md.replace(/\*\*\*(.+?)\*\*\*/g, '<strong><em>$1</em></strong>');
  // Bold
  md = md.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  // Italic
  md = md.replace(/\*(.+?)\*/g, '<em>$1</em>');

  // Links (only allow http/https URLs to prevent javascript: injection)
  md = md.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, text, url) => {
    if (/^https?:\/\//i.test(url) || url.startsWith('/') || url.startsWith('#')) {
      return `<a href="${url}" target="_blank" rel="noopener">${text}</a>`;
    }
    return text;
  });

  // Unordered lists
  md = md.replace(/^(\s*)[-*] (.+)$/gm, (_, indent, content) => {
    return `${indent}<li>${content}</li>`;
  });
  // Wrap consecutive <li> in <ul>
  md = md.replace(/((?:<li>.*<\/li>\n?)+)/g, '<ul>$1</ul>');

  // Ordered lists
  md = md.replace(/^(\s*)\d+\. (.+)$/gm, (_, indent, content) => {
    return `${indent}<oli>${content}</oli>`;
  });
  md = md.replace(/((?:<oli>.*<\/oli>\n?)+)/g, (match) => {
    return '<ol>' + match.replace(/<\/?oli>/g, (t) => t.replace('oli', 'li')) + '</ol>';
  });

  // Horizontal rules
  md = md.replace(/^---$/gm, '<hr>');

  // Paragraphs: wrap remaining lines that are not already wrapped in block tags
  const lines = md.split('\n');
  const result = [];
  let inParagraph = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();

    if (!trimmed) {
      if (inParagraph) {
        result.push('</p>');
        inParagraph = false;
      }
      continue;
    }

    const isBlock = /^<(h[1-6]|ul|ol|li|pre|hr|blockquote)/.test(trimmed);
    if (isBlock) {
      if (inParagraph) {
        result.push('</p>');
        inParagraph = false;
      }
      result.push(line);
    } else {
      if (!inParagraph) {
        result.push('<p>');
        inParagraph = true;
      }
      result.push(line);
    }
  }

  if (inParagraph) {
    result.push('</p>');
  }

  return result.join('\n');
}

function escapeHtml(text) {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}
