// 1.23/1.24: rocket launches in the sky panel, and the panel Aardbevingen en natuurrampen (Nederland: KNMI,
// storm warning, natuurbrandrisico; Wereld: USGS, NASA EONET).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
const iso = ms => new Date(Date.now() + ms).toISOString();
const LAUNCHES = [
  { rocket: 'Falcon 9', mission: 'Transporter 18', provider: 'SpaceX', place: 'Vandenberg SFB, CA, USA', time: iso(-20 * 6e4), exact: true, status: 'flight' },
  { rocket: 'Falcon Heavy', mission: 'NROL-97', provider: 'SpaceX', place: 'Kennedy Space Center, FL, USA', time: iso(30 * 6e4), exact: true, status: 'go' },
  { rocket: 'Ariane 6', mission: 'Galileo L14', provider: 'Arianespace', place: 'Guiana Space Centre', time: iso(20 * 36e5), exact: false, status: 'tbd' },
  { rocket: 'Nuri', mission: 'NeonSat-2', provider: 'KARI', place: 'Naro Space Center, South Korea', time: iso(22 * 36e5), exact: true, status: 'go' },
];
const WORLD = { enabled: true, min_mag: 6, hours: 24,
  quakes: { fetched_at: iso(0), items: [
    { mag: 7.4, place: '80 km ENE of Tadine, New Caledonia', time: iso(-3 * 36e5), depth_km: 10, tsunami: true, alert: 'orange', url: 'https://earthquake.usgs.gov/x' },
    { mag: 6.1, place: 'southern Mid-Atlantic Ridge', time: iso(-20 * 36e5), depth_km: 15, url: 'https://earthquake.usgs.gov/y' }] },
  events: { fetched_at: iso(0), items: [
    { kind: 'storm', title: 'Hurricane Rachel', time: iso(-6 * 36e5), wind_kmh: 167, url: 'https://www.nhc.noaa.gov/' },
    { kind: 'storm', title: 'Super Typhoon Choi-wan', time: iso(-6 * 36e5), wind_kmh: 250 },
    { kind: 'volcano', title: 'Etna Volcano, Italy', time: iso(-864e5) },
    { kind: 'wildfire', title: 'Wildfire Hatch Grade, Walla Walla, Washington', time: iso(-36e5), area_ha: 4047 }] },
  fire_risk: { fetched_at: iso(0), url: 'https://www.brandweer.nl/natuurbrandrisico/', regions: [
    ...Array.from({ length: 22 }, (_, i) => ({ region: 'Regio ' + i, phase: 1 })), { region: 'Noord-Holland-Noord', phase: 2 }, { region: 'Kennemerland', phase: 2 }, { region: 'Zaanstreek-Waterland', phase: 0 }] } };
