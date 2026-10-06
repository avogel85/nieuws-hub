import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || (process.env.BASE || 'http://127.0.0.1:8080/');
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme });
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#panel-weather .wxnow');
  const t = await p.evaluate(() => ({
    banner: document.querySelector('#panel-weather .wxwarn')?.textContent,
    bannerColor: getComputedStyle(document.querySelector('#panel-weather .wxwarn'), '::before').backgroundColor,
    elsewhere: document.querySelector('#panel-weather .wxelse')?.textContent,
    now: document.querySelector('#panel-weather .wxnow')?.textContent,
    rain: document.querySelector('#panel-weather .rain p')?.textContent,
    bars: document.querySelectorAll('#panel-weather .rainchart').length,
    hours: document.querySelectorAll('#panel-weather .hour').length,
    days: document.querySelectorAll('#panel-weather .day7').length,
    mini: document.querySelector('#wx-mini')?.getAttribute('aria-label'),
    sw: document.documentElement.scrollWidth, iw: innerWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t, null, 1));
  ok(/^Code oranje: wind/.test(t.banner || '') && t.banner.includes('Utrecht') && t.banner.includes('tot '), `${w} ${scheme}: orange banner "${t.banner}"`);
  ok(t.bannerColor === (scheme === 'dark' ? 'rgb(255, 138, 76)' : 'rgb(180, 72, 15)'), `${w} ${scheme}: banner bar uses orange severity colour (${t.bannerColor})`);
  ok((t.elsewhere || '').includes('Elders in Nederland') && t.elsewhere.includes('onweer in Friesland, Groningen') && t.elsewhere.includes('mist in Zeeland') && !t.elsewhere.includes('Limburg'), `${w} ${scheme}: other regions line, expired one hidden`);
  ok(/gevoel .* · wind [NOZW]{1,3} \d+ \(.+\) · \d+% vocht/.test(t.now), `${w} ${scheme}: current conditions with Beaufort`);
  ok(t.bars === (t.rain.startsWith('Droog') ? 0 : 1) && /^(Droog tot|Lichte|Matige|Zware)/.test(t.rain), `${w} ${scheme}: rain summary "${t.rain}" (chart only when wet)`);
  ok(t.hours === 12 && t.days === 7, `${w} ${scheme}: 12 two-hourly cells, 7 days`);
  ok(w < 700 || /code oranje voor wind/.test(t.mini), `${w} ${scheme}: header mini weather mentions the warning`);
  ok(t.sw <= t.iw, `${w} ${scheme}: no horizontal scroll`);
  const r = await new AxeBuilder({ page: p }).include('#panel-weather').include('#top').analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe on weather panel + header: ${r.violations.length} violations`);
  for (const v of r.violations) console.log(`   - ${v.id}: ${v.help} → ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`);
  await p.locator('#panel-weather').screenshot({ path: `${OUT}/weather-${w}-${scheme}.png` });
  await ctx.close();
}

// Location change via search, and via browser geolocation.
{
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, geolocation: { latitude: 51.44321, longitude: 3.57555 }, permissions: ['geolocation'] });
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#panel-weather .wxnow');
  await p.click('#panel-weather .pextra .linkbtn');
  ok(await p.evaluate(() => document.activeElement.id === 'wx-search'), '"wijzigen" opens settings with focus on place search');
  await p.keyboard.type('Zwolle');
  await p.waitForSelector('#wx-results button');
  const first = await p.textContent('#wx-results li:first-child button');
  ok(first.startsWith('Zwolle') && first.includes('Overijssel'), `geocode result: "${first}"`);
  await p.click('#wx-results li:first-child button');
  await p.keyboard.press('Escape');
  await p.waitForFunction(() => document.querySelector('#panel-weather .pextra')?.textContent.startsWith('Zwolle') && document.querySelector('#panel-weather .wxnow'));
  const z = await p.evaluate(() => ({ banners: document.querySelectorAll('#panel-weather .wxwarn').length, line: document.querySelector('#panel-weather .wxelse')?.textContent, stored: JSON.parse(localStorage.getItem('ndb:prefs')).weather }));
  ok(z.banners === 0 && z.line.includes('wind in Utrecht'), 'Zwolle (Overijssel): no banner, Utrecht warning listed as elsewhere');
  ok(z.stored.name === 'Zwolle' && z.stored.region === 'Overijssel' && z.stored.cc === 'NL', 'location stored in localStorage');
  // geolocation
  await p.click('#open-settings');
  const [req] = await Promise.all([p.waitForRequest(r => r.url().includes('api/weather?lat=')), p.click('#wx-geo')]);
  ok(req.url().includes('lat=51.44&lon=3.58'), `geolocation sent rounded to 2 decimals: ${req.url().split('?')[1]}`);
  ok((await p.textContent('#wx-msg')).includes('afgerond'), 'geolocation confirmation message');
  await p.click('#wx-default');
  await p.keyboard.press('Escape');
  await p.waitForFunction(() => document.querySelector('#panel-weather .pextra')?.textContent.startsWith('Utrecht') && document.querySelector('#panel-weather .wxnow'));
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).weather === null), 'reset to default location');
  // mini weather click expands collapsed panel
  await p.click('#panel-weather .ptoggle');
  await p.click('#wx-mini');
  ok(await p.getAttribute('#panel-weather .ptoggle', 'aria-expanded') === 'true', 'header mini weather opens a collapsed weather panel');
  await ctx.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
