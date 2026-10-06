import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const ctxFor = async (prefs = { onboarded: true }, opts = {}) => {
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, ...opts });
  await ctx.addInitScript(p => { if (!sessionStorage.getItem('init')) { localStorage.setItem('ndb:prefs', JSON.stringify(p)); sessionStorage.setItem('init', '1'); } }, prefs);
  return ctx;
};
const state = p => p.evaluate(() => ({
  extra: document.querySelector('#p-alarms-extra').textContent,
  groups: [...document.querySelectorAll('#panel-alarms .agroup')].map(g => ({ h: g.querySelector('h3').textContent, n: g.querySelectorAll('li').length,
    links: [...g.querySelectorAll('li a')].map(a => a.href) })),
  note: document.querySelector('#panel-alarms .note112')?.textContent,
  fresh: document.querySelector('#p-alarms-fresh').textContent,
}));

{
  const ctx = await ctxFor(); const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#panel-alarms .agroup');
  const order = await p.$$eval('.panel', ps => ps.map(x => x.id.replace('panel-', '')));
  ok(order.indexOf('alarms') === order.indexOf('trains') + 1 && order.indexOf('trains') === order.indexOf('traffic') + 1, `Alarmeringen follows Verkeer and Treinstoringen (${order.join(', ')})`);
  const s = await state(p);
  ok(s.groups.map(g => g.h.replace(/^\W+/, '').split('·')[0].trim()).join(',') === 'Brandweer,Ambulance,Politie,Lifeliner', `four services: ${s.groups.map(g => g.h).join(' | ')}`);
  ok(s.groups.every(g => g.n <= 2), `at most 2 per service (${s.groups.map(g => g.n).join('/')})`);
  ok(s.groups.flatMap(g => g.links).every(u => u.startsWith('https://zwaailicht.nl/')), 'every alert links to its Zwaailicht.nl page');
  // no "call 112" notice (the alerts themselves may contain 112, e.g. a house or unit number)
  const notice = /bel 112|112 bellen|noodgeval/i.test(await p.textContent('#panel-alarms'));
  ok(/^Utrecht · wijzigen$/.test(s.extra) && !s.note && !notice && /bijgewerkt/.test(s.fresh), `default city and freshness shown, no 112 notice in the panel (${s.extra} | note ${!!s.note} | notice ${notice} | ${s.fresh})`);
  // top bar: national counts per service for the last hour
  await p.waitForFunction(() => !/…/.test(document.querySelector('#ab-p2k').textContent));
  const pc = await p.$$eval('#ab-p2k .pc', els => els.map(e => ({ t: e.title, v: e.querySelector('b').textContent, icon: e.querySelector('[aria-hidden]').textContent.trim() })));
  ok(pc.map(x => x.icon).join('') === '🔥🚑🚔🚁⛵' && pc.every(x => /^≥?\d+$/.test(x.v)), `top bar counts: ${pc.map(x => x.icon + x.v).join(' ')}`);
  ok(pc.every(x => /^≥/.test(x.v) === /minstens/.test(x.t)), '"≥" marks exactly the counts that do not yet cover the full hour');
  ok(/^Alarmeringen Den Haag laatste uur:/.test(await p.innerText('#ab-p2k')) && /in Den Haag/.test(await p.getAttribute('#ab-p2k', 'title')), 'top bar counts are for Den Haag');
  const axeTop = await new AxeBuilder({ page: p }).include('#alertbar').analyze();
  ok(axeTop.violations.length === 0, `axe on top bar: ${axeTop.violations.map(v => v.id).join(',') || '0 violations'}`);
  await p.click('#panel-alarms .ptoggle'); // collapse, then the top-bar link must reopen it
  await p.click('#ab-p2k');
  ok(await p.getAttribute('#panel-alarms .ptoggle', 'aria-expanded') === 'true' && await p.evaluate(() => document.activeElement.closest('#panel-alarms') !== null), 'clicking the counts opens the Alarmeringen panel');
  for (const scheme of ['light', 'dark']) {
    await p.emulateMedia({ colorScheme: scheme });
    const axe = await new AxeBuilder({ page: p }).include('#panel-alarms').analyze();
    ok(axe.violations.length === 0, `${scheme}: axe on Alarmeringen: ${axe.violations.map(v => v.id).join(',') || '0 violations'}`);
  }
  // change city via settings
  await p.click('#panel-alarms .pextra .linkbtn');
  ok(await p.evaluate(() => document.activeElement.id === 'alarm-city'), '"wijzigen" opens settings on the city field');
  await p.fill('#alarm-city', 'Den Haag');
  const [req] = await Promise.all([p.waitForRequest(r => r.url().includes('api/alarms?city=')), p.press('#alarm-city', 'Enter')]);
  ok(req.url().endsWith('city=den-haag'), `"Den Haag" becomes slug den-haag (${req.url().split('?')[1]})`);
  await p.waitForFunction(() => /Den Haag/.test(document.querySelector('#alarm-msg').textContent));
  const axeS = await new AxeBuilder({ page: p }).include('#set-alarm').analyze();
  ok(axeS.violations.length === 0, `axe on the city setting: ${axeS.violations.map(v => v.id).join(',') || '0 violations'}`);
  await p.keyboard.press('Escape');
  const dh = await state(p);
  const local = dh.groups.filter(g => !/elders/.test(g.h)).flatMap(g => g.links);
  ok(/^Den Haag/.test(dh.extra) && local.length > 0 && local.every(u => /zwaailicht\.nl\/(den-haag|s-gravenhage|[a-z-]+)\//.test(u)), `panel now shows Den Haag (${local.length} alerts)`);
  await p.reload(); await p.waitForSelector('#panel-alarms .agroup');
  ok(/^Den Haag/.test(await p.textContent('#p-alarms-extra')), 'the chosen city survives a reload');
  // alias and errors
  await p.click('#open-settings'); await p.fill('#alarm-city', "'s-Gravenhage");
  const [req2] = await Promise.all([p.waitForRequest(r => r.url().includes('api/alarms?city=')), p.click('#alarm-save')]);
  ok(req2.url().endsWith('city=den-haag'), "'s-Gravenhage maps to den-haag");
  await p.waitForFunction(() => !/Controleren/.test(document.querySelector('#alarm-msg').textContent));
  await p.fill('#alarm-city', 'Nergenshuizen'); await p.click('#alarm-save');
  await p.waitForFunction(() => /kent geen plaats/.test(document.querySelector('#alarm-msg').textContent));
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).alarmCity === 'den-haag'), 'an unknown city shows a clear message and keeps the previous choice');
  await p.click('#alarm-wx');
  await p.waitForFunction(() => /Utrecht/.test(document.querySelector('#alarm-msg').textContent));
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).alarmCity === 'utrecht'), '"Zelfde als weerlocatie" uses the weather place (Utrecht)');
  await p.click('#alarm-default');
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).alarmCity === null), '"Standaardplaats" returns to the server default');
  await ctx.close();
}
{ // existing user (order saved by 1.2.1): new panel lands after Verkeer
  const ctx = await ctxFor({ v: 2, onboarded: true, panels: { order: ['weather', 'traffic', 'threats', 'advisories', 'outages', 'ap', 'health'], collapsed: {}, hidden: {} } });
  const p = await ctx.newPage(); await p.goto(URL); await p.waitForSelector('#panel-alarms');
  const order = await p.$$eval('.panel', ps => ps.map(x => x.id.replace('panel-', '')));
  ok(order.join(',') === 'weather,satellite,today,waste,air,pollen,sky,traffic,trains,alarms,nlalert,quakes,energy,fuel,economy,markets,sports,politics,threats,nlthreat,advisories,breaches,ransomware,utilities,outages,ap,health', `saved 1.2.1 order gets Alarmeringen after Verkeer (${order.join(', ')})`);
  await ctx.close();
}
{
  const ctx = await ctxFor({ onboarded: true }, { viewport: { width: 360, height: 800 } }); const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#panel-alarms .agroup');
  ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth), '360px: no horizontal scroll');
  await ctx.close();
}
await b.close(); console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED'); process.exit(fails ? 1 : 0);
