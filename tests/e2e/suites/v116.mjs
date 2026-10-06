import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const thumb = 'data:image/svg+xml;utf8,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="56" height="56"><rect width="56" height="56" fill="#888"/></svg>');
const TREND = { enabled: true, window_hours: 3, terms: [{ term: 'Trump', sources: 9 }, { term: 'Onbekend Woord', sources: 4 }, { term: 'Den Haag', sources: 5 }] };
const WIKI = {
  Trump: { enabled: true, term: 'Trump', search: 'https://nl.wikipedia.org/w/index.php?search=Trump', summary: { found: true, title: 'Donald Trump', description: 'president van de Verenigde Staten', extract: 'Donald John Trump is een Amerikaans zakenman, mediapersoonlijkheid en politicus.', url: 'https://nl.wikipedia.org/wiki/Donald_Trump', thumb } },
  'Onbekend Woord': { enabled: true, term: 'Onbekend Woord', search: 'https://nl.wikipedia.org/w/index.php?search=Onbekend+Woord', summary: { found: false } },
};
let wikiCalls = 0;
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, fixtures = true, sources = null } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 900 }, colorScheme: scheme, serviceWorkers: 'block', hasTouch: mobile, isMobile: mobile });
  await ctx.addInitScript(([l, s]) => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l, sources: s })), [lang, sources]);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (fixtures) {
    await p.route('**/api/trending*', r => r.fulfill({ json: TREND }));
    await p.route('**/api/wiki?*', r => { wikiCalls++; const t = new globalThis.URL(r.request().url()).searchParams.get('term'); return r.fulfill({ json: WIKI[t] || WIKI['Onbekend Woord'] }); });
  }
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(800);
  return [ctx, p, errs];
}
const card = p => p.evaluate(() => {
  const c = document.querySelector('#wikicard'); const r = c?.getBoundingClientRect();
  return c && { hidden: c.hidden, title: c.querySelector('.wk-title')?.textContent, text: c.textContent, link: c.querySelector('a')?.href,
    img: !!c.querySelector('img'), right: r.right, left: r.left, top: r.top, sw: document.documentElement.scrollWidth,
    exp: [...document.querySelectorAll('#trending .tinfo')].map(x => x.getAttribute('aria-expanded')).join(), focus: document.activeElement?.className };
});

