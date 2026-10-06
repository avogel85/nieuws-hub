import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const NEW = ['energy', 'air', 'trains', 'politics'];
async function open(w, scheme, prefs = {}, url = URL, extra = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, ...extra });
  await ctx.addInitScript(v => { try { if (!sessionStorage.getItem('seeded')) { localStorage.setItem('ndb:prefs', v); sessionStorage.setItem('seeded', '1'); } } catch {} },
    JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage();
  const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(url);
  return [ctx, p, errs];
}
const ready = p => Promise.all([p.waitForSelector('#panel-politics .pgrp'), p.waitForSelector('#panel-energy .enow'), p.waitForSelector('#panel-air .airnow'), p.waitForSelector('#panel-trains .pnote')]);

// C) top bar order + A) panels, light/dark/phone
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, scheme);
  await ready(p); await p.waitForTimeout(500);
  const t = await p.evaluate(() => ({
    bar: [...document.querySelectorAll('#alertbar .ab')].filter(a => a.getBoundingClientRect().width > 0).map(a => a.id),
    knmiShown: document.querySelector('#ab-knmi').getBoundingClientRect().width > 0,
    knmiText: document.querySelector('#ab-knmi').textContent,
    nlActive: /NL-Alert: (in |actief)/.test(document.querySelector('#ab-nl').textContent) && document.querySelector('#ab-nl').getBoundingClientRect().width > 0,
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    heads: Object.fromEntries(['energy', 'air', 'trains', 'politics'].map(id => [id, document.querySelector(`#panel-${id} h2`).textContent.trim()])),
    price: document.querySelector('#panel-energy .enow .big')?.textContent, bars: document.querySelectorAll('#panel-energy .echart rect').length,
    nowBar: document.querySelectorAll('#panel-energy .echart rect.now').length, cheap: [...document.querySelectorAll('#panel-energy .eline')].map(x => x.textContent).find(x => /Goedkoopste/.test(x)),
    lki: document.querySelector('#panel-air .lki')?.textContent, station: document.querySelector('#panel-air .aadv a')?.href,
    comps: document.querySelectorAll('#panel-air .airc li').length, airx: document.querySelector('#p-air-extra')?.textContent,
    trains: document.querySelector('#panel-trains .pnote')?.textContent,
    polH: [...document.querySelectorAll('#panel-politics h3')].map(x => x.textContent),
    polLinks: [...document.querySelectorAll('#panel-politics .plist a')].map(a => a.href),
    votes: [...document.querySelectorAll('#panel-politics .tag')].map(x => x.textContent),
    fresh: ['energy', 'air', 'politics'].map(id => document.querySelector(`#p-${id}-fresh`)?.textContent),
    sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t, null, 1));
  ok(t.bar.join() === [t.knmiShown && 'ab-knmi', 'ab-p2k', t.nlActive && 'ab-nl', 'ab-nctv'].filter(Boolean).join() && !(t.knmiShown && /geen waarschuwingen/.test(t.knmiText)), `${w} ${scheme}: top bar KNMI → Alarmeringen → (NL-Alert only when active) → Dreigingsniveau (${t.bar})`);
  ok(t.order.join() === 'weather,satellite,today,waste,air,pollen,sky,traffic,trains,alarms,nlalert,quakes,energy,fuel,economy,markets,sports,politics,threats,nlthreat,advisories,breaches,ransomware,utilities,outages,ap,health', `${w} ${scheme}: panel order ${t.order.join(',')}`);
  ok(t.heads.energy === 'Energieprijzen' && t.heads.air === 'Luchtkwaliteit' && t.heads.trains === 'Treinstoringen' && t.heads.politics === 'Politiek vandaag', 'panel names');
  ok(/^€\s?-?\d+,\d\d$/.test(t.price) && t.bars >= 23 && t.bars <= 50 && t.nowBar === 1, `energy: now ${t.price}, ${t.bars} hourly bars, current hour marked`);
  ok(/Goedkoopste 3 uur: (morgen )?\d\d:\d\d–\d\d:\d\d/.test(t.cheap || ''), `energy: ${t.cheap}`);
  ok(/^\d{1,2}$/.test(t.lki) && /luchtmeetnet\.nl\/meetpunten\?station=NL[0-9A-Z]+$/.test(t.station) && t.comps >= 1 && /Utrecht/.test(t.airx), `air: LKI ${t.lki}, ${t.comps} pollutants, ${t.station}`);
  ok(/gratis sleutel van de NS API/.test(t.trains), 'trains: explains the NS key');
  ok(/^(Vandaag in de Kamer|Volgende vergaderdag: .+)$/.test(t.polH[0]) && t.polH.includes('Laatste stemmingen'), `politics: ${t.polH.join(' | ')}`);
  ok(t.polLinks.length > 2 && t.polLinks.every(h => h.startsWith('https://www.tweedekamer.nl/')), 'politics: every item links to tweedekamer.nl');
  ok(t.votes.length > 0 && t.votes.every(v => /^(aangenomen|verworpen|geannuleerd)$/.test(v)), `votes: ${t.votes.join(',')}`);
  ok(t.fresh.every(f => /bijgewerkt/.test(f || '')), 'freshness lines');
  ok(t.sw <= w, `${w}: no horizontal scroll`);
  const r = await new AxeBuilder({ page: p }).include('#alertbar').include(NEW.map(id => '#panel-' + id)).analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe on top bar + new panels: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  ok(errs.length === 0, `no page errors ${errs.join(' | ')}`);
  await ctx.close();
}