const WIND = { level: 'yellow', active: true, count: 2, types: ['Wind'], areas: ['Zeeland', 'Noord-Holland'] };
const CALM_WATER = { fetched_at: iso(0), url: 'https://waterberichtgeving.rws.nl/owb/', status: { level: 1, peak: 1, sectors: [{ id: 'ijssel', name: 'IJssel', code: 1 }], barriers: [{ name: 'Oosterscheldekering', open: true, status: 'Geopend' }] } };
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, fixtures = true } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', isMobile: mobile, hasTouch: mobile });
  await ctx.addInitScript(l => { // once per tab, so a reload keeps what the page saved
    if (sessionStorage.getItem('t-init')) return;
    sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l }));
  }, lang);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (fixtures) {
    await p.route('**/api/world*', r => r.fulfill({ json: WORLD }));
    await p.route('**/api/alerts', async r => { const res = await r.fetch(); const j = await res.json(); j.knmi = { ...(j.knmi || {}), status: WIND }; j.water = CALM_WATER; return r.fulfill({ response: res, json: j }); });
    await p.route('**/api/sky*', async r => { const res = await r.fetch(); const j = await res.json(); j.launches = { fetched_at: iso(0), hours: 24, items: LAUNCHES }; return r.fulfill({ response: res, json: j }); });
  }
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(1000);
  if (mobile) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  return [ctx, p, errs];
}

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, { scheme, mobile: w === 360 });
  await p.waitForSelector('#panel-sky .launches li', { timeout: 20000 });
  const sky = await p.evaluate(() => ({ h: document.querySelector('#panel-sky .launches h3').textContent,
    items: [...document.querySelectorAll('#panel-sky .launches li')].map(li => li.textContent), foot: document.querySelector('#panel-sky .pfoot').textContent }));
  ok(/Raketlanceringen/.test(sky.h) && sky.items.length === 3, `${w} ${scheme}: sky panel shows the next 3 launches`);
  ok(/^nu in de lucht Falcon 9 · Transporter 18/.test(sky.items[0]) && /^over 3\d min Falcon Heavy · NROL-97/.test(sky.items[1]) && /Ariane 6.*datum nog niet zeker/.test(sky.items[2]),
    `labels: ${sky.items.map(x => x.slice(0, 40)).join(' | ')}`);
  ok(/SpaceX · Kennedy Space Center/.test(sky.items[1]) && /lanceringen: Launch Library 2/.test(sky.foot), 'organisation, launch site and source');
  // Aardbevingen en natuurrampen: Nederland first, the Wereld tab on request
  await p.waitForSelector('#panel-quakes .hzsum');
  const nl = await p.evaluate(() => {
    const pn = document.querySelector('#panel-quakes');
    return { h2: pn.querySelector('h2').textContent, sum: pn.querySelector('.hzsum').textContent, tabs: [...pn.querySelectorAll('.advf .chip')].map(b => b.textContent + ':' + b.getAttribute('aria-pressed')),
      secs: [...pn.querySelectorAll('.wsec h3')].map(x => x.textContent.trim()), fire: [...pn.querySelectorAll('.wsec')].find(x => /Natuurbrandrisico/.test(x.textContent))?.textContent, storm: [...pn.querySelectorAll('.wsec')].find(x => /Storm/.test(x.textContent))?.textContent,
      order: [...document.querySelectorAll('#side .panel')].map(x => x.id.replace('panel-', '')) };
  });
  ok(nl.h2 === 'Aardbevingen en natuurrampen' && !nl.order.includes('world'), `one panel "${nl.h2}", no separate Wereldwijd`);
  ok(nl.tabs.join() === 'Nederland:true,Wereld:false', `tabs ${nl.tabs}`);
  ok(/^Nederland: .*windwaarschuwing, natuurbrandrisico fase 2 in 2 regio’s · Wereld: 2 zware bevingen, 2 stormen, 2 andere rampen$/.test(nl.sum), `summary: ${nl.sum}`);
  ok(nl.secs.join('|') === '🌍 Aardbevingen (14 dagen)|🌀 Storm|🔥 Natuurbrandrisico|🌊 Hoogwater en stormvloed', `NL sections: ${nl.secs.join(' | ')}`);
  ok(/code geel/.test(nl.storm) && /KNMI: wind in Zeeland, Noord-Holland/.test(nl.storm), `storm: ${nl.storm}`);
  ok(/fase 2 Kennemerland/.test(nl.fire) && /fase 2 Noord-Holland-Noord/.test(nl.fire) && /1 regio’s onbekend/.test(nl.fire) && /Kaart en uitleg \(brandweer\)/.test(nl.fire), `fire risk: ${nl.fire}`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-quakes').screenshot({ path: `${OUT}/hazards-nl.png` });
  await axe(p, '#panel-quakes', `${w} ${scheme} Nederland tab`);
  await p.click('#panel-quakes .advf .chip:has-text("Wereld")'); await p.waitForSelector('#panel-quakes .wsec li');
  const wd = await p.evaluate(() => {
    const pn = document.querySelector('#panel-quakes');
    return { secs: [...pn.querySelectorAll('.wsec h3')].map(x => x.textContent.trim()), items: [...pn.querySelectorAll('.wsec li')].map(li => li.textContent),
      links: [...pn.querySelectorAll('.wsec li a')].map(a => a.href), foot: pn.querySelector('.pfoot').textContent, sw: document.documentElement.scrollWidth,
      pressed: document.activeElement?.textContent };
  });
  if (w === 1440 && scheme === 'light') { console.log(JSON.stringify(wd.items)); await p.locator('#panel-quakes').screenshot({ path: `${OUT}/hazards-world.png` }); await p.locator('#panel-sky').screenshot({ path: `${OUT}/sky-launches.png` }); }
  ok(wd.secs.join('|') === '🌍 Aardbevingen vanaf M6 (24 uur)|🌀 Stormen|🌋 Vulkanen|🔥 Grote natuurbranden' && wd.pressed === 'Wereld', `Wereld tab sections: ${wd.secs.join(' | ')}`);
  ok(/^M7,4 80 km ONO van Tadine, New Caledonia/.test(wd.items[0]) && /tsunamiwaarschuwing/.test(wd.items[0]) && /gevolgen: groot/.test(wd.items[0]), `quake in Dutch with tsunami warning: ${wd.items[0]}`);
  ok(/^Orkaan Rachel167 km\/u/.test(wd.items[2]) && /^Supertyfoon Choi-wan250 km\/u/.test(wd.items[3]) && /^Etna, Italy/.test(wd.items[4]) && /^Hatch Grade, Walla Walla, Washington4\.047 ha/.test(wd.items[5]),
    `storms, volcano and fire: ${wd.items.slice(2).map(x => x.slice(0, 32)).join(' | ')}`);
  ok(wd.links.includes('https://earthquake.usgs.gov/x') && wd.links.includes('https://www.nhc.noaa.gov/') && /USGS · NASA EONET/.test(wd.foot), 'source links and footer');
  await axe(p, '#panel-quakes', `${w} ${scheme} Wereld tab`);
  await axe(p, '#panel-sky', `${w} ${scheme} sky`);
  ok(errs.length === 0 && wd.sw <= w, `${w} ${scheme}: no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
{ // the tab choice is remembered
  const [ctx, p] = await open(1440);
  await p.click('#panel-quakes .advf .chip:has-text("Wereld")'); await p.waitForTimeout(200);
  await p.reload(); await p.waitForSelector('#panel-quakes .advf');
  ok(await p.getAttribute('#panel-quakes .advf .chip:has-text("Wereld")', 'aria-pressed') === 'true' && !!(await p.$('#panel-quakes .wsec li')), 'Wereld stays chosen after a reload');
  await ctx.close();
}
{ // English keeps the original names
  const [ctx, p] = await open(1440, { lang: 'en' });
  await p.click('#panel-quakes .advf .chip:has-text("World")'); await p.waitForSelector('#panel-quakes .wsec li');
  const t = await p.textContent('#panel-quakes');
  ok(/Earthquakes and natural disasters/.test(await p.textContent('#panel-quakes h2')) && /Hurricane Rachel167 km\/h/.test(t) && /80 km ENE of Tadine/.test(t) && /Earthquakes from M6 \(24 hours\)/.test(t), 'English panel');
  ok(/Rocket launches/.test(await p.textContent('#panel-sky')) && /in flight now/.test(await p.textContent('#panel-sky')), 'English launches');
  await ctx.close();
}
{ // no launches in the coming 24 hours: a short line instead of the list
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })));
  const p = await ctx.newPage();
  await p.route('**/api/sky*', async r => { const res = await r.fetch(); const j = await res.json(); j.launches = { fetched_at: iso(0), hours: 24, items: [] }; return r.fulfill({ response: res, json: j }); });
  await p.goto(BASEURL); await p.waitForSelector('#panel-sky .launches');
  const t = await p.textContent('#panel-sky .launches');
  ok(/Geen raketlanceringen in de komende 24 uur\./.test(t) && !(await p.$('#panel-sky .launches li')), `no launches: "${t.replace(/\s+/g, ' ').trim()}"`);
  await ctx.close();
}
{ // empty and error states in the Wereld tab, all phase 1 in the Nederland tab
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, hazardTab: 'world' })));
  const p = await ctx.newPage();
  await p.route('**/api/world*', r => r.fulfill({ json: { enabled: true, min_mag: 6, hours: 24, quakes: { fetched_at: iso(0), items: [] }, events: { error: 'HTTP 503' },
    fire_risk: { fetched_at: iso(0), url: 'https://www.brandweer.nl/natuurbrandrisico/', regions: Array.from({ length: 25 }, (_, i) => ({ region: 'Regio ' + i, phase: 1 })) } } }));
  await p.goto(BASEURL); await p.waitForSelector('#panel-quakes .pnote');
  const t = await p.textContent('#panel-quakes');
  ok(/Geen zware aardbevingen in de afgelopen 24 uur/.test(t) && /NASA EONET is niet bereikbaar \(HTTP 503\)/.test(t), `empty and error states: ${t.replace(/\s+/g, ' ').slice(0, 160)}`);
  await p.click('#panel-quakes .advf .chip:has-text("Nederland")'); await p.waitForTimeout(200);
  ok(/Overal fase 1: geen extra risico\./.test(await p.textContent('#panel-quakes')), 'all phase 1: one calm line');
  await ctx.close();
}
{ // live data from this server (no fixtures)
  const [ctx, p, errs] = await open(1440, { fixtures: false });
  await p.waitForSelector('#panel-quakes .hzsum', { timeout: 20000 });
  const nlt = await p.textContent('#panel-quakes');
  // live data: brandweer.nl sometimes serves datacenter networks (such as CI runners) a page without the regions; the panel
  // then says so, which is the correct behaviour, and the check passes with a note
  const fireDown = /De brandweer-site is niet bereikbaar/.test(nlt);
  ok(/Natuurbrandrisico/.test(nlt) && (fireDown || !/niet bereikbaar/.test(nlt)), `live Nederland tab${fireDown ? ' (brandweer.nl unreachable from this network: shown as such)' : ''}: ${nlt.replace(/\s+/g, ' ').slice(0, 160)}`);
  await p.click('#panel-quakes .advf .chip:has-text("Wereld")'); await p.waitForSelector('#panel-quakes .wsec');
  const wt = await p.textContent('#panel-quakes');
  ok(/Aardbevingen vanaf M6/.test(wt) && !/niet bereikbaar/.test(wt), `live Wereld tab: ${wt.replace(/\s+/g, ' ').slice(0, 140)}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
