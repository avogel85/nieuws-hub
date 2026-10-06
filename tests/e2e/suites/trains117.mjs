import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
import { trains } from './fixtures.mjs';
const TRAINS = trains(); // a fixed NS answer, so the test needs no NS API key
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block' });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.route('**/api/trains*', r => r.fulfill({ json: TRAINS }));
  await p.goto(process.env.BASE); await p.waitForSelector('#stream .item');
  if (w === 360) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  await p.waitForSelector('#panel-trains .tgrp h3', { timeout: 20000 });
  await p.locator('#panel-trains').scrollIntoViewIfNeeded(); await p.waitForTimeout(300);
  const s = await p.evaluate(() => {
    const body = document.querySelector('#p-trains-body'), groups = [...body.querySelectorAll('.tgrp')];
    const br = body.getBoundingClientRect();
    return { n: groups.length, display: groups.map(g => getComputedStyle(g).display), heads: groups.map(g => g.querySelector('h3').textContent),
      full: groups.every(g => Math.abs(g.getBoundingClientRect().width - br.width) < 2),
      stacked: groups.every((g, i) => !i || g.getBoundingClientRect().top >= groups[i - 1].getBoundingClientRect().bottom - 1) };
  });
  if (w === 1440 && scheme === 'light') await p.locator('#panel-trains').screenshot({ path: `${OUT}/trains.png` });
  if (w === 360) await p.locator('#panel-trains').screenshot({ path: `${OUT}/trains-360.png` });
  ok(s.display.every(d => d === 'block') && s.full && s.stacked, `${w} ${scheme}: sections below each other, full width (${s.heads.join(' / ')}; ${s.display})`);
  // the trending chips keep their joined look
  const chip = await p.evaluate(() => { const g = document.querySelector('#trending .tchipgrp'); return g ? getComputedStyle(g).display : 'none'; });
  ok(chip === 'inline-flex' || chip === 'flex' || chip === 'none', `trending chip group still inline-flex (${chip})`);
  const r = await new AxeBuilder({ page: p }).include('#panel-trains').analyze();
  ok(r.violations.length === 0 && errs.length === 0, `${w} ${scheme} axe/errors: ${r.violations.map(v => v.id).join(', ') || 0} ${errs.join('|')}`);
  await ctx.close();
}
await b.close(); console.log(fails ? `${fails} FAILED` : 'ALL PASSED'); process.exit(fails ? 1 : 0);
