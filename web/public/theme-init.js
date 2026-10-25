// Applies the persisted theme before first paint.
// Kept as an external file (not inline) so the strict CSP
// `script-src 'self'` does not block it.
(function () {
  try {
    var t = localStorage.getItem('myrouter-theme');
    if (t !== 'light' && t !== 'dark' && t !== 'system') t = 'light';
    var dark =
      t === 'dark' ||
      (t === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
  } catch (e) {
    document.documentElement.setAttribute('data-theme', 'light');
  }
})();
