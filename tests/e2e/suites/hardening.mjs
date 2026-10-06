import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const EVIL = 'http://127.0.0.1:8092/', URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
showBothViews(b);
const EVIL_IDS = (await (await fetch(EVIL + 'api/catalog')).json()).sources.map(s => s.id); // returning visitor: no auto-added defaults
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const watch = p => { const log = { dialogs: [], csp: [], errors: [] };
  p.on('dialog', d => { log.dialogs.push(d.message()); d.dismiss(); });
  p.on('console', m => { if (/Content Security Policy|Refused to/.test(m.text())) log.csp.push(m.text().slice(0, 140)); });
  p.on('pageerror', e => log.errors.push(e.message)); return log; };

// 1. Malicious feed in a real browser
{
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 } });
  await ctx.addInitScript(ids => localStorage.setItem('ndb:prefs', JSON.stringify({ onboarded: true, sources: ['evil'], known: ids })), EVIL_IDS);
  const p = await ctx.newPage(); const log = watch(p);
  await p.goto(EVIL); await p.waitForSelector('#stream .item', { timeout: 40000 });
  await p.hover('#stream .item .sum').catch(() => {}); await p.waitForTimeout(800);
  const dom = await p.evaluate(() => {
    const s = document.querySelector('#stream');
    return {
      titles: [...s.querySelectorAll('.item .t a')].map(a => ({ t: a.textContent, href: a.getAttribute('href') })),
      onAttrs: [...document.querySelectorAll('*')].filter(e => [...e.attributes].some(a => /^on/i.test(a.name))).map(e => e.tagName),
      bad: s.querySelectorAll('script, iframe, img:not(.thumb):not(.sico), object, embed, svg:not(.i):not(.w)').length,
      src: [...s.querySelectorAll('.src span:last-child')].map(x => x.textContent)[0],
    };
  });
  console.log('  rendered titles:', dom.titles.map(x => x.t).join(' | '));
  ok(log.dialogs.length === 0, `evil feed: no script executed (alerts: ${log.dialogs.join(',') || 'none'})`);
  ok(dom.onAttrs.length === 0 && dom.bad === 0, `evil feed: no event-handler attributes or injected elements (${dom.onAttrs.join(',') || 'none'}, ${dom.bad})`);
  ok(dom.titles.every(x => /^https:\/\/evil\.example\//.test(x.href)) && !dom.titles.some(x => /javascript/i.test(x.href)), 'evil feed: only https links rendered');
  ok(dom.titles.some(x => x.t === 'Kop alert(2) één' || x.t === 'Kop één') && dom.titles.some(x => x.t === 'Bidi txt.exe spoof'), 'evil feed: hostile titles shown as harmless text, bidi override removed');
  ok(dom.src === 'Evil <b>feed</b>', 'evil source name from config is shown as text, not markup');
  ok(log.errors.length === 0, `no JavaScript errors (${log.errors.join(' | ') || 'none'})`);
  await ctx.close();
}

// 2. Normal use under the hashed CSP: no violations anywhere
{
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 } });
  const p = await ctx.newPage(); const log = watch(p);
  await p.goto(URL); await p.waitForSelector('#welcome .preset'); await p.click('#welcome .btn:not(.primary)');
  await p.waitForSelector('#panel-threats .spark'); await p.waitForSelector('#panel-advisories .adv');
  await p.click('#open-settings'); await p.waitForTimeout(400); await p.keyboard.press('Escape');
  await p.keyboard.press('?'); await p.keyboard.press('Escape');
  await p.click('#open-changes'); await p.waitForTimeout(200);
  ok(/Versie 1\.1\.0/.test(await p.textContent('#changes-dlg')), '"Wat is er nieuw" lists the changes per version');
  const axeC = await new AxeBuilder({ page: p }).include('#changes-dlg').analyze();
  ok(axeC.violations.length === 0, `axe on changelog: ${axeC.violations.map(v => v.id).join(',') || '0 violations'}`);
  await p.keyboard.press('Escape');
  await p.click('#open-status'); await p.waitForFunction(() => /bronnen in orde/.test(document.querySelector('#status-sum').textContent));
  const st = await p.evaluate(() => ({ sum: document.querySelector('#status-sum').textContent, rows: document.querySelectorAll('#status-list li').length,
    groups: [...document.querySelectorAll('#status-list h3')].map(x => x.textContent) }));
  ok(st.rows >= 80 && st.groups.includes('Cyberdreigingen') && st.groups.includes('Security-adviezen'), `Bronstatus: ${st.sum} (${st.rows} rows, ${st.groups.length} groups)`);
  await p.check('#status-bad');
  const onlyBad = await p.evaluate(() => [...document.querySelectorAll('#status-list li')].every(li => li.dataset.bad === '1'));
  ok(onlyBad, '"alleen problemen" filter shows only failing sources');
  const axeS = await new AxeBuilder({ page: p }).include('#status-dlg').analyze();
  ok(axeS.violations.length === 0, `axe on Bronstatus: ${axeS.violations.map(v => v.id).join(',') || '0 violations'}`);
  await p.screenshot({ path: `${OUT}/status.png` });
  await p.keyboard.press('Escape');
  ok(log.csp.length === 0, `no CSP violations during normal use (${log.csp.join(' | ') || 'none'})`);
  ok(log.errors.length === 0, `no JavaScript errors (${log.errors.join(' | ') || 'none'})`);
  await ctx.close();
}

// 3. Status view on the evil instance shows a failing source clearly
{
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 } });
  await ctx.addInitScript(ids => localStorage.setItem('ndb:prefs', JSON.stringify({ onboarded: true, sources: ['evil'], known: ids })), EVIL_IDS);
  const p = await ctx.newPage();
  await p.goto(EVIL); await p.waitForSelector('#stream .item', { timeout: 40000 });
  await p.click('#open-status'); await p.waitForFunction(() => /bronnen in orde/.test(document.querySelector('#status-sum').textContent));
  ok(/Evil <b>feed<\/b>/.test(await p.textContent('#status-list')), 'Bronstatus renders source names as text');
  await ctx.close();
}
await b.close(); console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED'); process.exit(fails ? 1 : 0);
