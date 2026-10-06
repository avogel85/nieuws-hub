import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
const URL = process.env.BASE;
const b = await chromium.launch();
showBothViews(b);
const ALL_IDS = (await (await fetch(URL + 'api/catalog')).json()).sources.map(s => s.id);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
// Weather: a slow answer for the previous location must not overwrite the newer default.
{
  const ctx = await b.newContext({ serviceWorkers: 'block', geolocation: { latitude: 51.44, longitude: 3.58 }, permissions: ['geolocation'] });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ onboarded: true })));
  const p = await ctx.newPage();
  await p.route('**/api/weather?lat=51.44*', async route => { await new Promise(r => setTimeout(r, 3000)); await route.continue(); });
  await p.goto(URL); await p.waitForSelector('#panel-weather .wxnow');
  await p.click('#open-settings'); await p.click('#wx-geo'); await p.waitForTimeout(300);
  await p.click('#wx-default'); await p.keyboard.press('Escape');
  await p.waitForTimeout(4500); // the delayed answer has arrived by now
  const extra = await p.textContent('#panel-weather .pextra');
  ok(extra.startsWith('Utrecht'), `weather: newest location wins over a late answer ("${extra}")`);
  await ctx.close();
}
// News: a slow answer for the old source list must not replace the newer one.
{
  const ctx = await b.newContext({ serviceWorkers: 'block' });
  await ctx.addInitScript(ids => localStorage.setItem('ndb:prefs', JSON.stringify({ onboarded: true, sources: ['nos-algemeen'], known: ids })), ALL_IDS);
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#stream .item');
  let first = true;
  await p.route(u => u.href.includes('/api/news?') && u.href.includes('tweakers'), async route => { if (first) { first = false; await new Promise(r => setTimeout(r, 3000)); } await route.continue(); });
  await p.click('#open-settings'); await p.check('#src-groups input[data-id="tweakers"]'); await p.keyboard.press('Escape'); // request A (slow)
  await p.waitForTimeout(300);
  await p.click('#open-settings'); await p.uncheck('#src-groups input[data-id="tweakers"]'); await p.check('#src-groups input[data-id="security-nl"]'); await p.keyboard.press('Escape'); // request B
  await p.waitForTimeout(4500);
  const srcs = new Set(await p.$$eval('#stream .item .src', s => s.map(x => x.textContent)));
  ok(!srcs.has('Tweakers') && srcs.has('Security.NL'), `news: newest source list wins (${[...srcs].join(', ')})`);
  await ctx.close();
}
await b.close(); console.log(fails ? `${fails} FAILED` : 'ALL PASSED'); process.exit(fails ? 1 : 0);
