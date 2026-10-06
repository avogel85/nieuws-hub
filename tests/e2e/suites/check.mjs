import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const out = OUT + '/';
const browser = await chromium.launch();
const log = (...a) => console.log(...a);
let fails = 0; const ok = (c, m) => { log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };

// 1. No flash: record theme + body background at the moment <body> is created.
for (const [stored, scheme, wantBg] of [['dark','light','rgb(0, 0, 0)'], ['light','dark','rgb(255, 255, 255)'], ['auto','dark','rgb(0, 0, 0)'], ['auto','light','rgb(255, 255, 255)']]) {
  const ctx = await browser.newContext({ colorScheme: scheme });
  await ctx.addInitScript(t => {
    try { localStorage.setItem('ndb:prefs', JSON.stringify({ theme: t })); } catch {}
    new MutationObserver((m, o) => { if (document.body) { window.__first = { theme: document.documentElement.getAttribute('data-theme'), bg: getComputedStyle(document.body).backgroundColor }; o.disconnect(); } })
      .observe(document, { childList: true, subtree: true });
  }, stored);
  const p = await ctx.newPage();
  await p.goto(URL);
  const f = await p.evaluate(() => window.__first);
  ok(f && f.bg === wantBg, `no-flash: stored=${stored} system=${scheme} → first paint bg ${f && f.bg} (want ${wantBg})`);
  await ctx.close();
}

// 2. Widths × themes: overflow check + screenshots.
for (const w of [360, 768, 1440]) for (const scheme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: w, height: w === 360 ? 780 : 900 }, colorScheme: scheme, deviceScaleFactor: 1 });
  const p = await ctx.newPage();
  await p.goto(URL);
  await p.waitForSelector('#stream .item', { timeout: 15000 });
  const m = await p.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: innerWidth, items: document.querySelectorAll('#stream .item').length, cols: getComputedStyle(document.querySelector('.layout')).gridTemplateColumns }));
  ok(m.sw <= m.iw, `width ${w} ${scheme}: no horizontal scroll (scrollWidth ${m.sw}, viewport ${m.iw}), ${m.items} items, grid "${m.cols}"`);
  await p.screenshot({ path: `${out}${w}-${scheme}.png` });
  if (w === 1440) await p.screenshot({ path: `${out}${w}-${scheme}-full.png`, fullPage: true });
  if (w === 360 && scheme === 'light') {
    await p.click('#search-open'); await p.keyboard.type('kabinet');
    await p.waitForTimeout(300);
    await p.screenshot({ path: `${out}360-search.png` });
  }
  await ctx.close();
}

// 2b. No stray "null"/"undefined" text anywhere (regression: header mini weather showed "18° null").
{
  const p = await (await browser.newContext({ viewport: { width: 1440, height: 900 } })).newPage();
  await p.goto(URL);
  await p.waitForSelector('#wx-mini:not([hidden])'); await p.waitForSelector('#panel-threats .spark'); await p.waitForSelector('#panel-advisories .adv');
  const mini = (await p.textContent('#wx-mini')).trim();
  ok(/^-?\d+°$/.test(mini), `header mini weather text is just the temperature: "${mini}"`);
  await p.click('#open-settings'); await p.waitForTimeout(800);
  const stray = await p.evaluate(() => {
    const out = [], w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n; (n = w.nextNode());) if (/\b(null|undefined|NaN)\b/.test(n.textContent) && !n.parentElement.closest('script,textarea')) out.push(n.parentElement.tagName + ': ' + n.textContent.trim().slice(0, 60));
    return out;
  });
  ok(stray.length === 0, `no "null"/"undefined"/"NaN" text nodes on the page (found: ${stray.join(' | ') || 'none'})`);
}

