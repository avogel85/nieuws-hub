// 1.26: high water and storm surge (Aardbevingen en natuurrampen, top bar, push topic), heat and smog (Luchtkwaliteit)
// and space weather (Vanavond aan de hemel).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
const iso = ms => new Date(Date.now() + ms).toISOString();
const day = n => new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Amsterdam', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(Date.now() + n * 864e5));
const utcDay = n => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);
const sectors = (up = []) => [...up, ...Array.from({ length: 19 }, (_, i) => ({ id: 's' + i, name: 'Sector ' + i, code: 1 }))];
const barriers = (closed = false) => [
  { name: 'Oosterscheldekering', open: true, status: 'Geopend' }, { name: 'Haringvlietdam', open: true, status: 'Geopend' },
  { name: 'Europoortkering (Maeslant- en Hartelkering)', open: !closed, status: closed ? 'Gesloten' : 'Geopend' }, { name: 'Hollandsche IJsselkering', open: true, status: 'Geopend' }];
const WATER_HIGH = { fetched_at: iso(0), url: 'https://waterberichtgeving.rws.nl/owb/', status: { level: 3, peak: 4, peak_at: iso(6 * 36e5),
  sectors: sectors([{ id: 'ijssel', name: 'IJssel', code: 3 }, { id: 'bedijktemaas', name: 'Bedijkte Maas', code: 2 }]), barriers: barriers(true),
  outlook: 'Door de noordwesterstorm worden hoge waterstanden verwacht langs de kust.' } };
const WATER_CALM = { fetched_at: iso(0), url: 'https://waterberichtgeving.rws.nl/owb/', status: { level: 2, peak: 2, sectors: sectors([{ id: 'bedijktemaas', name: 'Bedijkte Maas', code: 2 }]), barriers: barriers(false),
  outlook: 'Er worden voor de komende dagen geen afwijkingen van betekenis verwacht.' } };
const HEAT = { level: 'yellow', active: true };
const SPACE = { fetched_at: iso(0), days: [
  { date: utcDay(0), now: true, r: 0, s: 0, g: 2 },
  { date: utcDay(1), r: -1, s: -1, g: 1, r_prob: 30, r_major: 5, s_prob: 1 },
  { date: utcDay(2), r: -1, s: -1, g: 0, r_prob: 10, s_prob: 1 },
  { date: utcDay(3), r: -1, s: -1, g: 0, r_prob: 10, s_prob: 1 }], flare: { class: 'M2.1', at: iso(-3 * 36e5) } };
const AIR = { enabled: true, station: { name: 'Den Haag-Rebecquestraat', url: 'https://www.luchtmeetnet.nl/x', distance_km: 2.1 }, lki: { value: 3, at: iso(-36e5) },
  components: [{ formula: 'O3', value: 92 }], source: { url: 'https://www.luchtmeetnet.nl/' } };
const pollen = o3 => ({ enabled: true, fetched_at: iso(0), data: { now: { grass: 1 }, days: [0, 1, 2].map(i => ({ date: day(i), max: { grass: 1 }, o3: o3[i] })) } });
const RIVM = { items: [{ id: 'r1', source: 'rivm', title: 'Smogwaarschuwing voor zaterdag in het zuiden', url: 'https://www.rivm.nl/nieuws/smog', published: iso(-2 * 36e5), summary: 'Kans op matige smog door ozon.' }], status: { rivm: {} } };