// Air location: own place, then back to the weather location
{
  const [ctx, p] = await open(1280, 'light');
  await ready(p);
  await p.click('#p-air-extra .linkbtn'); await p.waitForSelector('#settings[open]');
  ok(await p.evaluate(() => document.activeElement?.id) === 'air-search', '"wijzigen" opens settings at the air section');
  ok(/zelfde als je weerlocatie/.test(await p.textContent('#air-now')), 'hint: follows the weather location');
  await p.fill('#air-search', 'Den Haag'); await p.press('#air-search', 'Enter');
  await p.waitForFunction(() => /Den Haag/.test(document.querySelector('#air-msg').textContent));
  await p.click('#settings-close');
  await p.waitForFunction(() => /Den Haag|Gravenhage/.test(document.querySelector('#panel-air .aadv a')?.textContent || ''));
  const st = await p.evaluate(() => ({ name: document.querySelector('#panel-air .aadv a').textContent, x: document.querySelector('#p-air-extra').textContent, pref: JSON.parse(localStorage.getItem('ndb:prefs')).air }));
  ok(st.pref?.name === 'Den Haag' && /Den Haag/.test(st.x) && /Den Haag/.test(st.name), `own place: ${st.name}`);
  const wx = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).weather);
  ok(wx === null, 'weather location unchanged');
  await p.click('#open-settings'); await p.click('#air-wx');
  await p.waitForFunction(() => JSON.parse(localStorage.getItem('ndb:prefs')).air === null);
  await p.click('#settings-close');
  await p.waitForFunction(() => /Utrecht/.test(document.querySelector('#panel-air .aadv a')?.textContent || ''));
  ok(true, 'reset to the weather location (Utrecht again)');
  await ctx.close();
}

// B) Overview (digest): settings, key "v", URL, card → panel
{
  const [ctx, p, errs] = await open(1440, 'light');
  await ready(p); await p.waitForSelector('#stream .item');
  await p.click('#open-settings'); await p.click('#mode-seg label:has-text("Overzicht")'); await p.click('#settings-close');
  await p.waitForSelector('#dgrid .dcard');
  await p.waitForTimeout(600);
  const d = await p.evaluate(() => ({ mode: document.documentElement.dataset.mode, saved: JSON.parse(localStorage.getItem('ndb:prefs')).mode,
    news: getComputedStyle(document.querySelector('#news')).display, side: getComputedStyle(document.querySelector('#side')).display,
    cards: [...document.querySelectorAll('.dcard h3')].map(x => x.textContent), stories: document.querySelectorAll('#dc-news ~ ul li, .dcard.wide li').length,
    date: document.querySelector('#digest-date').textContent }));
  console.log(JSON.stringify(d));
  ok(d.mode === 'digest' && d.saved === 'digest' && d.news === 'none' && d.side === 'none', 'Overzicht via settings: saved, news and panels hidden');
  ok(['Weer →', 'Belangrijkste nieuws →', 'Energieprijzen →', 'Luchtkwaliteit →', 'Politiek vandaag →'].every(c => d.cards.includes(c)) && d.stories >= 3, `cards: ${d.cards.join(', ')}`);
  const r = await new AxeBuilder({ page: p }).include('#digest').analyze();
  ok(r.violations.length === 0, `axe on the overview: ${r.violations.map(v => v.id).join(',') || 0}`);
  await p.screenshot({ path: `${OUT}/v16-digest.png`, fullPage: true });
  await p.click('#dc-energy button');
  await p.waitForTimeout(400);
  const back = await p.evaluate(() => ({ mode: document.documentElement.dataset.mode || 'normal', focus: document.activeElement?.closest('.panel')?.id,
    top: Math.round(document.querySelector('#panel-energy').getBoundingClientRect().top) }));
  ok(back.mode === 'normal' && back.focus === 'panel-energy' && back.top < 300, `card title opens the panel (${JSON.stringify(back)})`);
  await p.keyboard.press('v'); await p.waitForTimeout(200);
  ok(await p.evaluate(() => document.documentElement.dataset.mode) === 'digest', 'key "v" switches to the overview');
  await p.keyboard.press('v');
  ok(await p.evaluate(() => !document.documentElement.dataset.mode), 'key "v" switches back');
  ok(errs.length === 0, 'no page errors');
  await ctx.close();
  for (const w of [360]) {
    const [c2, p2] = await open(w, 'light', { mode: 'digest' });
    await p2.waitForSelector('#dgrid .dcard'); await p2.waitForTimeout(3000);
    ok(await p2.evaluate(() => document.documentElement.scrollWidth) <= w, `${w}: overview without horizontal scroll`);
    await p2.screenshot({ path: `${OUT}/v16-digest-360.png`, fullPage: true });
    await c2.close();
  }
}

