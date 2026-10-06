import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const URL = 'http://127.0.0.1:8090/', MOCK = 'http://127.0.0.1:8091/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const ctxFor = async (opts = {}, prefs = { onboarded: true }) => {
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, ...opts });
  await ctx.addInitScript(p => localStorage.setItem('ndb:prefs', JSON.stringify(p)), prefs);
  return ctx;
};
const ready = async p => { await p.waitForSelector('#panel-traffic .jam'); await p.waitForSelector('#panel-outages .out'); await p.waitForSelector('#panel-health .pfoot'); await p.waitForFunction(() => !/…/.test(document.querySelector('#alertbar').innerText)); };

// Top bar (live) + panels
for (const scheme of ['light', 'dark']) {
  const ctx = await ctxFor({ colorScheme: scheme }); const p = await ctx.newPage();
  await p.goto(URL); await ready(p);
  const t = await p.evaluate(() => {
    const q = s => document.querySelector(s), qa = s => [...document.querySelectorAll(s)];
    return {
      nctv: q('#ab-nctv').innerText, nctvHref: q('#ab-nctv').href, nctvDot: getComputedStyle(q('#ab-nctv .d')).backgroundColor,
      knmi: q('#ab-knmi').hidden ? '(hidden)' : q('#ab-knmi').innerText,
      order: qa('.panel').map(x => x.id.replace('panel-', '')),
      tsum: q('#panel-traffic .tsum').textContent, jams: qa('#panel-traffic .pbody > ul.jams .jam').length,
      shields: qa('#panel-traffic .shield').map(x => x.className + ':' + x.textContent).slice(0, 4),
      unknown: qa('#panel-traffic .shield').some(x => x.textContent === '?'),
      outs: qa('#panel-outages .out:not(.inet) .row1').map(r => r.textContent),
      health: qa('#panel-health .plist a').map(a => a.textContent),
      fresh: ['traffic', 'outages', 'health'].map(id => q(`#p-${id}-fresh`).textContent),
      sw: document.documentElement.scrollWidth, iw: innerWidth,
    };
  });
  if (scheme === 'light') console.log(JSON.stringify({ ...t, health: t.health.slice(0, 4) }, null, 1));
  ok(/Dreigingsniveau terrorisme: [1-5] van 5 · (minimaal|beperkt|aanzienlijk|substantieel|kritiek)/.test(t.nctv) && t.nctvHref.startsWith('https://www.nctv.nl/'), `${scheme}: NCTV badge "${t.nctv}"`);
  ok(t.knmi === '(hidden)' || /^KNMI: (waarschuwingen onbekend|code )/.test(t.knmi) || /^KNMI code/.test(t.knmi), `${scheme}: KNMI badge (hidden without warnings) "${t.knmi}"`);
  ok(t.order.join(',') === 'weather,satellite,today,waste,air,pollen,sky,traffic,trains,alarms,nlalert,quakes,energy,fuel,economy,markets,sports,politics,threats,nlthreat,advisories,breaches,ransomware,utilities,outages,ap,health', `${scheme}: default order, Gezondheid below AP (${t.order.join(', ')})`);
  ok(/\d+ files? · (.* · )?\d+ ongeval(len)? · \d+ afsluiting(en)?/.test(t.tsum) && t.jams === Math.min(8, Number(t.tsum.match(/^(\d+) file/)[1])) && !t.unknown, `${scheme}: traffic "${t.tsum}", ${t.jams} jams listed (max 8), no unknown roads`);
  ok(t.shields.every(s => /shield (a:A\d+|n:N\d+|x:)/.test(s)), `${scheme}: road shields ${t.shields.join(' ')}`);
  ok(t.outs.length === 7 && t.outs.every(r => /(werkt normaal|verstoring|storing|status onbekend)/.test(r)), `${scheme}: 7 providers with status`);
  ok(!t.health.some(x => /microplastic|ouderenzorg|bedrijfsartsen|energieverkenning/i.test(x)), `${scheme}: health shows only health alerts (${t.health.length})`);
  ok(t.fresh.every(f => /bijgewerkt/.test(f)), `${scheme}: freshness on the new panels`);
  ok(t.sw <= t.iw, `${scheme}: no horizontal scroll`);
  const axe = await new AxeBuilder({ page: p }).include('#alertbar').include('#panel-traffic').include('#panel-outages').include('#panel-health').analyze();
  ok(axe.violations.length === 0, `${scheme}: axe on top bar + new panels: ${axe.violations.map(v => v.id + ' ' + v.nodes[0].target).join(',') || '0 violations'}`);
  if (scheme === 'light') {
    if (await p.locator('#panel-traffic .more').count()) {
      await p.click('#panel-traffic .more');
      ok(await p.locator('#panel-traffic .pbody > ul.jams .jam').count() > 8, '"Meer files" shows more jams');
    } else console.log('SKIP "Meer files": 8 jams or fewer right now');
    await p.click('#open-status'); await p.waitForFunction(() => /bronnen in orde/.test(document.querySelector('#status-sum').textContent));
    const groups = await p.$$eval('#status-list h3', hs => hs.map(x => x.textContent));
    ok(['Bovenbalk', 'Verkeer', 'Internetstoringen'].every(g => groups.includes(g)), `Bronstatus lists the new sources (${groups.slice(-3).join(', ')})`);
  }
  await ctx.close();
}

