import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };

// Dutch words that should not appear in the English interface (content marked translate="no" is skipped).
const DUTCH = /\b(de|het|een|van|voor|niet|wordt|worden|nieuws|bijgewerkt|geleden|instellingen|bronnen|berichten|meldingen|zoek|opslaan|verkeer|storingen|minuten|uur|vandaag|gisteren|meer|alle|geen|nog|naar|bij|tot|kies|toon|verberg|bewaren|bewaard|volglijst|panelen|weergave|taal|licht|donker|sluiten|wijzigen|gevoel|vocht|droog|onbekend|dreigingsniveau|waarschuwing|alarmeringen|plaats|locatie|standaard|terug|aan|uit|met|zonder|op|je|jouw|ophalen|opgehaald)\b/i;
const ALLOW = /^(Nieuws Hub|NL|EN|Nederlands|English|Auto|KNMI|NCTV|NCSC|MeteoAlarm|Open-Meteo|Buienradar|Zwaailicht\.nl|P2000)$/;

async function leftovers(p, root = 'body') {
  return p.evaluate(([root, src, flags]) => {
    const re = new RegExp(src, flags), out = new Set();
    const skip = el => el.closest('[translate="no"], script, style, noscript');
    const w = document.createTreeWalker(document.querySelector(root), NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
    for (let n; (n = w.nextNode());) {
      if (n.nodeType === 3) {
        if (skip(n.parentElement)) continue;
        const s = n.nodeValue.trim();
        if (s && re.test(s)) out.add(s.slice(0, 120));
      } else {
        if (skip(n)) continue;
        for (const a of ['aria-label', 'title', 'placeholder']) {
          const s = n.getAttribute(a);
          if (s && re.test(s) && !/Den Haag/.test(s)) out.add(`[${a}] ${s.slice(0, 120)}`);
        }
      }
    }
    return [...out];
  }, [root, DUTCH.source, DUTCH.flags]);
}

// 1. Default is Dutch; header switch visible on desktop, hidden on phones.
{
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'en-US' });
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#stream .item');
  const t = await p.evaluate(() => ({ lang: document.documentElement.lang, seg: getComputedStyle(document.querySelector('#lang-seg')).display,
    checked: document.querySelector('input[name="lang"]:checked')?.value, settings: document.querySelector('#open-settings').getAttribute('aria-label') }));
  ok(t.lang === 'nl' && t.checked === 'nl' && t.settings === 'Instellingen', `default Dutch even with an English browser (lang=${t.lang}, checked=${t.checked})`);
  ok(t.seg !== 'none', 'desktop: NL/EN/Auto switch in the header');

  // 2. Switch to English with the header button.
  await Promise.all([p.waitForEvent('load'), p.click('#lang-seg label:has-text("EN")')]);
  await p.waitForSelector('#stream .item'); await p.waitForSelector('#panel-weather .wxnow'); await p.waitForTimeout(3000);
  const e = await p.evaluate(() => ({
    lang: document.documentElement.lang, pending: document.documentElement.classList.contains('i18n-pending'),
    settings: document.querySelector('#open-settings').getAttribute('aria-label'),
    heads: [...document.querySelectorAll('.panel h2')].map(x => x.textContent.trim()),
    clock: document.querySelector('#clock')?.textContent, wx: document.querySelector('#panel-weather .wxnow .sub')?.textContent,
    fresh: document.querySelector('#panel-weather .fresh')?.textContent, stored: JSON.parse(localStorage.getItem('ndb:prefs') || '{}').lang,
    titleLang: document.querySelector('#stream .item')?.getAttribute('translate'),
  }));
  console.log(JSON.stringify(e, null, 1));
  ok(e.lang === 'en' && !e.pending && e.stored === 'en', 'English after the switch, remembered, page visible');
  ok(e.settings === 'Settings', `header button: "${e.settings}"`);
  ok(e.heads.includes('Weather') && e.heads.includes('Traffic') && e.heads.some(x => /Dutch Data Protection Authority/.test(x)), `panel headings: ${e.heads.join(' | ')}`);
  ok(/^(Mon|Tue|Wed|Thu|Fri|Sat|Sun)/.test(e.clock || ''), `clock in English: "${e.clock}"`);
  ok(/^feels .* · wind [NESW]{1,3} \d+ \(.+\) · \d+% humidity$/.test(e.wx || ''), `weather line: "${e.wx}"`);
  ok(/updated/.test(e.fresh || ''), `freshness: "${e.fresh}"`);
  ok(e.titleLang === 'no', 'news items (content) are not translated');
  const left = await leftovers(p);
  ok(left.length === 0, `page: no Dutch interface text left (${left.length})`);
  for (const s of left.slice(0, 40)) console.log('   - ' + s);

  // Settings dialog in English, and switch back from there.
  await p.click('#open-settings'); await p.waitForSelector('#settings[open]'); await p.waitForTimeout(800);
  const ls = await leftovers(p, '#settings');
  ok(ls.length === 0, `settings: no Dutch interface text left (${ls.length})`);
  for (const s of ls.slice(0, 40)) console.log('   - ' + s);
  const sh = await p.evaluate(() => [...document.querySelectorAll('#settings h3, #settings h2')].map(x => x.textContent.trim()));
  ok(sh.includes('Weather location') && sh.includes('Sources'), `settings headings: ${sh.join(' | ')}`);
  for (const id of ['#keys', '#status-dlg', '#changes-dlg']) {
    const l = await leftovers(p, id);
    ok(l.length === 0, `${id}: no Dutch interface text left (${l.length})`);
    for (const s of l.slice(0, 20)) console.log('   - ' + s);
  }
  const ax = await new AxeBuilder({ page: p }).analyze();
  ok(ax.violations.length === 0, `axe (English, settings open): ${ax.violations.length} violations`);
  for (const v of ax.violations) console.log(`   - ${v.id}: ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`);
  await p.screenshot({ path: `${OUT}/i18n-settings-en.png` });
  await Promise.all([p.waitForEvent('load'), p.click('#lang-seg2 label:has-text("Nederlands")')]);
  await p.waitForSelector('#stream .item');
  const back = await p.evaluate(() => [document.documentElement.lang, document.querySelector('#open-settings').getAttribute('aria-label')]);
  ok(back[0] === 'nl' && back[1] === 'Instellingen', 'back to Dutch from the settings');

  // Auto follows the browser.
  await p.evaluate(() => { const x = JSON.parse(localStorage.getItem('ndb:prefs')); x.lang = 'auto'; localStorage.setItem('ndb:prefs', JSON.stringify(x)); });
  await p.reload(); await p.waitForSelector('#stream .item');
  ok(await p.evaluate(() => document.documentElement.lang) === 'en', 'Auto + English browser = English');
  await p.screenshot({ path: `${OUT}/i18n-en-1440.png` });
  await ctx.close();
}
{
  const ctx = await b.newContext({ viewport: { width: 360, height: 800 }, locale: 'nl-NL' });
  const p = await ctx.newPage();
  await p.goto(URL); await p.evaluate(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, lang: 'auto' })));
  await p.reload(); await p.waitForSelector('#stream');
  const t = await p.evaluate(() => ({ lang: document.documentElement.lang, seg: getComputedStyle(document.querySelector('#lang-seg')).display, sw: document.documentElement.scrollWidth }));
  ok(t.lang === 'nl', 'Auto + Dutch browser = Dutch');
  ok(t.seg === 'none' && t.sw <= 360, 'phone: header switch hidden (in settings), no horizontal scroll');
  await p.evaluate(() => { const x = JSON.parse(localStorage.getItem('ndb:prefs')); x.lang = 'en'; localStorage.setItem('ndb:prefs', JSON.stringify(x)); });
  await p.reload(); await p.waitForSelector('#stream .item'); await p.waitForTimeout(2000);
  ok(await p.evaluate(() => document.documentElement.scrollWidth) <= 360, 'phone English: no horizontal scroll');
  await p.screenshot({ path: `${OUT}/i18n-en-360.png` });
  await ctx.close();
}