// B) Kiosk via URL (this visit only): no settings, columns scroll by themselves, Esc leaves
{
  const [ctx, p, errs] = await open(1920, 'dark', {}, URL + '?mode=kiosk');
  await ready(p); await p.waitForSelector('#stream .item'); await p.waitForTimeout(800);
  const k = await p.evaluate(() => ({ mode: document.documentElement.dataset.mode, saved: JSON.parse(localStorage.getItem('ndb:prefs')).mode || 'normal',
    hidden: ['#open-settings', '.search', '#theme-seg', '#lang-seg', '#refresh'].map(s => getComputedStyle(document.querySelector(s)).display),
    exit: getComputedStyle(document.querySelector('#kiosk-exit')).display, zoom: getComputedStyle(document.body).zoom,
    bodyScroll: ['#news', '#side'].every(sel => { const b = document.querySelector(sel).getBoundingClientRect().bottom; return b <= innerHeight + 2 && b >= innerHeight - 40; }) && getComputedStyle(document.body).overflow === 'hidden',
    news: [document.querySelector('#news').scrollHeight, document.querySelector('#news').clientHeight],
    side: [document.querySelector('#side').scrollHeight, document.querySelector('#side').clientHeight] }));
  console.log(JSON.stringify(k));
  ok(k.mode === 'kiosk' && k.saved === 'normal', '?mode=kiosk applies for this visit without changing the saved mode');
  ok(k.hidden.every(v => v === 'none') && k.exit !== 'none', 'kiosk: settings, search, theme/language and refresh hidden; exit button shown');
  ok(Number(k.zoom) > 1.2, `kiosk: larger (zoom ${k.zoom})`);
  ok(k.bodyScroll && k.news[0] > k.news[1] && k.side[0] > k.side[1], 'kiosk: the page fits the screen; news and panels scroll inside their columns');
  const before = await p.evaluate(() => [document.querySelector('#news').scrollTop, document.querySelector('#side').scrollTop]);
  await p.waitForTimeout(16500); // one auto-advance step (every 15 s)
  const after = await p.evaluate(() => [document.querySelector('#news').scrollTop, document.querySelector('#side').scrollTop]);
  ok(after[0] > before[0] && after[1] > before[1], `kiosk: auto-advance moves both columns (${before} → ${after})`);
  await p.mouse.click(5, 500); await p.waitForTimeout(16500); // user input pauses auto-advance for 60 s
  const paused = await p.evaluate(() => [document.querySelector('#news').scrollTop, document.querySelector('#side').scrollTop]);
  ok(paused[0] === after[0] && paused[1] === after[1], 'kiosk: pauses after user input');
  await p.screenshot({ path: `${OUT}/v16-kiosk-1920.png` });
  const r = await new AxeBuilder({ page: p }).analyze();
  ok(r.violations.length === 0, `axe in kiosk mode: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await p.keyboard.press('Escape');
  ok(await p.evaluate(() => !document.documentElement.dataset.mode && getComputedStyle(document.querySelector('#open-settings')).display !== 'none'), 'Esc leaves kiosk');
  ok(errs.length === 0, 'no page errors');
  await ctx.close();
}

// English: new panels, overview, settings
{
  const [ctx, p] = await open(1440, 'light', { lang: 'en' });
  await ready(p); await p.waitForTimeout(800);
  const e = await p.evaluate(() => ({ heads: ['energy', 'air', 'trains', 'politics'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    energy: document.querySelector('#panel-energy .pbody').innerText, air: document.querySelector('#panel-air .pbody').innerText,
    pol: [...document.querySelectorAll('#panel-politics h3')].map(x => x.textContent), bar: document.querySelector('#alertbar').getAttribute('aria-label') }));
  ok(e.heads.join() === 'Energy prices,Air quality,Train disruptions,Politics today', `English panel names: ${e.heads}`);
  ok(/per kWh/.test(e.energy) && /Cheapest 3 hours/.test(e.energy) && /Good|Moderate|Poor|Bad/.test(e.air) && /Latest votes/.test(e.pol.join()), 'English panel texts');
  ok(/^-?€\d/.test(e.energy.trim()), `English currency format: ${e.energy.trim().slice(0, 60)}`);
  await p.keyboard.press('v'); await p.waitForSelector('#dgrid .dcard'); await p.waitForTimeout(500);
  const dh = await p.evaluate(() => ({ h: document.querySelector('#digest-h').textContent, cards: [...document.querySelectorAll('.dcard h3')].map(x => x.textContent) }));
  ok(dh.h === 'Today at a glance' && dh.cards.includes('Top news →') && dh.cards.includes('Energy prices →'), `English overview: ${dh.cards.join(', ')}`);
  await ctx.close();
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