// Mobile
{
  const ctx = await ctxFor({ viewport: { width: 360, height: 800 } }); const p = await ctx.newPage();
  await p.goto(URL); await ready(p);
  const m = await p.evaluate(() => ({ nctv: document.querySelector('#ab-nctv').innerText, sw: document.documentElement.scrollWidth, iw: innerWidth }));
  ok(/^Terreurdreiging: \d van 5/.test(m.nctv) && m.sw <= m.iw, `360px: short label "${m.nctv}", no horizontal scroll`);
  await ctx.close();
}

// KNMI code from (mocked) warnings, and NCTV failure → "onbekend"
{
  const ctx = await ctxFor(); const p = await ctx.newPage();
  await p.goto(MOCK); await p.waitForFunction(() => /KNMI code/.test(document.querySelector('#ab-knmi').textContent));
  const k = await p.evaluate(() => ({ t: document.querySelector('#ab-knmi').textContent, dot: getComputedStyle(document.querySelector('#ab-knmi .d')).backgroundColor }));
  ok(/^KNMI code oranje: wind \(Utrecht\)$/.test(k.t) && k.dot === 'rgb(180, 72, 15)', `KNMI badge from warnings: "${k.t}" (${k.dot})`);
  await ctx.close();
  const c2 = await ctxFor(); const p2 = await c2.newPage();
  await p2.route('**/api/alerts', async route => { const r = await route.fetch(); const d = await r.json(); d.nctv = { url: d.nctv.url, error: 'nctv: threat level sentence not found on page' }; await route.fulfill({ response: r, json: d }); });
  await p2.goto(URL); await p2.waitForFunction(() => !/…/.test(document.querySelector('#ab-nctv').textContent));
  ok(/Dreigingsniveau terrorisme: onbekend/.test(await p2.innerText('#ab-nctv')), 'NCTV unreadable → "onbekend", never a guess');
  await c2.close();
}

// Saved panel orders: 1.1 (no new panels), 1.2.0 (Gezondheid above AP) and a deliberate v2 choice
for (const [label, saved, want] of [
  ['1.1 order', { order: ['ap', 'weather', 'threats', 'advisories'], collapsed: { threats: true }, hidden: {} }, 'ap,health,weather,satellite,today,waste,air,pollen,sky,traffic,trains,alarms,nlalert,quakes,energy,fuel,economy,markets,sports,politics,threats,nlthreat,advisories,breaches,ransomware,utilities,outages'],
  ['1.2.0 order', { order: ['weather', 'traffic', 'threats', 'advisories', 'outages', 'health', 'ap'], collapsed: {}, hidden: {} }, 'weather,satellite,today,waste,air,pollen,sky,traffic,trains,alarms,nlalert,quakes,energy,fuel,economy,markets,sports,politics,threats,nlthreat,advisories,breaches,ransomware,utilities,outages,ap,health'],
]) {
  const ctx = await ctxFor({}, { onboarded: true, panels: saved });
  const p = await ctx.newPage(); await p.goto(URL); await p.waitForSelector('#panel-traffic');
  const order = await p.$$eval('.panel', ps => ps.map(x => x.id.replace('panel-', '')));
  ok(order.join(',') === want, `${label} migrated: ${order.join(', ')}`);
  await ctx.close();
}
{
  const ctx = await ctxFor({}, { v: 2, onboarded: true, panels: { order: ['weather', 'health', 'traffic', 'threats', 'advisories', 'outages', 'ap'], collapsed: {}, hidden: {} } });
  const p = await ctx.newPage(); await p.goto(URL); await p.waitForSelector('#panel-traffic');
  const order = await p.$$eval('.panel', ps => ps.map(x => x.id.replace('panel-', '')));
  ok(order.indexOf('health') === order.indexOf('sky') + 1 && order.indexOf('sky') === order.indexOf('pollen') + 1 && order.indexOf('health') < order.indexOf('traffic'), `a deliberate v2 order is left alone (${order.join(', ')})`);
  ok(await p.textContent('#panel-ap h2') === 'Autoriteit Persoonsgegevens', 'AP panel is called "Autoriteit Persoonsgegevens"');
  await ctx.close();
}
await b.close(); console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED'); process.exit(fails ? 1 : 0);
