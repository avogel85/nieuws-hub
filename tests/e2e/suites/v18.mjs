import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
async function open(w, scheme, prefs = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme });
  await ctx.addInitScript(v => { try { if (!sessionStorage.getItem('seeded')) { localStorage.setItem('ndb:prefs', v); sessionStorage.setItem('seeded', '1'); } } catch {} },
    JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(URL);
  await Promise.all(['#panel-utilities .grid-op', '#panel-quakes .eline', '#panel-pollen .eline', '#panel-outages .inet'].map(s => p.waitForSelector(s)));
  await p.waitForTimeout(600);
  return [ctx, p, errs];
}
const ut = await (await fetch(URL + 'api/utilities')).json();
const qk = await (await fetch(URL + 'api/quakes')).json();
const out = await (await fetch(URL + 'api/outages')).json();

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, scheme);
  const t = await p.evaluate(() => ({
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    heads: ['pollen', 'utilities', 'quakes'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    pollen: document.querySelector('#panel-pollen .eline')?.textContent, pollenRows: document.querySelectorAll('#panel-pollen .pol li:not(.hd)').length,
    pollenX: document.querySelector('#p-pollen-extra')?.textContent,
    ops: [...document.querySelectorAll('#panel-utilities .grid-op')].map(g => ({ name: g.querySelector('.row1 a')?.textContent, href: g.querySelector('.row1 a')?.href,
      st: g.querySelector('.st2')?.textContent, items: g.querySelectorAll('li').length })),
    utNotes: [...document.querySelectorAll('#panel-utilities .pnote, #panel-utilities .pfoot')].map(x => x.textContent).join(' | '),
    qSum: document.querySelector('#panel-quakes .wsec')?.querySelector('.eline')?.textContent,
    qItems: [...document.querySelectorAll('#panel-quakes .qlist li')].map(li => ({ mag: li.querySelector('.mag')?.textContent, href: li.querySelector('a')?.href, induced: !!li.querySelector('.tag') })),
    inet: document.querySelector('#panel-outages .inet .row1')?.textContent, inetItems: document.querySelectorAll('#panel-outages .inet li').length,
    outOrder: [...document.querySelectorAll('#panel-outages .out .row1 a')].map(x => x.textContent),
    outFoot: document.querySelector('#panel-outages .pfoot')?.textContent,
    sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t, null, 1));
  const i = n => t.order.indexOf(n);
  ok(i('pollen') === i('air') + 1 && i('quakes') === i('nlalert') + 1 && i('utilities') === i('ransomware') + 1 && i('outages') === i('utilities') + 1, `${w} ${scheme}: panel positions`);
  ok(t.heads.join() === 'Gezondheid,Kritieke infrastructuur,Aardbevingen en natuurrampen', `panel names: ${t.heads}`);
  ok(/^Vandaag: /.test(t.pollen || '') && /Utrecht/.test(t.pollenX || ''), `hooikoorts: "${t.pollen}", place follows the air/weather location`);
  ok(t.ops.length === 2 && t.ops[0].name === 'Liander' && t.ops[1].name === 'Stedin' && t.ops.every(o => /liander\.nl|stedin\.net/.test(o.href)), `grid operators: ${t.ops.map(o => `${o.name} (${o.st})`).join(', ')}`);
  ok(t.ops[0].items === Math.min(4, ut.operators[0].active.length) && t.ops[1].items === Math.min(4, ut.operators[1].active.length), 'up to 4 active outages per operator');
  ok(!/Enexis/.test(t.utNotes) && !/Drinkwater/.test(t.utNotes) && /0800-9009/.test(t.utNotes), 'only the 0800-9009 note; no Enexis or drinking-water note');
  const recent = qk.quakes.filter(q => Date.now() - new Date(q.time) <= 31 * 864e5);
  ok(recent.length ? new RegExp(`^${recent.length} bevingen? in de afgelopen 14 dagen · sterkste: M`).test(t.qSum || '') : !t.qSum, `quakes summary (14 days): ${t.qSum}`);
  ok(t.qItems.length === Math.min(6, recent.length) && t.qItems.every(q => /^M(\d|\?)/.test(q.mag) && q.href.startsWith('https://www.knmi.nl/nederland-nu/seismologie/aardbevingen/')), 'quakes: magnitude + KNMI link each');
  ok(t.qItems.filter(q => q.induced).length === recent.slice(0, 6).filter(q => q.induced).length, 'induced quakes are tagged');
  const evs = (out.internet.entities || []).reduce((n, e) => n + e.events.length, 0);
  ok(/^Internet in Nederland/.test(t.inet || '') && t.inetItems === Math.min(4, evs), `internet row: "${t.inet}" with ${t.inetItems} events`);
  ok(!/nu geen verstoring|geen verstoringen \(7 dagen\)/.test(t.inet) || !evs || !/geen verstoringen \(7 dagen\)/.test(t.inet), 'status text does not claim a clean week while listing events');
  ok(t.outOrder.join() === 'Internet in Nederland,Akamai,AWS,Cloudflare,Microsoft Azure,Microsoft 365,Google Cloud,STACKIT', `Storingen order: ${t.outOrder.join(', ')}`);
  ok(/IODA/.test(t.outFoot) && !/ontbreken daarom/.test(t.outFoot) && !/stroom en gas/.test(t.outFoot), 'Storingen footer names IODA, without the stroom-en-gas note');
  ok(t.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors ${errs.join('|')}`);
  const r = await new AxeBuilder({ page: p }).include(['#panel-pollen', '#panel-utilities', '#panel-quakes', '#panel-outages']).analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await ctx.close();
}

// Pollen follows an own air/pollen place (Den Haag)
{
  const [ctx, p] = await open(1280, 'light');
  await p.click('#p-pollen-extra .linkbtn'); await p.waitForSelector('#settings[open]');
  ok(await p.evaluate(() => document.activeElement?.id) === 'air-search' && /hooikoorts/.test(await p.textContent('#set-air-h')), '"wijzigen" opens the shared air/pollen place');
  await p.fill('#air-search', 'Den Haag'); await p.press('#air-search', 'Enter');
  await p.waitForFunction(() => /Den Haag/.test(document.querySelector('#air-msg').textContent));
  await p.click('#settings-close');
  // the new place, with its forecast (or a clear note when Open-Meteo rate-limits the test machine)
  await p.waitForFunction(() => /Den Haag/.test(document.querySelector('#p-pollen-extra').textContent)
    && document.querySelector('#panel-pollen .eline, #panel-pollen .pnote'), null, { timeout: 60000 });
  ok(true, 'pollen reloads for Den Haag');
  await ctx.close();
}

// Overview
{
  const [ctx, p] = await open(1440, 'light');
  await p.keyboard.press('v'); await p.waitForSelector('#dc-utilities'); await p.waitForTimeout(400);
  const d = await p.evaluate(() => ({ ut: document.querySelector('#dc-utilities')?.parentElement.textContent, air: document.querySelector('#dc-air')?.parentElement.textContent,
    quakes: !!document.querySelector('#dc-quakes') }));
  ok(/stroom- en \d+ gasstoringen bij Liander en Stedin/.test(d.ut || ''), `overview: ${d.ut?.slice(0, 90)}`);
  ok(/Hooikoorts: /.test(d.air || ''), 'overview: hay fever line in the air card');
  ok(d.quakes === qk.quakes.some(q => Date.now() - new Date(q.time) < 7 * 864e5), 'overview: earthquake card only with a quake in the last 7 days');
  await ctx.close();
}

// English
{
  const [ctx, p] = await open(1440, 'light', { lang: 'en' });
  const e = await p.evaluate(() => ({ heads: ['pollen', 'utilities', 'quakes'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    ut: document.querySelector('#panel-utilities .pbody').innerText, q: document.querySelector('#panel-quakes .pbody').innerText, inet: document.querySelector('#panel-outages .inet').innerText }));
  ok(e.heads.join() === 'Health,Critical infrastructure,Earthquakes and natural disasters', `English names: ${e.heads}`);
  ok(/electricity/.test(e.ut) && !/Drinking water/.test(e.ut) && /earthquake/.test(e.q) && /Internet in the Netherlands/.test(e.inet), 'English texts');
  await ctx.close();
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
