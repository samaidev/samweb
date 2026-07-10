// SamWeb UI controller.
//
// Responsibilities:
//   - tab management (open / close / switch / reload)
//   - omnibox input handling (URL detection vs. search, navigation)
//   - per-tab history (back / forward)
//   - bookmarks (persisted to localStorage)
//   - global history (persisted to localStorage)
//   - search engine switching
//
// Pages are rendered through the Go-side proxy (config.proxyBase) so that
// X-Frame-Options / CSP frame-ancestors do not block embedding in the
// iframe. The new-tab page is rendered via the iframe's srcdoc so it does
// not require any network access.
'use strict';

// ----------------------------- State -----------------------------
const state = {
  tabs: [],          // [{ id, title, url, history: [], histIndex: -1, favicon }]
  activeTabId: null,
  nextTabId: 1,
  config: { proxyBase: '', defaultEngine: 'Google', engines: [] },
  bookmarks: loadJSON('samweb.bookmarks', []),
  history: loadJSON('samweb.history', []),
  engine: localStorage.getItem('samweb.engine') || 'Google',
};

// ----------------------------- DOM refs -----------------------------
const el = {
  tabStrip: document.getElementById('tab-strip'),
  tabs: document.getElementById('tabs'),
  newTabBtn: document.getElementById('new-tab-btn'),
  back: document.getElementById('back-btn'),
  forward: document.getElementById('forward-btn'),
  reload: document.getElementById('reload-btn'),
  home: document.getElementById('home-btn'),
  omniboxWrap: document.getElementById('omnibox-wrap'),
  omniboxIcon: document.getElementById('omnibox-icon'),
  omnibox: document.getElementById('omnibox'),
  bookmark: document.getElementById('bookmark-btn'),
  openDirect: document.getElementById('open-direct-btn'),
  omniboxSuggestions: document.getElementById('omnibox-suggestions'),
  history: document.getElementById('history-btn'),
  bookmarks: document.getElementById('bookmarks-btn'),
  engine: document.getElementById('engine-btn'),
  view: document.getElementById('view'),
  popoverMask: document.getElementById('popover-mask'),
  enginePopover: document.getElementById('engine-popover'),
  engineList: document.getElementById('engine-list'),
  historyPopover: document.getElementById('history-popover'),
  historyList: document.getElementById('history-list'),
  clearHistory: document.getElementById('clear-history'),
  bookmarksPopover: document.getElementById('bookmarks-popover'),
  bookmarksList: document.getElementById('bookmarks-list'),
  // Profile management UI
  profileWrap: document.getElementById('profile-wrap'),
  profileSelect: document.getElementById('profile-select'),
  profileSaveBtn: document.getElementById('profile-save-btn'),
  profileManageBtn: document.getElementById('profile-manage-btn'),
  profilePopover: document.getElementById('profile-popover'),
  profileList: document.getElementById('profile-list'),
  profileNewName: document.getElementById('profile-new-name'),
  profileCreateBtn: document.getElementById('profile-create-btn'),
};

// Profile state: list of {id, name, cookie_count, created, updated} + activeProfileId
state.profiles = [];
state.activeProfileId = '';

// ----------------------------- Bootstrap -----------------------------
async function init() {
  const cfg = await fetchJSON('/api/config');
  state.config = cfg;
  if (!localStorage.getItem('samweb.engine')) {
    state.engine = cfg.defaultEngine || 'Google';
  }
  bindEvents();
  await refreshProfiles(); // load saved profiles into the dropdown
  newTab(); // start with a single fresh tab
}

document.addEventListener('DOMContentLoaded', init);

