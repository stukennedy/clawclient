// ClawClient Application JavaScript

// --- Markdown Rendering ---
// Render all .msg-content elements after DOM updates
function renderMarkdown() {
  document.querySelectorAll('.msg-markdown:not([data-rendered])').forEach(el => {
    if (typeof marked !== 'undefined') {
      const raw = el.textContent;
      el.innerHTML = marked.parse(raw, { breaks: true, gfm: true });
      el.setAttribute('data-rendered', '1');
    }
  });
}

// Run on page load and after Datastar patches
const observer = new MutationObserver(() => {
  requestAnimationFrame(renderMarkdown);
});
observer.observe(document.body, { childList: true, subtree: true });
document.addEventListener('DOMContentLoaded', renderMarkdown);

// --- Textarea Auto-resize ---
function autoResize(textarea) {
  textarea.style.height = 'auto';
  textarea.style.height = Math.min(textarea.scrollHeight, 150) + 'px';
}

// Initialize textarea behavior on any .chat-input
document.addEventListener('DOMContentLoaded', () => {
  initTextarea();
});

// Re-init after DOM changes (Datastar patches)
const textareaObserver = new MutationObserver(() => {
  initTextarea();
});
textareaObserver.observe(document.body, { childList: true, subtree: true });

function initTextarea() {
  document.querySelectorAll('.chat-input:not([data-init])').forEach(ta => {
    ta.setAttribute('data-init', '1');
    ta.addEventListener('input', () => autoResize(ta));
    // Reset height after send (content cleared by Datastar)
    const obs = new MutationObserver(() => {
      if (ta.value === '') {
        ta.style.height = 'auto';
      }
    });
    obs.observe(ta, { attributes: true, attributeFilter: ['value'] });
  });
}

// --- Context Menu ---
function toggleMenu() {
  var m = document.getElementById('ctx-menu');
  if (m) m.classList.toggle('hidden');
}

function closeMenu() {
  var m = document.getElementById('ctx-menu');
  if (m) m.classList.add('hidden');
}
