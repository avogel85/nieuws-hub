// 1.29: Zee en getij (Weer), Op deze dag (Vandaag), Oplichting en phishing (Datalekken).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
const iso = ms => new Date(Date.now() + ms).toISOString();
const SEA = { enabled: true, station: { id: 'scheveningen', name: 'Scheveningen', km: 4 }, fetched_at: iso(0),
  tides: [{ time: iso(-20 * 6e4), high: false, cm: -55 }, { time: iso(5 * 36e5), high: true, cm: 112 }, { time: iso(11 * 36e5), high: false, cm: -51 }, { time: iso(17 * 36e5), high: true, cm: 98 }],
  sea: { temp: 16.4, wave: 0.8, wave_dir: 270, wave_max: 1.4 } };
const OTD = { fetched_at: iso(0), title: '3 oktober', url: 'https://nl.wikipedia.org/wiki/3_oktober', events: [
  { year: 1574, text: 'Leiden wordt ontzet; het beleg door de Spanjaarden is voorbij.' }, { year: 1863, text: 'Abraham Lincoln maakt van Thanksgiving Day een nationale feestdag.' },
  { year: 1990, text: 'Duitsland wordt herenigd.' }, { year: 2000, text: 'In Den Haag wordt het eerste homohuwelijk wettelijk mogelijk.' }] };