// ---------- Wikipedia card, desktop: hover
{
  const [ctx, p, errs] = await open(1440);
  const grp = p.locator('#trending .tchipgrp').first();
  ok(await p.locator('#trending .tchipgrp').count() === 3 && await p.locator('#trending .tinfo').count() === 3, 'each trending chip has an ⓘ button');
  await grp.locator('.tchip').hover(); await p.waitForTimeout(250);
  ok((await card(p)).hidden, 'no card before 0.5 s of hovering');
  await p.waitForTimeout(600);
  let c = await card(p);
  ok(!c.hidden && c.title === 'Donald Trump' && /zakenman/.test(c.text) && c.link === 'https://nl.wikipedia.org/wiki/Donald_Trump' && c.img && /CC BY-SA/.test(c.text), `hover opens the card: ${c.title}`);
  await p.locator('#trending').screenshot({ path: `${OUT}/wiki-card.png` }).catch(() => {});
  await p.locator('#wikicard').hover(); await p.waitForTimeout(400);
  ok(!(await card(p)).hidden, 'moving onto the card keeps it open');
  await p.mouse.move(700, 800); await p.waitForTimeout(400);
  ok((await card(p)).hidden, 'leaving closes it');
  const r = await new AxeBuilder({ page: p }).include('#trending').analyze();
  ok(r.violations.length === 0, `axe trending (closed): ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  // keyboard: ⓘ, then Escape returns focus
  await p.locator('#trending .tinfo').nth(1).focus(); await p.keyboard.press('Enter'); await p.waitForTimeout(300);
  c = await card(p);
  ok(!c.hidden && /Geen Wikipedia-artikel gevonden voor “Onbekend Woord”/.test(c.text) && /search=Onbekend/.test(c.link) && c.exp === 'false,true,false', `ⓘ opens "not found" with a search link (${c.exp})`);
  const r2 = await new AxeBuilder({ page: p }).include('#trending').analyze();
  ok(r2.violations.length === 0, `axe trending (card open): ${r2.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await p.keyboard.press('Escape'); await p.waitForTimeout(100);
  c = await card(p);
  const q = await p.inputValue('#q');
  ok(c.hidden && c.focus === 'tinfo' && c.exp === 'false,false,false', 'Escape closes and returns focus to ⓘ');
  // chip click still filters
  await p.locator('#trending .tchip').first().click(); await p.waitForTimeout(300);
  ok(await p.inputValue('#q') === 'Trump' && q === '', 'clicking the chip itself still searches');
  const n = wikiCalls; await p.locator('#trending .tinfo').first().click(); await p.waitForTimeout(300);
  ok(wikiCalls === n && !(await card(p)).hidden, 'a second look uses the cached answer');
  await p.mouse.click(700, 850); await p.waitForTimeout(100);
  ok((await card(p)).hidden, 'a click elsewhere closes the card');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
// ---------- phone: tap ⓘ, card inside the screen
for (const scheme of ['light', 'dark']) {
  const [ctx, p, errs] = await open(360, { mobile: true, scheme });
  await p.locator('#trending .tchip').first().tap(); await p.waitForTimeout(700);
  ok((await card(p)).hidden && await p.inputValue('#q') === 'Trump', `360 ${scheme}: tapping a chip searches, no card (touch)`);
  await p.locator('#trending .tchip').first().tap(); await p.waitForTimeout(200); // toggles the search off
  await p.locator('#trending .tinfo').nth(2).tap(); await p.waitForTimeout(300);
  const c = await card(p);
  ok(!c.hidden && c.left >= 0 && c.right <= 360 && c.sw <= 360, `360 ${scheme}: tap ⓘ opens the card within the screen (${Math.round(c.left)}–${Math.round(c.right)})`);
  if (scheme === 'light') await p.screenshot({ path: `${OUT}/wiki-360.png` });
  const r = await new AxeBuilder({ page: p }).include('#trending').analyze();
  ok(r.violations.length === 0, `360 ${scheme} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
// ---------- English
{
  const [ctx, p] = await open(1440, { lang: 'en' });
  await p.locator('#trending .tinfo').nth(1).click(); await p.waitForTimeout(300);
  const c = await card(p);
  ok(/No Wikipedia article found for “Onbekend Woord”/.test(c.text) && /Search Wikipedia/.test(c.text), 'English card');
  ok(await p.getAttribute('#trending .tinfo >> nth=0', 'aria-label') === 'About Trump (Wikipedia)', 'English ⓘ label');
  await ctx.close();
}
// ---------- live Wikipedia through this server (no fixtures)
{
  const [ctx, p, errs] = await open(1440, { fixtures: false });
  const n = await p.locator('#trending .tinfo').count();
  if (n) {
    await p.locator('#trending .tinfo').first().click();
    await p.waitForFunction(() => !/geraadpleegd/.test(document.querySelector('#wikicard').textContent), null, { timeout: 15000 }).catch(() => {});
    const c = await card(p);
    console.log('live:', c.title || c.text.slice(0, 80));
    ok(!c.hidden && (c.title || /Geen Wikipedia-artikel/.test(c.text)) && !/niet bereikbaar/.test(c.text), 'live lookup through /api/wiki');
    ok(!c.link || c.link.startsWith('https://nl.wikipedia.org/'), 'link to nl.wikipedia.org only');
  } else ok(true, 'live: no trending terms right now (skipped)');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}

// ---------- satellite panel
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, { scheme, fixtures: false });
  if (w === 360) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  await p.waitForFunction(() => document.querySelector('#panel-satellite .satwrap img')?.complete, null, { timeout: 15000 }).catch(() => {});
  const s = await p.evaluate(() => {
    const pn = document.querySelector('#panel-satellite'), imgs = [...pn.querySelectorAll('.satwrap img')];
    const order = [...document.querySelectorAll('#side .panel')].map(x => x.id.replace('panel-', ''));
    const r = pn.querySelector('.satwrap').getBoundingClientRect();
    return { order: order.slice(0, 3).join(), n: imgs.length, nat: imgs.map(i => i.naturalWidth), alt: imgs[0]?.alt, ovAlt: imgs[1]?.getAttribute('alt'),
      foot: pn.querySelector('.pfoot')?.textContent, link: pn.querySelector('.pfoot a')?.href, fresh: pn.querySelector('.pfresh')?.textContent,
      ratio: r.width / r.height, w: r.width, head: pn.querySelector('h2').textContent, sw: document.documentElement.scrollWidth };
  });
  if (w === 1440 && scheme === 'light') { console.log(JSON.stringify(s)); await p.locator('#panel-satellite').screenshot({ path: `${OUT}/sat-panel.png` }); }
  if (w === 360) await p.locator('#panel-satellite').screenshot({ path: `${OUT}/sat-360.png` });
  ok(s.order.startsWith('weather,satellite'), `${w} ${scheme}: Satellietbeeld right after Weer (${s.order})`);
  // the image, and the coastline overlay when EUMETSAT delivered it (it is optional)
  ok(s.n >= 1 && s.nat.every(x => x === 800) && /^Satellietbeeld van de Benelux en omgeving om \d\d:\d\d$/.test(s.alt) && (s.n === 1 || s.ovAlt === ''), `image loaded${s.n === 2 ? ' with coastline overlay' : ' (no overlay from EUMETSAT this time)'}, alt "${s.alt}"`);
  ok(/Opname \d\d:\d\d/.test(s.foot) && s.link === 'https://view.eumetsat.int/' && /geleden|min/.test(s.fresh), `footer and freshness: ${s.foot} | ${s.fresh}`);
  ok(Math.abs(s.ratio - 800 / 587) < 0.02 && s.sw <= w, `aspect ratio kept (${s.ratio.toFixed(3)}), no horizontal scroll`);
  const r = await new AxeBuilder({ page: p }).include('#panel-satellite').analyze();
  ok(r.violations.length === 0, `${w} ${scheme} axe satellite: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{
  const [ctx, p] = await open(1440, { lang: 'en', fixtures: false });
  const s = await p.evaluate(() => ({ h: document.querySelector('#panel-satellite h2').textContent, foot: document.querySelector('#panel-satellite .pfoot')?.textContent }));
  ok(s.h === 'Satellite image' && /Taken \d\d:\d\d/.test(s.foot), `English satellite panel: ${s.h} | ${s.foot}`);
  await ctx.close();
}

// ---------- paywall label and search operators (live news)
{
  const [ctx, p, errs] = await open(1440, { fixtures: false, sources: ['nos-algemeen', 'nos-binnenland', 'nos-sport', 'nu-algemeen', 'volkskrant', 'nrc', 'trouw', 'ad', 'nd'] });
  const pw = await p.evaluate(async () => {
    const cat = await (await fetch('api/catalog')).json();
    const paid = new Set(cat.sources.filter(s => s.paywall).map(s => s.name));
    const rows = [...document.querySelectorAll('#stream .item')].map(li => ({ src: li.querySelector('.meta .src').textContent, pw: !!li.querySelector('.meta .pw') }));
    return { paid: paid.size, n: rows.length, wrong: rows.filter(r => r.pw !== paid.has(r.src)).length, labelled: rows.filter(r => r.pw).length };
  });
  ok(pw.paid === 14 && pw.wrong === 0 && pw.labelled > 0, `€ label exactly on paywall sources (${pw.labelled} of ${pw.n} shown items, ${pw.paid} sources)`);
  const ids = await p.evaluate(async () => Object.fromEntries((await (await fetch('api/catalog')).json()).sources.map(s => [s.name, s.id])));
  const search = async q => { await p.fill('#q', q); await p.waitForTimeout(350); return p.evaluate(ids => {
    const fold = s => s.toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '');
    return [...document.querySelectorAll('#stream .item')].map(li => { const name = li.querySelector('.meta .src').textContent;
      return { src: ids[name], name, hay: fold(li.querySelector('.t').textContent + ' ' + (li.querySelector('.sum')?.textContent || '')) }; }); }, ids); };
  let r = await search('bron:nos');
  ok(r.length > 0 && r.every(x => x.src.startsWith('nos')), `bron:nos → only NOS (${r.length})`);
  r = await search('source:nu.nl');
  ok(r.length > 0 && r.every(x => x.src.startsWith('nu-')), `source:nu.nl → only NU.nl (${r.length})`);
  r = await search('bron:ad');
  ok(r.every(x => x.src === 'ad'), `bron:ad does not match Nederlands Dagblad (${r.length}: ${[...new Set(r.map(x => x.src))]})`);
  const all = await search('');
  const word = (() => { const c = {}; for (const x of all) for (const w of x.hay.split(/\s+/)) if (w.length > 5 && /^[a-z]+$/.test(w)) c[w] = (c[w] || 0) + 1; return Object.entries(c).sort((a, b) => b[1] - a[1])[0][0]; })();
  r = await search('-' + word);
  ok(r.length > 0 && r.every(x => !x.hay.includes(word)) && all.some(x => x.hay.includes(word)), `-${word} leaves those out (${all.length} → ${r.length})`);
  r = await search('bron:nos -bron:"nos sport"');
  ok(r.every(x => x.src.startsWith('nos') && x.src !== 'nos-sport'), `bron:nos -bron:"nos sport" (${r.length})`);
  const phrase = all.find(x => x.hay.split(' ').length > 5).hay.split(' ').slice(1, 3).join(' ');
  r = await search(`"${phrase}"`);
  ok(r.length > 0 && r.every(x => x.hay.includes(phrase)), `"${phrase}" → exact phrase (${r.length})`);
  await search(''); // the full list again, with paid articles
  await p.locator('#stream .item .pw').first().screenshot({ path: `${OUT}/pw-label.png` });
  const lab = await p.locator('#stream .item .pw').first().evaluate(e => ({ label: e.getAttribute('aria-label'), text: e.textContent }));
  ok(lab.label === 'Mogelijk achter een betaalmuur' && lab.text === '€', 'label has an accessible name');
  const ax = await new AxeBuilder({ page: p }).include('#stream').analyze();
  ok(ax.violations.length === 0, `axe stream with € labels: ${ax.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  r = await search('qqqzzz');
  const tip = await p.textContent('#stream .empty');
  ok(/Tip: bron:nos, "exacte woorden" en -woord werken ook\./.test(tip), 'no results shows the tip');
  ok(await p.getAttribute('#q', 'title') === 'Ook: bron:nos, "exacte woorden", -woord', 'search box title');
  await p.fill('#q', '');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
