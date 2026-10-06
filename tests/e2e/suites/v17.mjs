import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
import { ransomware } from './fixtures.mjs';
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const rw = ransomware(); // fixed answer with made-up organisations (the real API rate-limits and changes daily)
async function open(w, scheme, prefs = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block' }); // route() does not see service-worker requests
  await ctx.addInitScript(v => { try { localStorage.setItem('ndb:prefs', v); } catch {} }, JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.route('**/api/ransomware*', r => r.fulfill({ json: rw }));
  await p.goto(URL);
  await p.waitForSelector('#panel-today .school'); await p.waitForSelector('#panel-ransomware .plist'); await p.waitForTimeout(600);
  return [ctx, p, errs];
}
const api = await (await fetch(URL + 'api/today')).json();

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, scheme);
  const t = await p.evaluate(() => ({
    title: document.title, brand: document.querySelector('.brand').textContent,
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    heads: ['today', 'ransomware'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    day: document.querySelector('#panel-today .tday')?.textContent,
    rows: [...document.querySelectorAll('#panel-today .trow')].map(x => x.textContent),
    school: [...document.querySelectorAll('#panel-today .school li')].map(x => ({ t: x.textContent, mine: x.classList.contains('mine') })),
    rwLines: [...document.querySelectorAll('#panel-ransomware .eline')].map(x => x.textContent),
    rwItems: [...document.querySelectorAll('#panel-ransomware .plist li')].map(li => ({ name: li.querySelector('b')?.textContent, links: [...li.querySelectorAll('a')].map(a => a.href), text: li.textContent })),
    rwHtml: document.querySelector('#panel-ransomware').innerHTML,
    rwNote: document.querySelector('#panel-ransomware .pnote')?.textContent,
    sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify({ day: t.day, rows: t.rows, school: t.school, rwLines: t.rwLines, rwItems: t.rwItems.slice(0, 2) }, null, 1));
  ok(t.title === 'Nieuws Hub' && t.brand === 'Nieuws Hub', `${w} ${scheme}: name "Nieuws Hub"`);
  ok(t.order.indexOf('satellite') === t.order.indexOf('weather') + 1 && t.order.indexOf('today') === t.order.indexOf('satellite') + 1 && t.order.indexOf('ransomware') === t.order.indexOf('breaches') + 1 && t.order.indexOf('utilities') === t.order.indexOf('ransomware') + 1, 'Vandaag after Weer, Ransomware NL after Datalekken');
  ok(t.heads.join() === 'Vandaag,Ransomware', 'panel names');
  ok(new RegExp(`week ${api.week}$`).test(t.day || ''), `date and week: ${t.day}`);
  ok(t.rows.some(r => /Zon op \d\d:\d\d, onder \d\d:\d\d/.test(r)) && t.rows.some(r => /maan|kwartier/i.test(r)) && t.rows.some(r => /Volgende feestdag: /.test(r)), 'sun, moon and next public holiday');
  ok(!api.clock_change || t.rows.some(r => /Klok: .* een uur (terug|vooruit)/.test(r)), 'clock change shown when within 60 days');
  ok(t.school.length === 3 && t.school.filter(s => s.mine).length === 1 && t.school.find(s => s.mine).t.startsWith('Midden'), 'school holidays for 3 regions, Utrecht (weather location) = Midden highlighted');
  ok(new RegExp(`^${rw.last30} claims in de afgelopen 30 dagen · ${rw.last365} in 12 maanden$`).test(t.rwLines[0] || ''), `ransomware counts: ${t.rwLines[0]}`);
  ok(t.rwItems.length === Math.min(6, rw.victims.length) && t.rwItems[0].name === rw.victims[0].name, `ransomware: newest ${t.rwItems.length} claims`);
  ok(t.rwItems.every(i => i.links.length === 1 && i.links[0].startsWith('https://www.ransomware.live/group/')), 'only links to ransomware.live group pages');
  ok(!/\.onion/.test(t.rwHtml) && !rw.victims.some(v => 'description' in v), 'no leak-site links and no descriptions passed on');
  ok(/niet geverifieerd/.test(t.rwNote || ''), 'claims are marked as unverified');
  ok(t.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors ${errs.join('|')}`);
  const r = await new AxeBuilder({ page: p }).include('#panel-today').include('#panel-ransomware').analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe on the new panels: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await ctx.close();
}

// Overview cards
{
  const [ctx, p] = await open(1440, 'light');
  await p.keyboard.press('v'); await p.waitForSelector('#dc-today'); await p.waitForTimeout(400);
  const d = await p.evaluate(() => ({ today: document.querySelector('#dc-today').parentElement.textContent, sec: document.querySelector('#dc-advisories, #dc-breaches')?.parentElement.textContent }));
  ok(/week \d+/.test(d.today) && /Volgende feestdag/.test(d.today), `overview card Vandaag: ${d.today.slice(0, 120)}`);
  ok(/Ransomware: \d+ claims in 7 dagen/.test(d.sec || ''), 'overview Veiligheid mentions ransomware');
  await ctx.close();
}

// English
{
  const [ctx, p] = await open(1440, 'light', { lang: 'en' });
  const e = await p.evaluate(() => ({ heads: ['today', 'ransomware'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    today: document.querySelector('#panel-today .pbody').innerText, rw: document.querySelector('#panel-ransomware .pbody').innerText }));
  ok(e.heads.join() === 'Today,Ransomware', `English names: ${e.heads}`);
  ok(/Sunrise \d\d:\d\d/.test(e.today) && /School holidays/.test(e.today) && /Next public holiday: /.test(e.today) && /Middle/.test(e.today), 'English Vandaag');
  ok(/claims in the last 30 days/.test(e.rw) && /not verified/.test(e.rw), 'English Ransomware NL');
  await ctx.close();
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