// ----------------------------- Events -----------------------------
function bindEvents() {
  el.newTabBtn.addEventListener('click', () => newTab());
  el.back.addEventListener('click', () => goBack());
  el.forward.addEventListener('click', () => goForward());
  el.reload.addEventListener('click', () => reloadActive());
  el.home.addEventListener('click', () => navigate('about:newtab'));
  el.bookmark.addEventListener('click', () => toggleBookmark());
  el.openDirect.addEventListener('click', () => openDirect());
  el.history.addEventListener('click', () => togglePopover('history'));
  el.bookmarks.addEventListener('click', () => togglePopover('bookmarks'));
  el.engine.addEventListener('click', () => togglePopover('engine'));
  el.clearHistory.addEventListener('click', () => clearHistory());

  // Profile events
  el.profileSelect.addEventListener('change', () => switchProfile(el.profileSelect.value));
  el.profileSaveBtn.addEventListener('click', () => quickSaveProfile());
  el.profileManageBtn.addEventListener('click', () => togglePopover('profile'));
  el.profileCreateBtn.addEventListener('click', () => createProfileFromInput());

  el.omnibox.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      const sel = getSelectedSuggestion();
      if (sel) {
        navigateToSuggestion(sel, /*direct=*/e.shiftKey);
      } else {
        submitOmnibox();
      }
    } else if (e.key === 'Escape') {
      hideSuggestions();
      const t = activeTab();
      if (t) el.omnibox.value = displayURL(t.url);
      el.omnibox.blur();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      moveSuggestionSelection(1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      moveSuggestionSelection(-1);
    } else if (e.key === 'Tab') {
      // Tab on a suggestion completes the omnibox with that URL
      const sel = getSelectedSuggestion();
      if (sel && sel.url) {
        e.preventDefault();
        el.omnibox.value = sel.url;
        renderSuggestions(el.omnibox.value);
      }
    }
  });
  el.omnibox.addEventListener('input', () => renderSuggestions(el.omnibox.value));
  el.omnibox.addEventListener('focus', () => {
    el.omnibox.select();
    // Show suggestions on focus if there's history
    if (!el.omnibox.value) {
      renderSuggestions('');
    }
  });
  el.omnibox.addEventListener('blur', () => {
    // Delay hiding so click on suggestion registers first
    setTimeout(() => hideSuggestions(), 180);
  });
  el.omnibox.addEventListener('mousedown', (e) => {
    // Single-click anywhere in the omnibox selects all, Chrome-style.
    if (document.activeElement !== el.omnibox) {
      e.preventDefault();
      el.omnibox.focus();
      el.omnibox.select();
    }
  });

  el.popoverMask.addEventListener('click', () => closePopovers());

  // iframe load: update title + favicon.
  el.view.addEventListener('load', onFrameLoad);

  // Keyboard shortcuts (Chrome-compatible).
  document.addEventListener('keydown', (e) => {
    if (!(e.ctrlKey || e.metaKey)) return;
    if (e.key === 't') { e.preventDefault(); newTab(); }
    else if (e.key === 'w') { e.preventDefault(); closeTab(state.activeTabId); }
    else if (e.key === 'r') { e.preventDefault(); reloadActive(); }
    else if (e.key === 'l') { e.preventDefault(); el.omnibox.focus(); el.omnibox.select(); }
    else if (e.key === 'h' && e.shiftKey) { e.preventDefault(); togglePopover('history'); }
  });
  document.addEventListener('keydown', (e) => {
    if (e.altKey && e.key === 'ArrowLeft') { e.preventDefault(); goBack(); }
    else if (e.altKey && e.key === 'ArrowRight') { e.preventDefault(); goForward(); }
  });
}

// ----------------------------- Tabs -----------------------------
function newTab(url) {
  const id = state.nextTabId++;
  const tab = {
    id,
    title: 'New Tab',
    url: url || 'about:newtab',
    history: [url || 'about:newtab'],
    histIndex: 0,
  };
  state.tabs.push(tab);
  state.activeTabId = id;
  renderTabs();
  renderTab(tab);
}

function closeTab(id) {
  const idx = state.tabs.findIndex(t => t.id === id);
  if (idx === -1) return;
  state.tabs.splice(idx, 1);
  if (state.tabs.length === 0) {
    // Chrome closes the window when the last tab is closed. We exit by
    // navigating to about:blank which the host webview treats as no-op.
    window.close();
    return;
  }
  if (state.activeTabId === id) {
    const next = state.tabs[Math.min(idx, state.tabs.length - 1)];
    state.activeTabId = next.id;
    renderTabs();
    renderTab(next);
  } else {
    renderTabs();
  }
}

function activateTab(id) {
  const tab = state.tabs.find(t => t.id === id);
  if (!tab) return;
  state.activeTabId = id;
  renderTabs();
  renderTab(tab);
}

function renderTabs() {
  el.tabs.innerHTML = '';
  for (const t of state.tabs) {
    const node = document.createElement('div');
    node.className = 'tab' + (t.id === state.activeTabId ? ' active' : '');
    node.dataset.id = String(t.id);
    node.innerHTML = `
      <span class="tab-favicon">${faviconSVG(t)}</span>
      <span class="tab-title">${escapeHTML(t.title)}</span>
      <button class="tab-close" title="Close tab">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
      </button>`;
    node.addEventListener('click', (e) => {
      if (e.target.closest('.tab-close')) {
        e.stopPropagation();
        closeTab(t.id);
      } else {
        activateTab(t.id);
      }
    });
    el.tabs.appendChild(node);
  }
}

function faviconSVG(tab) {
  if (tab.url === 'about:newtab') {
    return `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>`;
  }
  return `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path></svg>`;
}

// ----------------------------- Navigation -----------------------------
function activeTab() {
  return state.tabs.find(t => t.id === state.activeTabId);
}

function submitOmnibox() {
  const input = el.omnibox.value.trim();
  if (!input) return;
  hideSuggestions();
  resolveOmnibox(input).then(url => navigate(url));
}

async function resolveOmnibox(input) {
  // Prefer the Go-side resolver (it knows the selected engine). Fall back
  // to a pure-JS resolver if the binding is unavailable.
  if (typeof window.samwebResolve === 'function') {
    try {
      const u = await window.samwebResolve(input);
      if (u) return u;
    } catch (_) { /* fall through */ }
  }
  const r = await fetchJSON('/api/resolve?q=' + encodeURIComponent(input) + '&engine=' + encodeURIComponent(state.engine));
  return r.url;
}