// 3. Keyboard-only navigation.
{
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const p = await ctx.newPage();
  await p.goto(URL);
  await p.waitForSelector('#stream .item');
  const seq = [];
  for (let i = 0; i < 16; i++) {
    await p.keyboard.press('Tab');
    seq.push(await p.evaluate(() => {
      const e = document.activeElement, cs = getComputedStyle(e);
      const lbl = e.getAttribute('aria-label') || e.textContent.trim().slice(0, 30) || e.value || e.name;
      return `${e.tagName.toLowerCase()}${e.id ? '#' + e.id : ''}[${lbl}] outline=${cs.outlineStyle !== 'none' || getComputedStyle(e.closest('label') || e).outlineStyle !== 'none' ? 'yes' : 'NO'}`;
    }));
  }
  log('  tab order:\n   ' + seq.join('\n   '));
  ok(seq[0].includes('Naar het nieuws') && seq[1].includes('Naar weer'), 'first Tabs reach both skip links');
  ok(seq.every(s => s.endsWith('outline=yes')), 'every focused element shows a focus ring');
  // skip link works
  await p.keyboard.press('Home'); await p.goto(URL); await p.waitForSelector('#stream .item');
  await p.keyboard.press('Tab'); await p.keyboard.press('Enter');
  ok(await p.evaluate(() => location.hash === '#news'), 'skip link moves to #news');
  // '/' focuses search
  await p.locator('body').click({ position: { x: 5, y: 400 } });
  await p.keyboard.press('/');
  ok(await p.evaluate(() => document.activeElement.id === 'q'), '"/" focuses the search box');
  await p.keyboard.type('zzzxxyy'); await p.waitForTimeout(250);
  ok(await p.locator('#stream .empty').count() === 1, 'search with no results shows empty state');
  await p.keyboard.press('Escape'); await p.waitForTimeout(200);
  ok(await p.locator('#stream .item').count() > 0, 'Escape clears the search');
  // category chip via keyboard
  await p.focus('#chips .chip:nth-child(2)'); await p.keyboard.press('Enter');
  const cat = await p.evaluate(() => document.querySelector('#chips [aria-pressed="true"]').dataset.cat);
  ok(cat !== 'all', `chip activation by keyboard (category ${cat})`);
  await p.focus('#chips [data-cat="all"]'); await p.keyboard.press('Enter');
  // panel collapse with keyboard, persisted
  await p.focus('#panel-ap .ptoggle'); await p.keyboard.press('Enter');
  ok(await p.getAttribute('#panel-ap .ptoggle', 'aria-expanded') === 'false', 'panel collapses with Enter');
  await p.reload(); await p.waitForSelector('#panel-ap');
  ok(await p.getAttribute('#panel-ap .ptoggle', 'aria-expanded') === 'false', 'collapse state persists after reload');
  await p.focus('#panel-ap .ptoggle'); await p.keyboard.press('Space');
  // settings dialog: open, Escape closes, focus returns
  await p.focus('#open-settings'); await p.keyboard.press('Enter');
  ok(await p.evaluate(() => document.querySelector('#settings').open), 'settings opens with Enter');
  ok(await p.evaluate(() => document.querySelector('#settings').contains(document.activeElement)), 'focus moves into settings');
  await p.waitForTimeout(600);
  await p.screenshot({ path: `${out}1440-settings.png` });
  await p.keyboard.press('Escape');
  ok(await p.evaluate(() => !document.querySelector('#settings').open && document.activeElement.id === 'open-settings'), 'Escape closes settings, focus returns to ⚙');
  // theme via keyboard radios
  await p.focus('#theme-seg input[value="dark"]'); await p.keyboard.press('Space');
  ok(await p.evaluate(() => document.documentElement.dataset.theme === 'dark' && JSON.parse(localStorage.getItem('ndb:prefs')).theme === 'dark'), 'theme switch via keyboard persists');
  // source toggle → news reload with new sources (pick a currently healthy, not-yet-enabled source)
  const pick = await p.evaluate(async () => {
    const [h, c] = await Promise.all([fetch('healthz').then(r => r.json()), fetch('api/catalog').then(r => r.json())]);
    const s = c.sources.find(s => !s.default_enabled && h.sources[s.id]?.ok && h.sources[s.id].items > 0);
    return s && { id: s.id, name: s.name };
  });
  await p.click('#open-settings');
  await p.check(`#src-groups input[data-id="${pick.id}"]`);
  await p.keyboard.press('Escape');
  await p.waitForResponse(r => r.url().includes('api/news') && r.url().includes(pick.id));
  await p.waitForTimeout(400);
  await p.fill('#q', pick.name); await p.waitForTimeout(300);
  ok(await p.locator('#stream .item .src', { hasText: pick.name }).count() > 0, `enabling a source (${pick.name}) reloads the stream with it`);
  await p.fill('#q', ''); await p.waitForTimeout(300);
  // export/import round trip
  await p.click('#open-settings');
  const json = await p.inputValue('#prefs-json');
  const mod = JSON.parse(json); mod.density = 'comfortable'; mod.pageSize = 30;
  await p.fill('#prefs-json', JSON.stringify(mod)); await p.click('#prefs-import');
  ok(await p.evaluate(() => document.documentElement.dataset.density === 'comfortable'), 'import applies preferences');
  await p.fill('#prefs-json', '{nope'); await p.click('#prefs-import');
  ok((await p.textContent('#prefs-msg')).includes('geen geldige JSON'), 'invalid import shows an error');
  await ctx.close();
}

// 4. axe-core accessibility scan (both themes, with and without the settings sheet).
for (const scheme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: scheme });
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#stream .item');
  let r = await new AxeBuilder({ page: p }).analyze();
  ok(r.violations.length === 0, `axe ${scheme}: ${r.violations.length} violations`);
  for (const v of r.violations) log(`   - ${v.id} (${v.impact}): ${v.help} → ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`);
  await p.click('#open-settings'); await p.waitForTimeout(500);
  r = await new AxeBuilder({ page: p }).include('#settings').analyze();
  ok(r.violations.length === 0, `axe ${scheme} settings sheet: ${r.violations.length} violations`);
  for (const v of r.violations) log(`   - ${v.id} (${v.impact}): ${v.help} → ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`);
  await ctx.close();
}
await browser.close();
log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