// 3. A) Weather: Opslaan button and Enter pick the best match.
{
  const ctx = await b.newContext({ viewport: { width: 1280, height: 900 } });
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#stream');
  await p.click('#open-settings'); await p.waitForSelector('#settings[open]');
  const pos = await p.evaluate(() => { const a = document.querySelector('#wx-search').getBoundingClientRect(), b = document.querySelector('#wx-save').getBoundingClientRect();
    return { same: Math.abs(a.top - b.top) < 8, after: b.left > a.right - 1, h: b.height }; });
  ok(pos.same && pos.after && pos.h >= 24, 'Opslaan button right after the search field');
  await p.click('#wx-save');
  ok(/Zoek een plaats en druk op Opslaan/.test(await p.textContent('#wx-msg')), 'Opslaan with an empty field explains what to do');
  await p.fill('#wx-search', 'Amersfoort'); await p.click('#wx-save');
  await p.waitForFunction(() => /Amersfoort/.test(document.querySelector('#wx-msg').textContent));
  const w = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).weather);
  ok(w?.name === 'Amersfoort' && Math.abs(w.lat - 52.16) < 0.1, `Opslaan stores ${JSON.stringify(w)}`);
  await p.fill('#wx-search', 'Maastricht'); await p.press('#wx-search', 'Enter');
  await p.waitForFunction(() => /Maastricht/.test(document.querySelector('#wx-msg').textContent));
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).weather.name) === 'Maastricht', 'Enter saves as well');
  await p.fill('#wx-search', 'Xyzzyqwv'); await p.click('#wx-save');
  await p.waitForFunction(() => /Geen plaats gevonden/.test(document.querySelector('#wx-msg').textContent));
  ok(true, 'unknown place: clear message');
  await ctx.close();
}

// 4. C) Refresh intervals come from config.yaml.
{
  const r = await (await fetch(URL + 'api/catalog')).json();
  ok(r.refresh?.news === 300 && r.refresh?.alarms === 120 && Object.keys(r.refresh).length === 34 && r.refresh.breaches === 1800, `catalog refresh: ${JSON.stringify(r.refresh)}`);
  ok(r.categories.find(c => c.id === 'be')?.name_en === 'Belgium' && r.presets.find(x => x.id === 'kort')?.name_en === 'Quick overview', 'English names from config.yaml');
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