function navigate(url, opts) {
  const t = activeTab();
  if (!t) return;
  url = url || 'about:newtab';
  opts = opts || {};

  if (!opts.skipHistory) {
    // Trim forward history when navigating from the middle.
    t.history = t.history.slice(0, t.histIndex + 1);
    t.history.push(url);
    t.histIndex = t.history.length - 1;
  }
  t.url = url;
  renderTab(t);
  recordHistory(url);
}

function goBack() {
  const t = activeTab();
  if (!t || t.histIndex <= 0) return;
  t.histIndex--;
  t.url = t.history[t.histIndex];
  renderTab(t);
}

function goForward() {
  const t = activeTab();
  if (!t || t.histIndex >= t.history.length - 1) return;
  t.histIndex++;
  t.url = t.history[t.histIndex];
  renderTab(t);
}

function reloadActive() {
  const t = activeTab();
  if (!t) return;
  // Force a reload by toggling the iframe src through about:blank.
  const cur = t.url;
  el.view.src = 'about:blank';
  setTimeout(() => renderFrameFor(t), 0);
  void cur;
}

// renderTab pushes the tab's state into the omnibox, nav buttons, and iframe.
function renderTab(t) {
  // Don't clobber the omnibox if the user is currently typing in it.
  // (iframe load events can trigger renderTab mid-typing.)
  if (document.activeElement !== el.omnibox) {
    el.omnibox.value = displayURL(t.url);
  }
  updateOmniboxIcon(t.url);
  el.back.disabled = t.histIndex <= 0;
  el.forward.disabled = t.histIndex >= t.history.length - 1;
  updateBookmarkButton(t.url);
  renderTabs();
  renderFrameFor(t);
}

function renderFrameFor(t) {
  if (t.url === 'about:newtab' || t.url === 'about:blank') {
    el.view.srcdoc = newTabPageHTML();
    el.view.removeAttribute('src');
    // reset srcdoc to force reload if already on srcdoc
    if (el.view.srcdoc === newTabPageHTML()) {
      el.view.contentWindow.location.reload();
    }
  } else {
    el.view.removeAttribute('srcdoc');
    el.view.src = state.config.proxyBase + encodeURIComponent(t.url);
  }
}

function onFrameLoad() {
  const t = activeTab();
  if (!t) return;
  // We cannot read the iframe's inner URL due to cross-origin, but we can
  // at least keep the omnibox showing what the user typed.
  try {
    const doc = el.view.contentDocument;
    if (doc && doc.title) {
      t.title = doc.title;
      renderTabs();
    }
  } catch (_) { /* cross-origin: ignore */ }
}

// ----------------------------- Omnibox helpers -----------------------------
function displayURL(url) {
  if (url === 'about:newtab') return '';
  return url;
}

