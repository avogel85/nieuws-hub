import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block' });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(process.env.BASE); await p.waitForSelector('#stream .item');
  if (w === 360) { await p.click('#mv-panels2'); await p.waitForTimeout(300); } // Internetstoringen is on the Achtergrond page
  await p.locator('#panel-outages').scrollIntoViewIfNeeded(); await p.waitForTimeout(1500);
  const txt = await p.textContent('#panel-outages');
  if (w === 1440 && scheme === 'light') await p.locator('#panel-outages').screenshot({ path: `${OUT}/outages.png` });
  const names = ['Akamai', 'AWS', 'Cloudflare', 'Microsoft Azure', 'Microsoft 365', 'Google Cloud', 'STACKIT'];
  const idx = names.map(n => txt.indexOf(n));
  ok(idx.every(i => i >= 0) && idx.every((v, i) => !i || v > idx[i - 1]), `${w} ${scheme}: all 7 providers in config order (${idx})`);
  const r = await new AxeBuilder({ page: p }).include('#panel-outages').analyze();
  ok(r.violations.length === 0, `${w} ${scheme} axe: ${r.violations.map(v => v.id).join(', ') || 0}`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, 'no errors, no horizontal scroll');
  await ctx.close();
}
await b.close(); console.log(fails ? `${fails} FAILED` : 'ALL PASSED'); process.exit(fails ? 1 : 0);
