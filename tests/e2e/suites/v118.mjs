import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
const raised = { enabled: true, fetched_at: new Date().toISOString(), data: { nearest: { id: 'NL1247', name: 'Scheveningen', value: 0.42, time: new Date(Date.now() - 3 * 36e5).toISOString() },
  km: 4, min: 0.07, median: 0.2, max: 0.61, max_name: 'Borssele', stations: 149, raised: true, above: 12, level: 0.3, time: new Date().toISOString() } };
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, prefs = {}, route = null } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', hasTouch: mobile, isMobile: mobile });
  await ctx.addInitScript(([l, pr]) => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l, ...pr })), [lang, prefs]);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (route) await route(p);
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(1500);
  if (mobile) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  return [ctx, p, errs];
}
const txt = (p, sel) => p.evaluate(s => document.querySelector(s)?.textContent ?? null, sel);

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const mobile = w === 360;
  const [ctx, p, errs] = await open(w, { scheme, mobile });
  await p.waitForSelector('#panel-weather .wxuv', { timeout: 20000 }).catch(() => {});
  const uv = await txt(p, '#panel-weather .wxuv'), sun = await txt(p, '#panel-weather .sunline');
  ok(/^☀ UV (vandaag|morgen) max \d+ \((laag|matig|hoog|zeer hoog|extreem)\) rond \d\d:\d\d/.test(uv || ''), `${w} ${scheme}: UV line "${uv}"`);
  ok(sun && !/UV/.test(sun), `sunrise line without the old UV number: "${sun}"`);
  const uvIdx = await p.evaluate(() => { const ps = [...document.querySelectorAll('#p-weather-body > *')]; return [ps.findIndex(e => e.classList.contains('wxthunder')), ps.findIndex(e => e.classList.contains('wxuv'))]; });
  ok(uvIdx[1] === uvIdx[0] + 1, `UV line right below the thunderstorm line (${uvIdx})`);
  await p.waitForSelector('#panel-air .radl', { timeout: 20000 }).catch(() => {});
  const rad = await txt(p, '#panel-air .radl');
  ok(/^☢ Straling: 0,\d\d µSv\/u · normaal/.test(rad || '') && /Meetpost .+ \(\d+ km\) om \d\d:\d\d · in Nederland 0,\d\d–0,\d\d µSv\/u · bron: RIVM via EURDEP/.test(rad || ''), `radiation line: "${rad}"`);
  const radBar = await p.evaluate(() => { const a = document.querySelector('#ab-rad'); const r = a.getBoundingClientRect(); return { hidden: a.hidden, display: getComputedStyle(a).display, w: r.width, visible: [...document.querySelectorAll('#alertbar .ab')].filter(x => x.getBoundingClientRect().width > 0).map(x => x.id).join() }; });
  ok(radBar.hidden && radBar.display === 'none' && radBar.w === 0 && !radBar.visible.includes('ab-rad'), `no radiation notice visible in the top bar at normal levels (${JSON.stringify(radBar)})`);
  ok(!(await p.$('#panel-energy .solar')), 'no solar section before panels are set');
  if (w === 1440 && scheme === 'light') {
    await p.locator('#panel-weather .wxuv').screenshot({ path: `${OUT}/uv.png` });
    await p.locator('#panel-air').screenshot({ path: `${OUT}/air-rad.png` });
  }
  await axe(p, '#panel-weather', `${w} ${scheme} weather`);
  await axe(p, '#panel-air', `${w} ${scheme} air`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, `no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}

// ---------- raised radiation: top-bar notice and red line
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, { scheme, mobile: w === 360, route: p => p.route('**/api/radiation*', r => r.fulfill({ json: raised })) });
  const s = await p.evaluate(() => { const a = document.querySelector('#ab-rad'); return { hidden: a.hidden, text: a.textContent, href: a.getAttribute('href'), title: a.title,
    order: [...document.querySelectorAll('#alertbar .ab')].filter(x => x.getBoundingClientRect().width > 0).map(x => x.id).join(','), dot: getComputedStyle(a.querySelector('.d')).backgroundColor }; });
  ok(!s.hidden && s.text === 'Straling: verhoogd' && s.href === '#panel-air' && /12 meetposten van het RIVM meten 0,30 µSv\/u of meer; hoogste 0,61 µSv\/u in Borssele/.test(s.title), `${w} ${scheme}: top-bar notice "${s.text}" (${s.title})`);
  ok(['ab-p2k,ab-nl,ab-rad,ab-nctv', 'ab-p2k,ab-rad,ab-nctv'].includes(s.order.replace(/^ab-knmi,/, '')), `after NL-Alert, before Dreigingsniveau: ${s.order}`);
  const rad = await p.evaluate(() => { const e = document.querySelector('#panel-air .radl'); return e && { text: e.textContent, up: e.classList.contains('up') }; });
  ok(rad?.up && /Straling: 0,42 µSv\/u · verhoogd/.test(rad.text), `red line in Luchtkwaliteit: ${rad?.text}`);
  if (w === 1440 && scheme === 'light') await p.locator('#alertbar').screenshot({ path: `${OUT}/rad-bar.png` });
  await axe(p, '#alertbar', `${w} ${scheme} top bar`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, 'no errors, no horizontal scroll');
  await ctx.close();
}

// ---------- solar: settings, section, change, clear
{
  const [ctx, p, errs] = await open(1440);
  await p.click('#open-settings');
  await p.waitForTimeout(300);
  ok(await p.isVisible('#set-solar'), 'settings: Zonnepanelen section');
  await axe(p, '#set-solar', 'solar settings');
  await p.fill('#solar-kwp', '0'); await p.click('#solar-save');
  ok(/Vul een vermogen/.test(await txt(p, '#solar-msg')), 'invalid kWp rejected');
  await p.fill('#solar-kwp', '4,2'); await p.selectOption('#solar-az', '0'); await p.fill('#solar-tilt', '35'); await p.click('#solar-save');
  ok(/Opgeslagen/.test(await txt(p, '#solar-msg')), 'saved');
  await p.keyboard.press('Escape'); await p.waitForTimeout(1500);
  const api = await p.evaluate(async () => (await (await fetch('api/solar?tilt=35&az=0')).json()).days);
  const sec = await txt(p, '#panel-energy .solar');
  const want = n => (Math.round(n * 4.2 * 10) / 10).toLocaleString('nl-NL', { maximumFractionDigits: 1 });
  ok(sec && sec.includes('(4,2 kWp, zuid, 35°)') && sec.includes(`Vandaag ~${want(api[0].kwh_per_kw)} kWh`) && sec.includes(`morgen ~${want(api[1].kwh_per_kw)} kWh`), `section: ${sec}`);
  ok(/Meeste opbrengst (vandaag|morgen) \d\d:\d\d–\d\d:\d\d/.test(sec) && /Open-Meteo/.test(sec), 'sunniest 3 hours and source');
  await p.locator('#panel-energy').screenshot({ path: `${OUT}/solar.png` });
  await axe(p, '#panel-energy', 'energy with solar');
  const stored = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).solar);
  ok(stored?.kwp === 4.2 && stored.tilt === 35 && stored.az === 0, `stored in the browser: ${JSON.stringify(stored)}`);
  await p.click('#panel-energy .solar .linkbtn'); await p.waitForTimeout(400);
  ok(await p.inputValue('#solar-kwp') === '4,2' && await p.evaluate(() => document.activeElement.id) === 'solar-kwp', '"wijzigen" opens the settings on the kWp field');
  await p.selectOption('#solar-az', '90'); await p.click('#solar-save'); await p.keyboard.press('Escape'); await p.waitForTimeout(1500);
  ok((await txt(p, '#panel-energy .solar')).includes('west'), 'direction change shows west');
  await p.click('#panel-energy .solar .linkbtn'); await p.waitForTimeout(300); await p.click('#solar-clear'); await p.keyboard.press('Escape'); await p.waitForTimeout(500);
  ok(!(await p.$('#panel-energy .solar')), 'cleared: section gone');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
// ---------- phone and dark with solar set, English
for (const [w, scheme, lang] of [[360, 'light', 'nl'], [1440, 'dark', 'nl'], [1440, 'light', 'en']]) {
  const [ctx, p, errs] = await open(w, { scheme, lang, mobile: w === 360, prefs: { solar: { kwp: 3.6, tilt: 30, az: -45 } } });
  await p.waitForSelector('#panel-energy .solar', { timeout: 20000 }).catch(() => {});
  const sec = await txt(p, '#panel-energy .solar');
  if (lang === 'en') {
    const uv = await txt(p, '#panel-weather .wxuv'), rad = await txt(p, '#panel-air .radl');
    ok(/Solar power \(3\.6 kWp, south-east, 30°\)/.test(sec || '') && /Today ~[\d.]+ kWh · tomorrow ~/.test(sec || ''), `English solar: ${sec}`);
    ok(/UV (today|tomorrow) up to \d+/.test(uv || '') && /Radiation: 0\.\d\d µSv\/h · normal/.test(rad || ''), `English UV/radiation: ${uv} | ${rad}`);
  } else {
    ok(/\(3,6 kWp, zuidoost, 30°\)/.test(sec || ''), `${w} ${scheme}: solar from stored prefs: ${sec}`);
    await axe(p, '#panel-energy', `${w} ${scheme} energy`);
  }
  if (w === 360) await p.locator('#panel-energy').screenshot({ path: `${OUT}/solar-360.png` });
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, `${w} ${scheme} ${lang}: no errors, no horizontal scroll`);
  await ctx.close();
}
// ---------- KNMI in the top bar: hidden without warnings, shown for a warning or when unknown
for (const [name, knmi, want] of [
  ['no warnings', { fetched_at: new Date().toISOString(), status: { level: 'none', active: false, count: 0 }, url: 'https://www.knmi.nl/' }, null],
  ['unknown', { error: 'MeteoAlarm: timeout', url: 'https://www.knmi.nl/' }, /^KNMI: waarschuwingen onbekend$/],
  ['code yellow', { fetched_at: new Date().toISOString(), status: { level: 'yellow', active: true, count: 2, types: ['Wind'], areas: ['Zeeland', 'Noord-Holland'] }, url: 'https://www.knmi.nl/' }, /^KNMI code geel: wind \(Zeeland, Noord-Holland\)$/],
]) {
  const [ctx, p, errs] = await open(1440, { route: async p => p.route('**/api/alerts', async r => {
    const res = await r.fetch(); const j = await res.json(); j.knmi = knmi; return r.fulfill({ response: res, json: j }); }) });
  const k = await p.evaluate(() => { const a = document.querySelector('#ab-knmi'); return { shown: a.getBoundingClientRect().width > 0, text: a.textContent,
    first: [...document.querySelectorAll('#alertbar .ab')].find(x => x.getBoundingClientRect().width > 0)?.id }; });
  ok(want ? k.shown && want.test(k.text) : !k.shown && k.first === 'ab-p2k', `KNMI ${name}: ${k.shown ? '"' + k.text + '"' : 'hidden, top bar starts with ' + k.first}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
