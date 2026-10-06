import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
async function open(w, scheme, prefs = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme });
  await ctx.addInitScript(v => { try { if (!sessionStorage.getItem('seeded')) { localStorage.setItem('ndb:prefs', v); sessionStorage.setItem('seeded', '1'); } } catch {} },
    JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(URL);
  await Promise.all(['#panel-economy .etile', '#panel-markets .mtop'].map(s => p.waitForSelector(s)));
  await p.waitForTimeout(500);
  return [ctx, p, errs];
}
const ec = (await (await fetch(URL + 'api/economy')).json()).data;
// live data: the ECB deposit rate is sometimes missing when the ECB API does not answer this network; then the panel
// correctly shows three tiles, and the rate checks are skipped with a note
const hasRate = !!ec.rate;
if (!hasRate) console.log('NOTE: ECB deposit rate not available from this network; rate checks skipped');
const mk = (await (await fetch(URL + 'api/markets')).json()).data;
const cat = await (await fetch(URL + 'api/catalog')).json();

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, scheme);
  const t = await p.evaluate(() => ({
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    heads: ['economy', 'markets'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    tiles: [...document.querySelectorAll('#panel-economy .etile')].map(x => ({ l: x.querySelector('.el').textContent, v: x.querySelector('.ev').textContent, s: x.querySelector('.es')?.textContent, spark: !!x.querySelector('svg.espark[aria-label]') })),
    ecFoot: document.querySelector('#panel-economy .pfoot')?.textContent,
    idx: [...document.querySelectorAll('#panel-markets > .pbody > ul.mkt li, #panel-markets .pbody > .mkt li')].map(li => li.textContent),
    idxN: document.querySelectorAll('#panel-markets .pbody > ul.mkt li').length,
    cols: [...document.querySelectorAll('#panel-markets .mcol')].map(c => ({ h: c.querySelector('h3').textContent, n: [...c.querySelectorAll('.mn')].map(x => x.textContent), cls: [...c.querySelectorAll('.chg')].map(x => x.className) })),
    mkFoot: document.querySelector('#panel-markets .pfoot')?.textContent,
    sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t, null, 1));
  const i = n => t.order.indexOf(n);
  ok(i('economy') === i('fuel') + 1 && i('markets') === i('economy') + 1 && i('sports') === i('markets') + 1 && i('politics') === i('sports') + 1, `${w} ${scheme}: energy, fuel, economy, markets, sports, politics`);
  ok(t.heads.join() === 'Economie in cijfers,Beurs', 'panel names');
  ok(t.tiles.map(x => x.l).join() === (hasRate ? 'Inflatie,Werkloosheid,ECB-depositorente,Euro in dollar' : 'Inflatie,Werkloosheid,Euro in dollar'), `tiles: ${t.tiles.map(x => x.l + ' ' + x.v).join(', ')}${hasRate ? '' : ' (ECB rate unavailable from this network)'}`);
  ok(t.tiles[0].v === ec.inflation.at(-1).value.toLocaleString('nl-NL', { minimumFractionDigits: 1 }) + '%' && /eurozone/.test(t.tiles[0].s) && t.tiles[0].spark && t.tiles[1].spark, 'inflation value, euro-area comparison and trend lines');
  ok(!hasRate || /^sinds \d+ \w+\.? \d{4}$/.test(t.tiles[2].s || ''), hasRate ? `ECB rate since: ${t.tiles[2].s}` : 'ECB rate since: skipped (rate unavailable from this network)');
  ok(/Eurostat/.test(t.ecFoot) && /ECB/.test(t.ecFoot), 'economy footer names Eurostat and ECB');
  ok(t.idxN === mk.indices.length && t.idx[0].startsWith('AEX'), `indices: ${t.idxN}`);
  ok(t.cols.length === 2 && t.cols[0].h === 'Stijgers (AEX)' && t.cols[1].h === 'Dalers (AEX)', 'top 3 columns');
  ok(t.cols[0].n.join() === mk.stocks.slice(0, 3).map(s => s.name).join() && t.cols[1].n.join() === mk.stocks.slice(-3).reverse().map(s => s.name).join(), `risers ${t.cols[0].n} / fallers ${t.cols[1].n}`);
  ok(t.cols[1].cls.every(c => /down|flat/.test(c)) && t.cols[0].cls.every(c => /up|flat/.test(c)), 'risers green, fallers red');
  ok(/vertraging/.test(t.mkFoot) && /AEX/.test(t.mkFoot) && /Yahoo Finance/.test(t.mkFoot) && /persoonlijk gebruik/.test(t.mkFoot), `markets footer: ${t.mkFoot}`);
  ok(t.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors ${errs.join('|')}`);
  const r = await new AxeBuilder({ page: p }).include(['#panel-economy', '#panel-markets']).analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await ctx.close();
}

// Sources and category
{
  const ids = cat.sources.map(s => s.id), onderzoek = cat.categories.find(c => c.id === 'onderzoek');
  ok(ids.includes('the-register') && cat.sources.find(s => s.id === 'the-register').category === 'tech', 'The Register under Tech');
  ok(onderzoek && onderzoek.name === 'Onderzoeksjournalistiek', 'category Onderzoeksjournalistiek');
  ok(['ftm', 'correspondent', 'investico', 'groene', 'lighthouse-reports', 'bellingcat'].every(x => ids.includes(x)) && !ids.includes('pointer'), 'investigative sources (De Correspondent back since 1.20; Pointer, disabled, hidden)');
  ok(cat.presets.some(p => p.id === 'onderzoek'), 'preset Onderzoek');
  ok(cat.refresh.economy === 3600 && cat.refresh.markets === 300, 'refresh keys');
}

// Overview
{
  const [ctx, p] = await open(1440, 'light');
  await p.keyboard.press('v'); await p.waitForSelector('#dc-markets'); await p.waitForTimeout(300);
  const d = await p.evaluate(() => document.querySelector('#dc-markets').parentElement.textContent);
  ok(/Beurs en economie/.test(d) && /AEX/.test(d) && /inflatie \d/.test(d) && (!hasRate || /ECB/.test(d)), `overview: ${d.slice(0, 140)}`);
  await ctx.close();
}

// English
{
  const [ctx, p] = await open(1440, 'light', { lang: 'en' });
  const e = await p.evaluate(() => ({ heads: ['economy', 'markets'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    ec: document.querySelector('#panel-economy .pbody').innerText, mk: document.querySelector('#panel-markets .pbody').innerText }));
  ok(e.heads.join() === 'Economy in figures,Markets', `English names: ${e.heads}`);
  ok(/Inflation/.test(e.ec) && /euro area/.test(e.ec) && (!hasRate || (/ECB deposit rate/.test(e.ec) && /since/.test(e.ec))) && !/Werkloosheid|sinds/.test(e.ec), 'English economy');
  ok(/Risers \(AEX\)/i.test(e.mk) && /Fallers/i.test(e.mk) && /Brent crude/.test(e.mk) && /Gold/.test(e.mk) && /personal use only/.test(e.mk) && /Delayed prices/.test(e.mk), 'English markets');
  await ctx.close();
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