const PHISH = { fetched_at: iso(0), url: 'https://www.fraudehelpdesk.nl/actueel/', items: [
  { title: 'Valse telefoontjes over ‘thuisbatterij’', url: 'https://www.fraudehelpdesk.nl/a', published: iso(-2 * 864e5) },
  { title: 'Valse websites restaurants', url: 'https://www.fraudehelpdesk.nl/b', published: iso(-6 * 864e5) },
  { title: 'Valse sms GBLT', url: 'https://www.fraudehelpdesk.nl/c', published: iso(-12 * 864e5) },
  { title: 'Oude waarschuwing', url: 'https://www.fraudehelpdesk.nl/d', published: iso(-45 * 864e5) }] };
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, sea = SEA, otd = OTD, phish = PHISH, live = false } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', isMobile: mobile, hasTouch: mobile });
  await ctx.addInitScript(l => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l })), lang);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (!live) {
    await p.route('**/api/sea*', r => r.fulfill({ json: sea }));
    await p.route('**/api/today', async r => { const res = await r.fetch(); const j = await res.json(); if (otd) j.on_this_day = otd; else delete j.on_this_day; return r.fulfill({ response: res, json: j }); });
    await p.route('**/api/breaches', async r => { const res = await r.fetch(); const j = await res.json(); if (phish) j.phishing = phish; else delete j.phishing; return r.fulfill({ response: res, json: j }); });
  }
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(2500);
  return [ctx, p, errs];
}
const text = (p, sel) => p.evaluate(s => document.querySelector(s)?.innerText.replace(/\s+/g, ' ').trim() || '', sel);

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const tag = `${w} ${scheme}`, mobile = w === 360;
  const [ctx, p, errs] = await open(w, { scheme, mobile });
  if (mobile) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  // Zee en getij
  await p.waitForSelector('#panel-weather .seab');
  const sea = await text(p, '#panel-weather .seab');
  ok(/^🌊 ZEE EN GETIJ · Scheveningen/i.test(sea), `${tag}: sea block heading: ${sea.slice(0, 40)}`);
  ok(/laagwater (morgen )?\d\d:\d\d \(-55 cm\) · hoogwater (morgen )?\d\d:\d\d \(\+112 cm\) · laagwater (morgen )?\d\d:\d\d \(-51 cm\)/.test(sea) && !/98 cm/.test(sea), `${tag}: the next three tides: ${sea.slice(25, 140)}`);
  ok(/zeewater 16,4° · golven 0,8 m uit het W · vandaag tot 1,4 m/.test(sea) && /Rijkswaterstaat/.test(sea), `${tag}: sea temperature and waves`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-weather .seab').screenshot({ path: `${OUT}/sea-tide.png` });
  await axe(p, '#panel-weather', `${tag} weather`);
  // Op deze dag
  const otd = await text(p, '#panel-today .otd');
  ok(/OP DEZE DAG/i.test(otd) && /1574 – Leiden wordt ontzet/.test(otd) && /2000 – In Den Haag/.test(otd) && /Meer op Wikipedia \(3 oktober\)/.test(otd), `${tag}: on this day: ${otd.slice(0, 80)}`);
  ok(await p.$eval('#panel-today .otd ul', e => e.getAttribute('translate')) === 'no', `${tag}: the events are content (not translated)`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-today').screenshot({ path: `${OUT}/on-this-day.png` });
  await axe(p, '#panel-today', `${tag} today`);
  // Oplichting en phishing (on a phone: the Achtergrond page)
  if (mobile) { await p.click('#mv-panels2'); await p.waitForTimeout(300); }
  await p.waitForSelector('#panel-breaches .phgrp li');
  const ph = await p.evaluate(() => { const g = document.querySelector('#panel-breaches .phgrp'); return { t: g?.innerText.replace(/\s+/g, ' '), n: g?.querySelectorAll('li').length, links: [...(g?.querySelectorAll('li a') || [])].map(a => a.href) }; });
  ok(/Oplichting en phishing/i.test(ph.t) && ph.n === 3 && !/Oude waarschuwing/.test(ph.t) && ph.links[0] === 'https://www.fraudehelpdesk.nl/a', `${tag}: the 3 warnings of the last 30 days: ${ph.t?.slice(0, 90)}`);
  ok(/Alle waarschuwingen en hulp \(Fraudehelpdesk\)/.test(ph.t) && /Fraudehelpdesk/.test(await text(p, '#panel-breaches .pfoot')), `${tag}: link to the Fraudehelpdesk and source in the footer`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-breaches').screenshot({ path: `${OUT}/phishing.png` });
  await axe(p, '#panel-breaches', `${tag} breaches`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, `${tag}: no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
{ // empty and error states
  const [ctx, p, errs] = await open(1440, { sea: { ...SEA, tides: undefined, tides_error: 'HTTP 503', sea: undefined }, otd: { error: 'HTTP 500' }, phish: { ...PHISH, items: [PHISH.items[3]] } });
  ok(/Getijden nu niet beschikbaar \(Rijkswaterstaat\)\./.test(await text(p, '#panel-weather .seab')), 'tides unavailable: one short line');
  ok(!(await p.$('#panel-today .otd')), 'no events: no Op deze dag block');
  ok(/Geen nieuwe waarschuwingen van de Fraudehelpdesk in de afgelopen 30 dagen\./.test(await text(p, '#panel-breaches')), 'no recent warnings: a calm note');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{ // switched off in config.yaml: nothing shown
  const [ctx, p] = await open(1440, { sea: { enabled: false }, otd: null, phish: null });
  const off = { sea: !!(await p.$('#panel-weather .seab')), otd: !!(await p.$('#panel-today .otd')), phish: /phishing/i.test(await text(p, '#panel-breaches')) };
  ok(!off.sea && !off.otd && !off.phish, `disabled: no sea, on-this-day or phishing blocks ${JSON.stringify(off)}`);
  await ctx.close();
}
{ // English
  const [ctx, p] = await open(1440, { lang: 'en' });
  const sea = await text(p, '#panel-weather .seab');
  ok(/SEA AND TIDE/i.test(sea) && /low tide \d\d:\d\d/.test(sea) && /sea water 16\.4° · waves 0\.8 m from the W/.test(sea), `English sea: ${sea.slice(0, 120)}`);
  ok(/ON THIS DAY/i.test(await text(p, '#panel-today')) && /Leiden wordt ontzet/.test(await text(p, '#panel-today')), 'English: heading translated, events in Dutch');
  ok(/Scams and phishing/i.test(await text(p, '#panel-breaches')), 'English phishing heading');
  await ctx.close();
}
{ // live data from this server
  const [ctx, p, errs] = await open(1440, { live: true });
  let sea = '', otd = '';
  for (let i = 0; i < 8; i++) {
    sea = await text(p, '#panel-weather .seab'); otd = await text(p, '#panel-today .otd');
    if (/water/.test(sea) && otd) break;
    await p.waitForTimeout(2500); await p.reload(); await p.waitForSelector('#stream .item'); await p.waitForTimeout(2500);
  }
  ok(/(hoog|laag)water (morgen )?\d\d:\d\d \([+-]?\d+ cm\)/.test(sea) && /zeewater \d+,\d°/.test(sea), `live sea and tide: ${sea.slice(0, 160)}`);
  ok(/OP DEZE DAG/i.test(otd) && (otd.match(/\d{1,4} – /g) || []).length >= 3, `live on this day: ${otd.slice(0, 160)}`);
  const ph = await text(p, '#panel-breaches');
  ok(/Oplichting en phishing/i.test(ph) && !/niet bereikbaar/.test(ph), `live phishing: ${ph.slice(ph.indexOf('Oplichting'), ph.indexOf('Oplichting') + 120)}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{ // 1.30: Gezondheid = Hooikoorts + Teken en muggen; renamed panels; old layouts migrate
  const [ctx, p, errs] = await open(1440, { live: true });
  await p.waitForSelector('#panel-pollen .itab');
  const g = await p.evaluate(() => ({ h2: document.querySelector('#panel-pollen h2').textContent, secs: [...document.querySelectorAll('#panel-pollen .wsec h3')].map(x => x.textContent.trim()),
    insects: !!document.querySelector('#panel-insects'), names: ['health', 'ap', 'ransomware'].map(id => document.querySelector(`#panel-${id} h2`)?.textContent).join('|') }));
  ok(g.h2 === 'Gezondheid' && g.secs.join('|') === '🌾 Hooikoorts|🕷️ Teken en muggen' && !g.insects, `one panel Gezondheid with two sections: ${g.secs.join(' | ')}`);
  ok(g.names === 'RIVM|Autoriteit Persoonsgegevens|Ransomware', `renamed panels: ${g.names}`);
  await axe(p, '#panel-pollen', 'Gezondheid');
  await p.locator('#panel-pollen').screenshot({ path: `${OUT}/gezondheid.png` });
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
  const c2 = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await c2.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, panels: { order: ['weather', 'pollen', 'insects', 'sky'], hidden: { pollen: true }, collapsed: {} } })));
  const p2 = await c2.newPage(); await p2.goto(BASEURL); await p2.waitForSelector('#stream .item');
  ok(!!(await p2.$('#panel-pollen')), 'Hooikoorts hidden but Teken en muggen visible: the combined panel stays visible');
  await c2.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
