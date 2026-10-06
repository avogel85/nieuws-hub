import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
async function open(w, prefs = {}, scheme = 'light') {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, hasTouch: w < 700, isMobile: w < 700 });
  await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: URL.replace(/\/$/, '') });
  await ctx.addInitScript(v => { if (!sessionStorage.getItem('s')) { localStorage.setItem('ndb:prefs', v); sessionStorage.setItem('s', '1'); } }, JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(URL);
  for (const s of ['#panel-pollen .itab', '#panel-sky .trow', '#panel-sports .spsec', '.wxthunder', '#stream .item'])
    await p.waitForSelector(s, { state: 'attached', timeout: 60000 });
  await p.waitForTimeout(400);
  return [ctx, p, errs];
}
const sky = (await (await fetch(URL + 'api/sky')).json()).data;
const sports = await (await fetch(URL + 'api/sports')).json();
const vulnsAPI = await fetch(URL + 'api/vulns');

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, {}, scheme);
  if (w < 700) { await p.click('#mv-panels'); await p.waitForTimeout(200); }
  const t = await p.evaluate(() => ({
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    heads: ['pollen', 'sky', 'sports'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    vulnsPanel: !!document.querySelector('#panel-vulns'), tip: document.querySelector('#panel-pollen .pbody').textContent,
    irows: [...document.querySelectorAll('#panel-pollen tbody tr')].map(r => [...r.querySelectorAll('.ilv')].map(x => x.textContent).join('/')),
    iextra: document.querySelector('#p-pollen-extra').textContent,
    sky: [...document.querySelectorAll('#panel-sky .trow')].map(x => x.textContent), skyFoot: document.querySelector('#panel-sky .pfoot').textContent,
    sp: [...document.querySelectorAll('#panel-sports .spsec h3')].map(x => x.lastChild.textContent), f1: document.querySelector('#panel-sports .spsec')?.textContent,
    thunder: document.querySelector('.wxthunder').textContent,
    accent: getComputedStyle(document.documentElement).getPropertyValue('--accent').trim(),
    bg: getComputedStyle(document.body).backgroundColor, sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify({ ...t, order: t.order.join() }, null, 1).slice(0, 1800));
  const i = n => t.order.indexOf(n);
  ok(i('sky') === i('pollen') + 1 && i('sports') === i('markets') + 1 && i('breaches') === i('advisories') + 1, `${w} ${scheme}: panel positions`);
  ok(t.heads.join('|') === 'Gezondheid|Vanavond aan de hemel|Sportagenda', 'panel names (Teken en muggen is part of Gezondheid since 1.30)');
  ok(!t.vulnsPanel && vulnsAPI.status === 404 && !/lange broek|ramen dicht|Tip:/.test(t.tip), 'Kwetsbaarheden panel removed (404), no tips in Teken en muggen');
  ok(t.irows.length === 3 && t.irows.every(r => /^(geen|laag|matig|hoog)\/(geen|laag|matig|hoog)$/.test(r)) && /wijzigen/.test(t.iextra), `ticks/mosquitoes: ${t.irows.join(', ')} [${t.iextra}]`);
  ok(/^🌆Donker vanaf \d\d:\d\d \(zon onder \d\d:\d\d\) tot \d\d:\d\d$/.test(t.sky[0]) && t.sky.some(x => /^🌙Maan: /.test(x)) && t.sky.some(x => /^🌌Noorderlicht: /.test(x)) &&
    t.sky.filter(x => /^🪐/.test(x)).length === Math.max(1, sky.planets.length) && !/standaardlocatie/.test(t.skyFoot), `sky: ${t.sky.length} rows, ${sky.planets.length} planets [${t.skyFoot.slice(0, 40)}]`);
  ok(t.sp.join('|') === 'Formule 1|Mountainbike|Atletiek' && /Volgende: .*Laatste: .*Stand: 1\./.test(t.f1), `sports: ${t.sp.join(', ')}`);
  ok(/^⚡ Onweer komende 24 uur: (geen|kleine|matige|grote) kans/.test(t.thunder), `thunder: ${t.thunder}`);
  ok(t.accent === (scheme === 'dark' ? t.accent : '#007da7') && t.accent !== '#0b5cad' && t.accent !== '#6cb4ff', `accent from config.yaml: ${t.accent}`);
  ok(t.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors ${errs.join('|')}`);
  const r = await new AxeBuilder({ page: p }).include(['#panel-pollen', '#panel-sky', '#panel-sports', '#panel-weather']).analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe (incl. contrast with the accent): ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await ctx.close();
}

// sports: choose in the settings
{
  const [ctx, p] = await open(1440);
  await p.click('#panel-sports .pfoot .linkbtn'); await p.waitForSelector('#settings[open]');
  ok(await p.evaluate(() => !document.querySelector('#set-sports').hidden && document.querySelectorAll('#sports-opts input').length === 3), 'Sportagenda settings: 3 sports');
  await p.click('#sports-opts label:has(input[data-sport="mtb"])'); await p.click('#settings-close'); await p.waitForTimeout(200);
  const hs = await p.evaluate(() => [...document.querySelectorAll('#panel-sports .spsec h3')].map(x => x.lastChild.textContent).join('|'));
  ok(hs === 'Formule 1|Atletiek' && await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).sports.join() === 'f1,athletics'), `mountain bike switched off: ${hs}`);
  // share: without Web Share the link is copied
  const hasShare = await p.evaluate(() => !!navigator.share);
  if (!hasShare) {
    await p.click('#stream .item >> nth=0 >> .sh');
    await p.waitForFunction(() => /Link gekopieerd/.test(document.querySelector('#toast').textContent));
    const clip = await p.evaluate(() => navigator.clipboard.readText()), url = await p.evaluate(() => document.querySelector('#stream .item .t a').href);
    ok(clip === url, 'share button copies the link (no Web Share in this browser)');
  } else ok(true, 'Web Share available (share sheet not testable headless)');
  await ctx.close();
}

// phone: three views, pull to refresh
{
  const [ctx, p, errs] = await open(390);
  const swipe = (x0, x1) => p.evaluate(([x0, x1]) => { const target = document.elementFromPoint(x0, 500); const t = x => new Touch({ identifier: 1, target, clientX: x, clientY: 500 });
    target.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, touches: [t(x0)], changedTouches: [t(x0)] })); target.dispatchEvent(new TouchEvent('touchend', { bubbles: true, touches: [], changedTouches: [t(x1)] })); }, [x0, x1]);
  const view = () => p.evaluate(() => ({ v: document.documentElement.dataset.mview, digest: getComputedStyle(document.querySelector('#digest')).display, cards: document.querySelectorAll('#dgrid .dcard').length,
    exit: getComputedStyle(document.querySelector('#digest-exit')).display, tabs: [...document.querySelectorAll('#mview button')].map(b => b.textContent + (b.getAttribute('aria-selected') === 'true' ? '*' : '')).join('|') }));
  ok((await view()).tabs === 'Overzicht|Nieuws*|Dagelijks|Achtergrond', 'phone: four tabs, starts on the news');
  await swipe(100, 300); await p.waitForTimeout(400);
  let v = await view();
  ok(v.v === 'overview' && v.digest === 'block' && v.cards >= 3 && v.exit === 'none', `swipe right from the news: overview (${v.cards} cards, no "Volledige weergave" button)`);
  await swipe(300, 100); await p.waitForTimeout(200); await swipe(300, 100); await p.waitForTimeout(200);
  ok((await view()).v === 'panels', 'two swipes left: panels');
  await swipe(300, 100); await p.waitForTimeout(200);
  ok((await view()).v === 'panels', 'no view after the panels');
  await p.click('#mv-news'); await p.evaluate(() => scrollTo(0, 0));
  const n0 = await p.evaluate(() => performance.getEntriesByType('resource').filter(e => /api\/news/.test(e.name)).length);
  await p.evaluate(() => { const target = document.querySelector('#stream'); const t = y => new Touch({ identifier: 2, target, clientX: 200, clientY: y });
    target.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, touches: [t(300)], changedTouches: [t(300)] }));
    target.dispatchEvent(new TouchEvent('touchmove', { bubbles: true, touches: [t(420)], changedTouches: [t(420)] })); });
  const ptr = await p.evaluate(() => ({ hidden: document.querySelector('#ptr').hidden, text: document.querySelector('#ptr').textContent }));
  await p.evaluate(() => { const target = document.querySelector('#stream'); const t = y => new Touch({ identifier: 2, target, clientX: 200, clientY: y });
    target.dispatchEvent(new TouchEvent('touchend', { bubbles: true, touches: [], changedTouches: [t(420)] })); });
  await p.waitForFunction(() => /Bijgewerkt/.test(document.querySelector('#toast').textContent));
  const n1 = await p.evaluate(() => performance.getEntriesByType('resource').filter(e => /api\/news/.test(e.name)).length);
  ok(!ptr.hidden && /Loslaten om te verversen/.test(ptr.text) && n1 > n0, `pull to refresh: "${ptr.text}", news fetched again, "Bijgewerkt"`);
  ok(await p.evaluate(() => getComputedStyle(document.documentElement).overscrollBehaviorY) === 'contain', "the browser's own pull-to-reload is off in the phone views");
  ok(errs.length === 0, `no page errors ${errs.join('|')}`);
  await ctx.close();
}

// English
{
  const [ctx, p] = await open(1440, { lang: 'en' });
  const e = await p.evaluate(() => ({ heads: ['pollen', 'sky', 'sports'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    sky: document.querySelector('#panel-sky .pbody').innerText, th: document.querySelector('.wxthunder').textContent }));
  ok(e.heads.join('|') === "Health|Tonight's sky|Sports calendar", `English names: ${e.heads}`);
  ok(/Dark from/.test(e.sky) && /Northern lights/.test(e.sky) && /Thunderstorms in the next 24 hours/.test(e.th), 'English texts');
  await ctx.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