function updateOmniboxIcon(url) {
  if (url === 'about:newtab' || url === 'about:blank') {
    el.omniboxIcon.innerHTML = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>`;
    el.omniboxIcon.classList.remove('secure');
    return;
  }
  if (url.startsWith('https://')) {
    el.omniboxIcon.innerHTML = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect><path d="M7 11V7a5 5 0 0 1 10 0v4"></path></svg>`;
    el.omniboxIcon.classList.add('secure');
  } else {
    el.omniboxIcon.innerHTML = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg>`;
    el.omniboxIcon.classList.remove('secure');
  }
}

// ----------------------------- History -----------------------------
function recordHistory(url) {
  if (url === 'about:newtab' || url === 'about:blank') return;
  // Avoid duplicate consecutive entries.
  if (state.history.length && state.history[0].url === url) {
    state.history[0].ts = Date.now();
  } else {
    state.history.unshift({ url, ts: Date.now() });
    if (state.history.length > 500) state.history.length = 500;
  }
  saveJSON('samweb.history', state.history);
}

function clearHistory() {
  state.history = [];
  saveJSON('samweb.history', state.history);
  renderHistoryPopover();
}

// ----------------------------- Open Direct (bypass iframe) -----------------------------
// openDirect makes the WebView2 top-level window navigate to the
// omnibox URL, bypassing the samweb iframe entirely. Use this for
// sites that block iframe embedding (z.ai, Google) — e.g. to log in
// to z.ai and save cookies to a profile.
//
// The backend injects a "← 返回 SamWeb" floating button on the
// cross-origin page so the user can come back.
async function openDirect() {
  const input = el.omnibox.value.trim();
  if (!input) {
    el.omnibox.focus();
    return;
  }
  hideSuggestions();
  let url;
  try {
    url = await resolveOmnibox(input);
  } catch (e) {
    url = input;
  }
  if (!url) url = input;

  if (!confirm('将离开 SamWeb UI，直接在 WebView2 顶层打开：\n\n  ' + url + '\n\n用于登录 z.ai 等不支持 iframe 嵌入的网站。\n登录完成后点右上角"← 返回 SamWeb"按钮回来，然后用"保存 profile"按钮保存 cookies。\n\n继续？')) {
    return;
  }

  try {
    el.openDirect.style.opacity = '0.5';
    el.openDirect.disabled = true;
    const r = await fetch('/agent/cdp-navigate-top', {
      method: 'POST',
      headers: {'Content-Type': 'application/json',
                'Authorization': 'Bearer test-token-2026'},
      body: JSON.stringify({url: url})
    });
    if (!r.ok) {
      const text = await r.text();
      alert('直接打开失败：' + r.status + ' ' + text);
    }
  } catch (e) {
    alert('直接打开出错：' + e.message);
  } finally {
    el.openDirect.style.opacity = '';
    el.openDirect.disabled = false;
  }
}

// ----------------------------- Omnibox Suggestions -----------------------------
// Suggestions dropdown: shows matching history + bookmark URLs as the
// user types in the omnibox. Arrow keys navigate, Enter navigates to
// the selected suggestion, Shift+Enter opens it via "直接打开"
// (bypassing the iframe, useful for z.ai login).

let _suggestionState = { items: [], selectedIdx: -1 };

function buildSuggestions(query) {
  const q = query.trim().toLowerCase();
  const seen = new Set();
  const out = [];

  // Helper to add a suggestion if not already present
  function add(url, title, source) {
    if (!url || url === 'about:newtab' || url === 'about:blank') return;
    if (seen.has(url)) return;
    if (q && !url.toLowerCase().includes(q) && !(title || '').toLowerCase().includes(q)) return;
    seen.add(url);
    out.push({ url, title: title || '', source });
  }

  // 1) Bookmarks (highest priority)
  for (const b of state.bookmarks) {
    add(b.url, b.title, 'bookmark');
  }
  // 2) History (most recent first)
  for (const h of state.history) {
    add(h.url, '', 'history');
  }

  // Sort: exact prefix match first, then bookmark-before-history,
  // then by original order (history is already most-recent-first).
  if (q) {
    out.sort((a, b) => {
      const aPrefix = a.url.toLowerCase().startsWith(q) ? 0 : 1;
      const bPrefix = b.url.toLowerCase().startsWith(q) ? 0 : 1;
      if (aPrefix !== bPrefix) return aPrefix - bPrefix;
      const aBook = a.source === 'bookmark' ? 0 : 1;
      const bBook = b.source === 'bookmark' ? 0 : 1;
      if (aBook !== bBook) return aBook - bBook;
      return 0;
    });
  } else {
    // No query: history first (most recent), then bookmarks
    out.sort((a, b) => {
      const aBook = a.source === 'bookmark' ? 1 : 0;
      const bBook = b.source === 'bookmark' ? 1 : 0;
      return aBook - bBook;
    });
  }

  return out.slice(0, 10);
}

function renderSuggestions(query) {
  const items = buildSuggestions(query);
  _suggestionState.items = items;
  _suggestionState.selectedIdx = -1;

  if (items.length === 0) {
    hideSuggestions();
    return;
  }

  const html = items.map((item, i) => {
    const iconSvg = item.source === 'bookmark'
      ? '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"></path></svg>'
      : '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 16 14"></polyline></svg>';
    const titleOrUrl = item.title || item.url;
    const urlPart = item.title ? `<span class="sug-url">${escapeHtml(item.url)}</span>` : '';
    return `<div class="omnibox-suggestion" data-idx="${i}" data-url="${escapeAttr(item.url)}">
      <span class="sug-icon">${iconSvg}</span>
      <span class="sug-text">${escapeHtml(titleOrUrl)}</span>
      ${urlPart}
      <span class="sug-direct" title="Shift+Enter 用直接打开">直接打开</span>
    </div>`;
  }).join('');
  el.omniboxSuggestions.innerHTML = html;
  el.omniboxSuggestions.classList.remove('hidden');

  // Attach click handlers
  el.omniboxSuggestions.querySelectorAll('.omnibox-suggestion').forEach(node => {
    node.addEventListener('mousedown', (e) => {
      e.preventDefault(); // keep omnibox focused
      const idx = parseInt(node.dataset.idx, 10);
      if (!isNaN(idx) && _suggestionState.items[idx]) {
        // Left click = normal navigate; middle click (button===1) = direct open
        navigateToSuggestion(_suggestionState.items[idx], /*direct=*/e.button === 1 || e.shiftKey);
      }
    });
    node.addEventListener('contextmenu', (e) => e.preventDefault());
  });
}

function hideSuggestions() {
  el.omniboxSuggestions.classList.add('hidden');
  el.omniboxSuggestions.innerHTML = '';
  _suggestionState.items = [];
  _suggestionState.selectedIdx = -1;
}

function getSelectedSuggestion() {
  if (_suggestionState.selectedIdx < 0) return null;
  return _suggestionState.items[_suggestionState.selectedIdx] || null;
}

function moveSuggestionSelection(delta) {
  if (_suggestionState.items.length === 0) return;
  _suggestionState.selectedIdx += delta;
  if (_suggestionState.selectedIdx < 0) _suggestionState.selectedIdx = _suggestionState.items.length - 1;
  if (_suggestionState.selectedIdx >= _suggestionState.items.length) _suggestionState.selectedIdx = 0;
  // Update DOM
  el.omniboxSuggestions.querySelectorAll('.omnibox-suggestion').forEach((node, i) => {
    node.classList.toggle('selected', i === _suggestionState.selectedIdx);
    if (i === _suggestionState.selectedIdx) {
      node.scrollIntoView({ block: 'nearest' });
    }
  });
}

function navigateToSuggestion(item, direct) {
  hideSuggestions();
  el.omnibox.value = item.url;
  el.omnibox.blur();
  if (direct) {
    // Use the "直接打开" path (bypass iframe, for z.ai etc.)
    openDirect();
  } else {
    submitOmnibox();
  }
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}
function escapeAttr(s) {
  return escapeHtml(s);
}

// ----------------------------- Bookmarks -----------------------------
function toggleBookmark() {
  const t = activeTab();
  if (!t || t.url === 'about:newtab') return;
  const idx = state.bookmarks.findIndex(b => b.url === t.url);
  if (idx >= 0) {
    state.bookmarks.splice(idx, 1);
  } else {
    state.bookmarks.push({ url: t.url, title: t.title, ts: Date.now() });
  }
  saveJSON('samweb.bookmarks', state.bookmarks);
  updateBookmarkButton(t.url);
  if (!el.bookmarksPopover.classList.contains('hidden')) renderBookmarksPopover();
}

function updateBookmarkButton(url) {
  const isMarked = state.bookmarks.some(b => b.url === url);
  el.bookmark.classList.toggle('active', isMarked);
}

// ----------------------------- Popovers -----------------------------
function togglePopover(name) {
  const wasOpen = !el.popoverMask.classList.contains('hidden');
  closePopovers();
  if (wasOpen) return;
  el.popoverMask.classList.remove('hidden');
  if (name === 'engine') { el.enginePopover.classList.remove('hidden'); renderEnginePopover(); }
  if (name === 'history') { el.historyPopover.classList.remove('hidden'); renderHistoryPopover(); }
  if (name === 'bookmarks') { el.bookmarksPopover.classList.remove('hidden'); renderBookmarksPopover(); }
  if (name === 'profile') { el.profilePopover.classList.remove('hidden'); renderProfilePopover(); }
}

function closePopovers() {
  el.popoverMask.classList.add('hidden');
  el.enginePopover.classList.add('hidden');
  el.historyPopover.classList.add('hidden');
  el.bookmarksPopover.classList.add('hidden');
  el.profilePopover.classList.add('hidden');
}

function renderEnginePopover() {
  el.engineList.innerHTML = '';
  for (const e of state.config.engines) {
    const node = document.createElement('div');
    node.className = 'engine-item' + (e.Name === state.engine ? ' selected' : '');
    node.innerHTML = `<span class="dot"></span><span>${escapeHTML(e.Name)}</span>`;
    node.addEventListener('click', () => {
      state.engine = e.Name;
      localStorage.setItem('samweb.engine', e.Name);
      renderEnginePopover();
      closePopovers();
    });
    el.engineList.appendChild(node);
  }
}

function renderHistoryPopover() {
  el.historyList.innerHTML = '';
  if (state.history.length === 0) {
    el.historyList.innerHTML = '<div class="empty">No history yet</div>';
    return;
  }
  for (const h of state.history.slice(0, 50)) {
    const node = document.createElement('div');
    node.className = 'link-item';
    node.innerHTML = `<span class="link-title">${escapeHTML(prettyURL(h.url))}</span><span class="link-url">${escapeHTML(hostOf(h.url))}</span>`;
    node.addEventListener('click', () => { navigate(h.url); closePopovers(); });
    el.historyList.appendChild(node);
  }
}

function renderBookmarksPopover() {
  el.bookmarksList.innerHTML = '';
  if (state.bookmarks.length === 0) {
    el.bookmarksList.innerHTML = '<div class="empty">No bookmarks yet</div>';
    return;
  }
  for (const b of state.bookmarks) {
    const node = document.createElement('div');
    node.className = 'link-item';
    node.innerHTML = `<span class="link-title">${escapeHTML(b.title || prettyURL(b.url))}</span><span class="link-url">${escapeHTML(hostOf(b.url))}</span><button class="link-remove" title="Remove"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg></button>`;
    node.addEventListener('click', (e) => {
      if (e.target.closest('.link-remove')) {
        e.stopPropagation();
        state.bookmarks = state.bookmarks.filter(x => x.url !== b.url);
        saveJSON('samweb.bookmarks', state.bookmarks);
        renderBookmarksPopover();
        updateBookmarkButton(activeTab() ? activeTab().url : '');
      } else {
        navigate(b.url);
        closePopovers();
      }
    });
    el.bookmarksList.appendChild(node);
  }
}

// ----------------------------- New tab page -----------------------------
function newTabPageHTML() {
  const engineName = state.engine || 'Google';
  const placeholder = `Search ${engineName} or type a URL`;
  return `<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body { height: 100%; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans", "Noto Sans SC", sans-serif; }
  body {
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    background: #ffffff; color: #202124; padding: 32px;
  }
  .logo { font-size: 56px; font-weight: 500; letter-spacing: -1px; margin-bottom: 32px; color: #4285f4; }
  .logo .a { color: #ea4335; } .logo .b { color: #fbbc05; } .logo .c { color: #4285f4; } .logo .d { color: #34a853; } .logo .e { color: #ea4335; }
  form { width: 100%; max-width: 580px; }
  .search-wrap {
    display: flex; align-items: center; height: 46px; padding: 0 18px;
    border: 1px solid #dadce0; border-radius: 24px;
    box-shadow: 0 1px 6px rgba(32,33,36,0.18);
    background: #ffffff; transition: box-shadow 200ms ease;
  }
  .search-wrap:focus-within { box-shadow: 0 1px 8px rgba(32,33,36,0.28); }
  .search-icon { color: #9aa0a6; margin-right: 12px; flex-shrink: 0; }
  input {
    flex: 1; border: none; outline: none; height: 100%;
    font-size: 16px; color: #202124; background: transparent;
  }
  .shortcuts { display: flex; gap: 24px; margin-top: 48px; flex-wrap: wrap; justify-content: center; }
  .shortcut {
    display: flex; flex-direction: column; align-items: center; gap: 8px;
    cursor: pointer; padding: 12px; border-radius: 12px; width: 96px;
    text-decoration: none; color: #202124;
  }
  .shortcut:hover { background: #f1f3f4; }
  .shortcut-icon {
    width: 48px; height: 48px; border-radius: 50%; background: #f1f3f4;
    display: flex; align-items: center; justify-content: center;
    font-size: 22px; color: #5f6368; font-weight: 600;
  }
  .shortcut-label { font-size: 12px; color: #3c4043; }
  .footer { position: absolute; bottom: 16px; right: 20px; font-size: 11px; color: #9aa0a6; }
</style></head>
<body>
  <div class="logo"><span class="a">S</span><span class="b">a</span><span class="c">m</span><span class="d">W</span><span class="e">e</span><span class="a">b</span></div>
  <form id="f" onsubmit="return doSearch();">
    <div class="search-wrap">
      <span class="search-icon">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>
      </span>
      <input id="q" type="text" autocomplete="off" placeholder="${escapeAttr(placeholder)}" autofocus />
    </div>
  </form>
  <div class="shortcuts">
    ${shortcut('Google', 'https://www.google.com', 'G')}
    ${shortcut('YouTube', 'https://www.youtube.com', 'Y')}
    ${shortcut('GitHub', 'https://github.com', 'H')}
    ${shortcut('Wikipedia', 'https://www.wikipedia.org', 'W')}
    ${shortcut('Bing', 'https://www.bing.com', 'B')}
    ${shortcut('Baidu', 'https://www.baidu.com', '百')}
  </div>
  <div class="footer">SamWeb &middot; Built with Go + WebKit</div>
  <script>
    function doSearch() {
      var q = document.getElementById('q').value.trim();
      if (!q) return false;
      // Talk to the parent window so the omnibox + history are kept in sync.
      parent.postMessage({ type: 'samweb-navigate', input: q }, '*');
      return false;
    }
    document.querySelectorAll('.shortcut').forEach(function(s) {
      s.addEventListener('click', function() {
        parent.postMessage({ type: 'samweb-navigate-url', url: s.dataset.url }, '*');
      });
    });
  <\/script>
</body></html>`;
}

function shortcut(label, url, glyph) {
  return `<a class="shortcut" data-url="${escapeAttr(url)}"><div class="shortcut-icon">${escapeHTML(glyph)}</div><div class="shortcut-label">${escapeHTML(label)}</div></a>`;
}

// Listen for navigation requests coming from the iframe (new-tab page).
window.addEventListener('message', (e) => {
  const msg = e.data || {};
  if (msg.type === 'samweb-navigate') {
    resolveOmnibox(msg.input).then(u => navigate(u));
  } else if (msg.type === 'samweb-navigate-url') {
    navigate(msg.url);
  }
});

// ----------------------------- Utils -----------------------------
function loadJSON(key, dflt) {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return dflt;
    return JSON.parse(raw);
  } catch (_) { return dflt; }
}
function saveJSON(key, val) {
  try { localStorage.setItem(key, JSON.stringify(val)); } catch (_) {}
}
async function fetchJSON(url) {
  const r = await fetch(url);
  return r.json();
}
function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]);
}
function escapeAttr(s) {
  return escapeHTML(s);
}
function prettyURL(url) {
  if (url === 'about:newtab') return 'New Tab';
  return url.replace(/^https?:\/\//, '').replace(/\/$/, '');
}
function hostOf(url) {
  try { return new URL(url).host; } catch (_) { return ''; }
}

// ----------------------------- Agent exports -----------------------------
// The agent JS bridge (defined via webview.Init in Go) calls these global
// functions to drive the browser. We export them on window so they're
// reachable from the eval'd bootstrap code.

window.navigate = navigate;
window.goBack = goBack;
window.goForward = goForward;
window.reloadActive = reloadActive;

window.getActiveTabId = function() {
  return state.activeTabId;
};

window.getTabsState = function() {
  return state.tabs.map(t => ({ id: t.id, title: t.title, url: t.url }));
};

window.canBack = function() {
  const t = activeTab();
  return !!(t && t.histIndex > 0);
};

window.canForward = function() {
  const t = activeTab();
  return !!(t && t.histIndex < t.history.length - 1);
};

// ----------------------------- Profiles (multi-account cookie sets) -----------------------------
//
// Profiles let the user save the current browser cookies under a name
// (e.g. "z.ai account A") and switch between them. This enables running
// multiple accounts on the same site simultaneously in a single samweb
// instance. The Go backend handles the actual cookie snapshot/restore
// via CDP; the UI just calls the HTTP API.

async function refreshProfiles() {
  try {
    const r = await fetchJSON('/api/config'); // ensure server is up
    void r;
    const resp = await fetch('/agent/profiles', {
      headers: { 'Accept': 'application/json' },
    });
    if (!resp.ok) {
      console.warn('profile list fetch failed:', resp.status);
      return;
    }
    const data = await resp.json();
    state.profiles = data.profiles || [];
    state.activeProfileId = data.active_profile_id || '';
    renderProfileSelect();
  } catch (e) {
    console.warn('refreshProfiles error:', e);
  }
}

function renderProfileSelect() {
  // Preserve current selection (or use state.activeProfileId)
  const prev = el.profileSelect.value || state.activeProfileId || '';
  el.profileSelect.innerHTML = '';
  // Default option: no profile
  const defOpt = document.createElement('option');
  defOpt.value = '';
  defOpt.textContent = '默认（无 profile）';
  el.profileSelect.appendChild(defOpt);
  for (const p of state.profiles) {
    const opt = document.createElement('option');
    opt.value = p.id;
    opt.textContent = `${p.name} (${p.cookie_count} cookies)`;
    el.profileSelect.appendChild(opt);
  }
  // Restore selection
  if (prev && state.profiles.some(p => p.id === prev)) {
    el.profileSelect.value = prev;
  } else {
    el.profileSelect.value = state.activeProfileId || '';
  }
}

function renderProfilePopover() {
  el.profileList.innerHTML = '';
  if (state.profiles.length === 0) {
    el.profileList.innerHTML = '<div class="empty">No profiles yet. Save current cookies to create one.</div>';
    return;
  }
  for (const p of state.profiles) {
    const node = document.createElement('div');
    node.className = 'profile-item' + (p.id === state.activeProfileId ? ' active' : '');
    const updated = new Date(p.updated * 1000).toLocaleString();
    node.innerHTML = `
      <span class="profile-name">${escapeHTML(p.name)}</span>
      <span class="profile-meta">${p.cookie_count} cookies · ${escapeHTML(updated)}</span>
      <div class="profile-actions">
        <button class="profile-rename" title="Rename">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
        </button>
        <button class="profile-delete" title="Delete">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"></path></svg>
        </button>
      </div>`;
    node.addEventListener('click', async (e) => {
      if (e.target.closest('.profile-rename')) {
        e.stopPropagation();
        const newName = prompt('Rename profile to:', p.name);
        if (newName && newName.trim()) {
          await renameProfile(p.id, newName.trim());
        }
      } else if (e.target.closest('.profile-delete')) {
        e.stopPropagation();
        if (confirm(`Delete profile "${p.name}"? This cannot be undone.`)) {
          await deleteProfile(p.id);
        }
      } else {
        // Click on the row switches to this profile
        await switchProfile(p.id);
        closePopovers();
      }
    });
    el.profileList.appendChild(node);
  }
}

async function createProfileFromInput() {
  const name = el.profileNewName.value.trim();
  if (!name) {
    el.profileNewName.focus();
    return;
  }
  el.profileCreateBtn.disabled = true;
  el.profileCreateBtn.textContent = 'Saving...';
  try {
    const resp = await fetch('/agent/profiles', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json',
                 'Authorization': 'Bearer test-token-2026' },
      body: JSON.stringify({ name }),
    });
    if (!resp.ok) {
      const err = await resp.text();
      alert('Failed to save profile: ' + err);
      return;
    }
    const prof = await resp.json();
    el.profileNewName.value = '';
    await refreshProfiles();
    // DO NOT auto-switch to the newly created profile. The user is in
    // the middle of a login-and-save flow: they navigated to z.ai,
    // logged in, came back, and hit save. Auto-switching would clear
    // all storage (wiping the login they just saved) and inject the
    // profile's storage back — which is fine, but it also sets the
    // skip-clear flag, causing the NEXT "直接打开" to z.ai to NOT clear
    // storage, so z.ai would still show the just-saved account's
    // login state, making it impossible to log in to a different
    // account for the next profile.
    //
    // Instead, just keep the user on whatever state they're in. They
    // can manually switch profiles from the dropdown when they want.
    renderProfilePopover();
    // Brief visual feedback
    el.profileCreateBtn.textContent = '✓ Saved';
    setTimeout(() => { el.profileCreateBtn.textContent = 'Save current'; }, 1500);
  } catch (e) {
    alert('Error saving profile: ' + e.message);
  } finally {
    el.profileCreateBtn.disabled = false;
  }
}

async function quickSaveProfile() {
  // Quick-save: update the current profile (if any) or prompt for a new name
  if (state.activeProfileId) {
    const active = state.profiles.find(p => p.id === state.activeProfileId);
    if (active) {
      // Update existing
      try {
        const resp = await fetch('/agent/profiles', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json',
                     'Authorization': 'Bearer test-token-2026' },
          body: JSON.stringify({ name: active.name }),
        });
        if (resp.ok) {
          await refreshProfiles();
          flashProfileSaved();
        } else {
          alert('Failed to update profile: ' + await resp.text());
        }
      } catch (e) {
        alert('Error: ' + e.message);
      }
      return;
    }
  }
  // No active profile — prompt for a new name
  const name = prompt('Save current cookies to a new profile. Profile name:');
  if (name && name.trim()) {
    try {
      const resp = await fetch('/agent/profiles', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json',
                   'Authorization': 'Bearer test-token-2026' },
        body: JSON.stringify({ name: name.trim() }),
      });
      if (resp.ok) {
        const prof = await resp.json();
        await refreshProfiles();
        // DO NOT auto-switch (see createProfileFromInput for rationale)
        flashProfileSaved();
      } else {
        alert('Failed: ' + await resp.text());
      }
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }
}

function flashProfileSaved() {
  // Brief visual feedback — change icon color for 800ms
  el.profileSaveBtn.style.color = '#34a853';
  setTimeout(() => { el.profileSaveBtn.style.color = ''; }, 800);
}

async function switchProfile(id) {
  // Don't switch if already on this profile
  if (id === state.activeProfileId && id !== '') return;
  const prof = id ? state.profiles.find(p => p.id === id) : null;
  const profName = prof ? prof.name : '空 profile';
  if (!confirm('切换到 profile：' + profName + '\n\n' +
               (id ? '将清空当前所有 cookies + localStorage，然后注入该 profile 的登录态。' :
                     '将清空当前所有 cookies + localStorage（回到匿名状态）。') +
               '\n\n继续？')) {
    el.profileSelect.value = state.activeProfileId || '';
    return;
  }
  try {
    const resp = await fetch('/agent/profiles/switch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json',
                 'Authorization': 'Bearer test-token-2026' },
      body: JSON.stringify({ id }),
    });
    if (!resp.ok) {
      alert('Failed to switch profile: ' + await resp.text());
      el.profileSelect.value = state.activeProfileId || '';
      return;
    }
    state.activeProfileId = id;
    if (id) {
      alert('已切换到 profile：' + profName + '\n\n' +
            '后端正在注入 cookies + localStorage（如果 profile 里有 localStorage，会先跳到目标网站注入再跳回，约需 5-10 秒）。\n' +
            '注入完成后，点"直接打开"按钮去 z.ai，应该已经是登录状态。');
    } else {
      alert('已切换到空 profile，所有 cookies + localStorage 已清空。');
    }
  } catch (e) {
    alert('Error switching profile: ' + e.message);
    el.profileSelect.value = state.activeProfileId || '';
  }
}

async function renameProfile(id, newName) {
  try {
    const resp = await fetch('/agent/profiles/rename', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, name: newName }),
    });
    if (!resp.ok) {
      alert('Failed to rename: ' + await resp.text());
      return;
    }
    await refreshProfiles();
    renderProfilePopover();
  } catch (e) {
    alert('Error: ' + e.message);
  }
}

async function deleteProfile(id) {
  try {
    const resp = await fetch('/agent/profiles/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id }),
    });
    if (!resp.ok) {
      alert('Failed to delete: ' + await resp.text());
      return;
    }
    // If the deleted profile was active, fall back to default
    if (state.activeProfileId === id) {
      state.activeProfileId = '';
    }
    await refreshProfiles();
    renderProfilePopover();
  } catch (e) {
    alert('Error: ' + e.message);
  }
}

// Export profile functions for agent control
window.refreshProfiles = refreshProfiles;
window.switchProfile = switchProfile;
window.quickSaveProfile = quickSaveProfile;