async function open(w, { scheme = 'light', lang = 'nl', mobile = false, water = WATER_HIGH, heat = HEAT, space = SPACE, o3 = [120, 195, 90], rivm = RIVM } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', isMobile: mobile, hasTouch: mobile });
  await ctx.addInitScript(l => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l })), lang);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.route('**/api/alerts', async r => { const res = await r.fetch(); const j = await res.json();
    if (water !== undefined) j.water = water;
    j.knmi = { ...(j.knmi || {}), status: { level: heat ? heat.level : 'none', active: true, count: heat ? 1 : 0, types: heat ? ['Hitte'] : [], areas: heat ? ['Limburg'] : [], heat: heat || undefined } };
    return r.fulfill({ response: res, json: j }); });
  await p.route('**/api/sky*', async r => { const res = await r.fetch(); const j = await res.json(); if (space !== undefined) j.space = space; return r.fulfill({ response: res, json: j }); });
  await p.route('**/api/air*', r => r.fulfill({ json: AIR }));
  if (o3) await p.route('**/api/pollen*', r => r.fulfill({ json: pollen(o3) }));
  if (rivm) await p.route('**/api/news?sources=rivm*', r => r.fulfill({ json: rivm }));
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(1200);
  if (mobile) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  return [ctx, p, errs];
}
const sectionText = (p, re) => p.evaluate(src => [...document.querySelectorAll('#panel-quakes .wsec')].find(x => new RegExp(src).test(x.querySelector('h3')?.textContent))?.textContent || '', re);

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, { scheme, mobile: w === 360 });
  // top bar
  const badge = await p.evaluate(() => { const a = document.querySelector('#ab-water'); return { hidden: a.hidden, text: a.innerText, title: a.title, href: a.getAttribute('href'), dot: a.querySelector('.d')?.className }; });
  ok(!badge.hidden && (w === 360 ? /^Water: code oranje · kering dicht$/.test(badge.text) : /^Hoogwater: code oranje \(IJssel\) · kering dicht$/.test(badge.text)) && badge.href === '#panel-quakes' && /lvl-orange/.test(badge.dot),
    `${w} ${scheme}: top-bar badge "${badge.text}"`);
  ok(/Rijkswaterstaat: code oranje voor IJssel\. Gesloten: Europoortkering/.test(badge.title), `badge title: ${badge.title}`);
  // Nederland tab
  await p.waitForSelector('#panel-quakes .hzsum');
  const sum = await p.textContent('#panel-quakes .hzsum');
  ok(/hoogwater code oranje, stormvloedkering dicht/.test(sum), `summary: ${sum}`);
  const wt = await sectionText(p, 'Hoogwater');
  ok(/code oranje IJssel/.test(wt) && /code geel Bedijkte Maas/.test(wt) && !/Sector 0/.test(wt), `sectors above green: ${wt.slice(0, 80)}`);
  ok(/Verwacht: code rood vanaf/.test(wt) && /Stormvloedkering dicht Europoortkering \(Maeslant- en Hartelkering\) \(Gesloten\)/.test(wt), `forecast and barrier: ${wt.slice(60, 220)}`);
  ok(/noordwesterstorm/.test(wt) && /Waterberichtgeving \(Rijkswaterstaat\)/.test(wt), 'outlook and source link');
  if (w === 1440 && scheme === 'light') { await p.locator('#panel-quakes').screenshot({ path: `${OUT}/water.png` }); await p.locator('#alertbar').screenshot({ path: `${OUT}/water-badge.png` }); }
  await axe(p, '#panel-quakes', `${w} ${scheme} water`);
  await axe(p, '#alertbar', `${w} ${scheme} top bar`);
  // heat and smog
  await p.waitForSelector('#panel-air .hsmog');
  const hs = await p.textContent('#panel-air .hsmog');
  ok(/Nationaal Hitteplan actief KNMI code geel: aanhoudende hitte/.test(hs), `heat plan: ${hs.slice(0, 90)}`);
  ok(/Smog · Smogwaarschuwing voor zaterdag in het zuiden/.test(hs) && /Ozon \(verwachting, hoogste uur\): vandaag 120 · morgen 195 · \S+ \d+ \S+ 90 µg\/m³/.test(hs), `smog and ozone: ${hs.slice(60)}`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-air').screenshot({ path: `${OUT}/heat-smog.png` });
  await axe(p, '#panel-air', `${w} ${scheme} air`);
  // space weather
  await p.waitForSelector('#panel-sky .spacew h3', { timeout: 20000 });
  const sw = await p.textContent('#panel-sky .spacew');
  ok(/Nu: G2 geomagnetische storm/.test(sw) && /morgen: G1 geomagnetische storm, 30% kans op radio-storing/.test(sw) && !/10% kans/.test(sw), `space weather: ${sw.slice(0, 140)}`);
  ok(/Sterkste zonnevlam \(24 uur\): M2\.1 om \d\d:\d\d/.test(sw) && /schaal 1–5/.test(sw), 'flare and legend');
  if (w === 1440 && scheme === 'light') await p.locator('#panel-sky').screenshot({ path: `${OUT}/space-weather.png` });
  await axe(p, '#panel-sky', `${w} ${scheme} sky`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, `${w} ${scheme}: no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
{ // calm: no badge at code geel, heat plan off, smog only from the ozone forecast, quiet space weather
  const [ctx, p, errs] = await open(1440, { water: WATER_CALM, heat: null, rivm: { items: [], status: { rivm: {} } }, o3: [80, 70, 60],
    space: { ...SPACE, days: SPACE.days.map(d => ({ ...d, g: d.now ? 0 : 0, r_prob: 5 })), flare: { class: 'C1.4', at: iso(-36e5) } } });
  ok(await p.evaluate(() => document.querySelector('#ab-water').hidden), 'code geel: no top-bar badge');
  const wt = await sectionText(p, 'Hoogwater');
  ok(/code geel Bedijkte Maas/.test(wt) && /Stormvloedkeringen: alle 4 open\./.test(wt) && !/Verwacht:/.test(wt), `calm water: ${wt.slice(0, 160)}`);
  ok(/hoogwater code geel/.test(await p.textContent('#panel-quakes .hzsum')), 'summary mentions code geel');
  const hs = await p.textContent('#panel-air .hsmog');
  ok(/Nationaal Hitteplan: niet actief\./.test(hs) && /Geen smog verwacht\./.test(hs), `calm heat and smog: ${hs}`);
  const sw = await p.textContent('#panel-sky');
  ok(/Nu: rustig \(geen storm of storing\)\./.test(sw) && /Komende dagen: geen storm verwacht\./.test(sw) && /C1\.4/.test(sw), 'quiet space weather');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{ // smog from the ozone forecast alone; only a closed barrier gives its own badge text
  const [ctx, p] = await open(1440, { water: { ...WATER_CALM, status: { ...WATER_CALM.status, level: 1, sectors: sectors(), barriers: barriers(true) } }, heat: null, rivm: { items: [], status: { rivm: {} } }, o3: [90, 210, 100] });
  ok(/Kans op smog ozon tot 210 µg\/m³ morgen/.test(await p.textContent('#panel-air .hsmog')), 'ozone above 180: chance of smog');
  ok(await p.evaluate(() => { const a = document.querySelector('#ab-water'); return !a.hidden && a.innerText === 'Stormvloedkering dicht'; }), 'closed barrier only: "Stormvloedkering dicht" in the top bar');
  ok(/Overal code groen: geen hoogwater\./.test(await sectionText(p, 'Hoogwater')), 'all green: one calm line');
  await ctx.close();
}
{ // error states
  const [ctx, p] = await open(1440, { water: { error: 'HTTP 503', error_since: iso(-6e5) }, space: { error: 'HTTP 500' } });
  ok(/Rijkswaterstaat is niet bereikbaar \(HTTP 503\)/.test(await sectionText(p, 'Hoogwater')) && await p.evaluate(() => document.querySelector('#ab-water').hidden), 'water error: a note, no badge');
  ok(/NOAA SWPC is niet bereikbaar \(HTTP 500\)/.test(await p.textContent('#panel-sky')), 'space weather error');
  await ctx.close();
}
{ // English
  const [ctx, p] = await open(1440, { lang: 'en' });
  const badge = await p.evaluate(() => document.querySelector('#ab-water').innerText);
  ok(/^High water: code orange \(IJssel\) · barrier closed$/.test(badge), `English badge: ${badge}`);
  ok(/High water and storm surge/.test(await p.textContent('#panel-quakes')) && /Storm-surge barrier closed/.test(await p.textContent('#panel-quakes')), 'English water section');
  ok(/Heat and smog/.test(await p.textContent('#panel-air')) && /National Heat Plan active/.test(await p.textContent('#panel-air')), 'English heat and smog');
  const sw = await p.textContent('#panel-sky');
  ok(/Space weather/.test(sw) && /Now: G2 geomagnetic storm/.test(sw) && /30% chance of a radio blackout/.test(sw), 'English space weather');
  await ctx.close();
}
{ // live data from this server: the sources answer and nothing says "unreachable"
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(BASEURL); await p.waitForSelector('#panel-quakes .hzsum', { timeout: 20000 });
  const deadline = Date.now() + 30000; let wt = '', sw = '';
  while (Date.now() < deadline) { // the first fetch after start can take a moment
    wt = await sectionText(p, 'Hoogwater'); sw = await p.textContent('#panel-sky');
    if (/Stormvloedkering|code/.test(wt) && /Ruimteweer/.test(sw) && !/voor het eerst/.test(wt + sw)) break;
    await p.waitForTimeout(3000); await p.reload(); await p.waitForSelector('#panel-quakes .hzsum'); await p.waitForTimeout(1500);
  }
  ok(/code (groen|geel|oranje|rood)|Overal code groen/.test(wt) && /Stormvloedkering/.test(wt) && !/niet bereikbaar/.test(wt), `live water: ${wt.replace(/\s+/g, ' ').slice(0, 160)}`);
  ok(/Ruimteweer/.test(sw) && /Nu: /.test(sw) && !/NOAA SWPC is niet bereikbaar/.test(sw), `live space weather: ${sw.slice(sw.indexOf('Ruimteweer'), sw.indexOf('Ruimteweer') + 140)}`);
  ok(/Hitte en smog/.test(await p.textContent('#panel-air')), 'live heat and smog section');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
