import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
// Inject a rain pattern into the real /api/weather response: dry, light shower, heavy burst, dry.
const mm = [0,0,0,0,0.2,0.4,0.8,1.5,3,6,12,8,4,2,1,0.5,0.2,0,0,0,0,0,0,0];
for (const scheme of ['light', 'dark']) {
  const p = await b.newPage({ viewport: { width: 1440, height: 1000 }, colorScheme: scheme });
  await p.route('**/api/weather*', async route => {
    const r = await route.fetch(); const d = await r.json();
    d.rain.points = d.rain.points.map((pt, i) => ({ ...pt, mmh: mm[i] ?? 0 }));
    d.rain.summary = 'Zware regen tussen ' + d.rain.points[4].time + ' en ' + d.rain.points[17].time + '.';
    await route.fulfill({ response: r, json: d });
  });
  await p.goto((process.env.BASE || 'http://127.0.0.1:8080/')); await p.waitForSelector('#panel-weather .rainchart');
  const t = await p.evaluate(() => {
    const bars = [...document.querySelectorAll('#panel-weather .rainchart rect.bar')];
    return { n: bars.length, heights: bars.map(r => +(+r.getAttribute('height')).toFixed(1)), tip: bars[6]?.querySelector('title')?.textContent,
      label: document.querySelector('#panel-weather .rainchart').getAttribute('aria-label'),
      axis: [...document.querySelectorAll('#panel-weather .rainaxis span')].map(s => s.textContent),
      fill: getComputedStyle(bars[0]).fill };
  });
  if (scheme === 'light') console.log(JSON.stringify(t));
  ok(t.n === 13, `${scheme}: one bar per wet 5-minute step (13)`);
  ok(t.heights[6] === 46 && t.heights[0] < t.heights[3] && t.heights.at(-1) < t.heights[6], `${scheme}: bar heights follow intensity, 10+ mm/u = full height`);
  ok(/^\d\d:\d\d: 12 mm\/u$/.test(t.tip), `${scheme}: bar tooltip "${t.tip}"`);
  ok(t.label.startsWith('Neerslag komende 2 uur: Zware regen tussen'), `${scheme}: chart has an accessible summary`);
  ok(t.axis.length === 5, `${scheme}: 5 time labels ${t.axis.join(' ')}`);
  await p.locator('#panel-weather .rain').screenshot({ path: `${OUT}/rain-${scheme}.png` });
}
// Dry forecast: summary only, no chart.
{
  const p = await b.newPage({ viewport: { width: 1440, height: 1000 } });
  await p.route('**/api/weather*', async route => {
    const r = await route.fetch(); const d = await r.json();
    d.rain.points = d.rain.points.map(pt => ({ ...pt, mmh: 0 }));
    await route.fulfill({ response: r, json: d });
  });
  await p.goto((process.env.BASE || 'http://127.0.0.1:8080/')); await p.waitForSelector('#panel-weather .rain');
  ok(await p.locator('#panel-weather .rainchart').count() === 0, 'dry forecast: no empty chart, summary only');
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED'); process.exit(fails ? 1 : 0);
