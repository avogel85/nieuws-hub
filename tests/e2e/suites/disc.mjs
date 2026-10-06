import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
for (const [w, lang] of [[1440, 'nl'], [360, 'nl'], [1440, 'en']]) {
  const ctx = await b.newContext({ viewport: { width: w, height: 900 } });
  await ctx.addInitScript(l => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l })), lang);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto('http://127.0.0.1:8090/'); await p.waitForSelector('#stream .item');
  await p.click('#open-disclaimer'); await p.waitForSelector('#disclaimer-dlg[open]');
  const d = await p.evaluate(() => ({ text: document.querySelector('#disclaimer-dlg').innerText, links: [...document.querySelectorAll('#disclaimer-dlg a')].map(a => a.href),
    sw: document.documentElement.scrollWidth, other: !!document.querySelector(document.documentElement.lang === 'en' ? '#disc-nl' : '#disc-en:not([hidden])') }));
  const nl = /best effort-basis/.test(d.text) && /Gebruik is volledig op eigen risico/.test(d.text) && /artikel 15 en 16/.test(d.text);
  const en = /best effort basis/.test(d.text) && /Use at your own risk/.test(d.text) && /sections 15 and 16/.test(d.text);
  ok(lang === 'nl' ? nl && !en : en && !nl, `${w} ${lang}: disclaimer text in ${lang} only`);
  ok(d.links.some(l => l.endsWith('/LICENSE')) && d.links.some(l => l === 'https://www.gnu.org/licenses/gpl-3.0.html') && !d.other, 'LICENSE and GPL links');
  ok(d.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors`);
  const r = await new AxeBuilder({ page: p }).include('#disclaimer-dlg').analyze();
  ok(r.violations.length === 0, `axe: ${r.violations.map(v => v.id).join(',') || 0}`);
  await p.keyboard.press('Escape');
  ok(await p.evaluate(() => !document.querySelector('#disclaimer-dlg').open), 'Escape closes it');
  if (w === 1440 && lang === 'nl') { await p.click('#open-disclaimer'); await p.locator('#disclaimer-dlg').screenshot({ path: `${OUT}/disclaimer.png` }); }
  await ctx.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
