// ClawClient Application JavaScript

// --- Markdown Rendering ---
function renderMarkdown() {
  document.querySelectorAll('.msg-markdown:not([data-rendered])').forEach(el => {
    if (typeof marked !== 'undefined') {
      const raw = el.textContent;
      el.innerHTML = marked.parse(raw, { breaks: true, gfm: true });
      el.setAttribute('data-rendered', '1');
    }
  });
}

// --- Auto-scroll to bottom of message list ---
var _lastMessageCount = 0;

function scrollToBottom() {
  var list = document.getElementById('message-list');
  if (!list) return;

  var count = list.children.length;
  if (count !== _lastMessageCount) {
    _lastMessageCount = count;
    // Find the scroll container (parent with overflow-y-auto)
    var container = list.closest('.overflow-y-auto') || list.parentElement;
    if (container) {
      requestAnimationFrame(function() {
        container.scrollTop = container.scrollHeight;
      });
    }
  }
}

// --- Combined DOM observer ---
// Fires on any DOM mutation — handles markdown, scroll, textarea init
const observer = new MutationObserver(() => {
  requestAnimationFrame(() => {
    renderMarkdown();
    scrollToBottom();
    initTextarea();
  });
});
observer.observe(document.body, { childList: true, subtree: true });

document.addEventListener('DOMContentLoaded', () => {
  renderMarkdown();
  scrollToBottom();
  initTextarea();
});

// --- Textarea Auto-resize ---
function autoResize(textarea) {
  textarea.style.height = 'auto';
  textarea.style.height = Math.min(textarea.scrollHeight, 150) + 'px';
}

function initTextarea() {
  document.querySelectorAll('.chat-input:not([data-init])').forEach(ta => {
    ta.setAttribute('data-init', '1');
    ta.addEventListener('input', () => autoResize(ta));
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
