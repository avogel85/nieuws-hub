// Test shim for the suites written before the phone views (1.13): on phones they
// expect news and panels stacked, so every new context shows both views (the
// phone views themselves are tested in swipe.mjs).
export function showBothViews(browser) {
  const nc = browser.newContext.bind(browser);
  browser.newContext = async (o) => {
    const c = await nc(o);
    await c.addInitScript(() => {
      const fix = () => { const r = document.documentElement; if (r?.dataset.mview && r.dataset.mview !== 'all') r.dataset.mview = 'all'; };
      const start = () => {
        if (!document.documentElement) return;
        new MutationObserver(fix).observe(document.documentElement, { attributes: true, attributeFilter: ['data-mview'] });
        fix();
      };
      if (document.documentElement) start(); else document.addEventListener('readystatechange', start, { once: true });
      document.addEventListener('DOMContentLoaded', () => { fix(); const s = document.createElement('style'); s.textContent = '.mview { display: none !important; }'; document.head.append(s); });
    });
    return c;
  };
}
