import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, prefs = {} } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', hasTouch: mobile, isMobile: mobile });
  await ctx.addInitScript(([l, pr]) => { // once per tab, so a reload keeps what the page saved
    if (sessionStorage.getItem('t-init')) return;
    sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l, ...pr }));
  }, [lang, prefs]);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(1500);
  return [ctx, p, errs];
}

// ---------- A) trending: every chip gives articles from the visitor's own sources
for (const [name, sources] of [['default sources', null], ['only Tweakers + NU.nl', ['tweakers', 'nu-algemeen', 'nu-binnenland']]]) {
  const [ctx, p, errs] = await open(1440, { prefs: { sources } });
  const req = await p.evaluate(() => performance.getEntriesByType('resource').map(e => e.name).find(n => n.includes('api/trending')));
  ok(req && /sources=/.test(req), `${name}: trending asks for the chosen sources (${req?.split('?')[1]?.slice(0, 60)}…)`);
  const chips = await p.$$eval('#trending .tchip', l => l.map(x => x.textContent));
  let empty = [];
  for (let i = 0; i < chips.length; i++) {
    await p.click(`#trending .tchip >> nth=${i}`); await p.waitForTimeout(250);
    const n = await p.$$eval('#stream .item', l => l.length);
    if (!n) empty.push(chips[i]);
    await p.click(`#trending .tchip >> nth=${i}`); await p.waitForTimeout(150); // off again
  }
  ok(chips.length > 0 && empty.length === 0, `${name}: all ${chips.length} chips give articles (${chips.join(', ')})${empty.length ? ' — empty: ' + empty : ''}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}

// ---------- B/C) Sportagenda
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, { scheme, mobile: w === 360 });
  if (w === 360) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  await p.waitForSelector('#panel-sports .spsec', { timeout: 20000 });
  const s = await p.evaluate(() => [...document.querySelectorAll('#panel-sports .spsec')].map(sec => ({
    h: sec.querySelector('h3').lastChild.textContent, evs: [...sec.querySelectorAll('.spev .sprow')].map(r => r.textContent) })));
  if (w === 1440 && scheme === 'light') { console.log(JSON.stringify(s, null, 1)); await p.locator('#panel-sports').screenshot({ path: `${OUT}/sports.png` }); }
  if (w === 360) await p.locator('#panel-sports').screenshot({ path: `${OUT}/sports-360.png` });
  ok(s.map(x => x.h).join() === 'Formule 1,Wielrennen,Mountainbike,Atletiek,Voetbal', `${w} ${scheme}: sports ${s.map(x => x.h).join(', ')}`);
  const icons = await p.$$eval('#panel-sports .spsec h3 .spico', l => l.map(x => x.textContent + ':' + x.getAttribute('aria-hidden')));
  ok(icons.join() === '🏎️:true,🚴:true,🚵:true,🏃:true,⚽:true', `icons in front of the sports, hidden from screen readers: ${icons}`);
  const road = s.find(x => x.h === 'Wielrennen'), foot = s.find(x => x.h === 'Voetbal'), mtb = s.find(x => x.h === 'Mountainbike');
  // races on now or just ended, then the next three; compared with the API so the check holds on any date
  const apiRoad = (await p.evaluate(() => fetch('api/sports').then(r => r.json()))).events.filter(e => e.sport === 'road');
  ok(road.evs.length === apiRoad.length && apiRoad.filter(e => e.status === 'upcoming').length === 3
    && apiRoad.every((e, i) => road.evs[i].includes(e.name) && (!e.note || road.evs[i].includes(e.note))) && apiRoad.some(e => e.note),
    `road: on now plus next three, with notes: ${apiRoad.map(e => `${e.name} (${e.status})`).join(', ')}`);
  ok(foot.evs.length === 3 && /WK voetbal vrouwen 2027/.test(foot.evs[0]) && /EK voetbal 2028/.test(foot.evs[1]) && /WK voetbal 2030/.test(foot.evs[2]), 'football: WK vrouwen 2027, EK 2028, WK 2030');
  ok(/EK mountainbike 2027.*\(datum voorlopig\)/.test(mtb.evs[0]) && !/voorlopig/.test(mtb.evs[1]), 'mtb: provisional EK marked, WK not');
  // readable countdowns: days up to a month, then months, then years; the exact number of days on hover
  const tags = await p.$$eval('#panel-sports .spev .tag.plain', l => l.map(x => ({ text: x.textContent, title: x.title })));
  const shape = x => /^(vandaag|morgen|over \d+ dagen|over 1 maand|over \d+ maanden|over \d+(,5)? jaar)$/.test(x.text)
    && (/dagen|vandaag|morgen/.test(x.text) ? !x.title : /^over \d+ dagen$/.test(x.title));
  const wk30 = tags.at(-1);
  ok(tags.length > 5 && tags.every(shape) && /jaar$/.test(wk30.text) && tags.some(x => /maanden$/.test(x.text)), `countdowns: ${tags.map(x => x.text).join(' · ')}`);
  await axe(p, '#panel-sports', `${w} ${scheme} sports`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, 'no errors, no horizontal scroll');
  await ctx.close();
}
// existing choice from 1.18: the new sports are switched on once, and switching one off sticks
{
  const [ctx, p, errs] = await open(1440, { prefs: { sports: ['f1', 'athletics'] } });
  const st = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')));
  ok(st.sports.join() === 'f1,athletics,road,football' && st.sportsSeen.length === 5, `new sports added once: ${st.sports} (seen ${st.sportsSeen})`);
  await p.click('#open-settings'); await p.waitForTimeout(300);
  const labels = await p.$$eval('#sports-opts label', l => l.map(x => x.textContent + ':' + x.querySelector('input').checked));
  ok(labels.join() === 'Formule 1:true,Wielrennen:true,Mountainbike:false,Atletiek:true,Voetbal:true', `settings: ${labels}`);
  await p.click('#sports-opts input[data-sport="football"]'); await p.keyboard.press('Escape');
  await p.reload(); await p.waitForSelector('#panel-sports .spsec'); await p.waitForTimeout(500);
  const h3 = await p.$$eval('#panel-sports .spsec h3', l => l.map(x => x.lastChild.textContent));
  ok(h3.join() === 'Formule 1,Wielrennen,Atletiek', `football off stays off after reload: ${h3}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{
  const [ctx, p] = await open(1440, { lang: 'en' });
  await p.waitForSelector('#panel-sports .spsec');
  const h3 = await p.$$eval('#panel-sports .spsec h3', l => l.map(x => x.lastChild.textContent));
  const tent = await p.$eval('#panel-sports', e => e.textContent.includes('(provisional date)'));
  ok(h3.join() === 'Formula 1,Road cycling,Mountain biking,Athletics,Football' && tent, `English: ${h3}`);
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
