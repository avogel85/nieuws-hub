import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
// a small grey JPEG-like placeholder as the photo (the real one comes through /api/img)
const photo = 'data:image/svg+xml;utf8,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="96" height="120"><rect width="96" height="120" fill="#999"/></svg>');
const amber = { id: '101', national: true, title: 'Sanne (8)', text: 'Laatst gezien in Utrecht-Oost. Blauwe jas, roze rugzak. Mogelijk in een witte bestelbus, kenteken xx-yy-99.', kind: 'Ontvoerd', url: 'https://www.politie.nl/amberalert', image: photo, sent: new Date().toISOString(), near: true };
const vkaNear = { id: '202', national: false, title: 'Tim (6)', text: 'Laatst gezien bij het Vondelpark.', kind: 'Vermist', url: 'https://www.burgernet.nl/kindvermissing/id=202', area: 'Amsterdam', image: photo, sent: new Date().toISOString(), near: true };
const vkaFar = { ...vkaNear, id: '303', title: 'Anna (7)', area: 'Groningen', near: false };
async function open(w, alerts, { scheme = 'light', lang = 'nl' } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 900 }, colorScheme: scheme, serviceWorkers: 'block' }); // route() does not see service-worker requests
  await ctx.addInitScript(l => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l })), lang);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.route('**/api/amber*', r => r.fulfill({ json: { enabled: true, fetched_at: new Date().toISOString(), alerts } }));
  await p.goto(URL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(700);
  return [ctx, p, errs];
}
const state = p => p.evaluate(() => {
  const box = document.querySelector('#amber'), r = box.getBoundingClientRect(), main = document.querySelector('main.layout').getBoundingClientRect();
  return { hidden: box.hidden, n: box.querySelectorAll('.amb').length, titles: [...box.querySelectorAll('.amb-title')].map(x => x.textContent),
    kinds: [...box.querySelectorAll('.amb-kind')].map(x => x.textContent), acts: [...box.querySelectorAll('.amb-act')].map(x => x.textContent),
    imgs: [...box.querySelectorAll('.amb-photo')].map(i => i.alt), links: [...box.querySelectorAll('.amb-act a')].map(a => a.href),
    above: r.bottom <= main.top + 1, role: box.getAttribute('role'), sw: document.documentElement.scrollWidth };
});
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, [amber, vkaNear, vkaFar], { scheme });
  const s = await state(p);
  if (w === 1440 && scheme === 'light') { console.log(JSON.stringify(s, null, 1)); await p.locator('#amber').screenshot({ path: `${OUT}/amber.png` }); }
  if (w === 360) await p.locator('#amber').screenshot({ path: `${OUT}/amber-360.png` });
  ok(!s.hidden && s.n === 2 && s.titles.join() === 'Sanne (8),Tim (6)' && s.above && s.role === 'alert', `${w} ${scheme}: banner above the content with the AMBER Alert and the nearby Vermist Kind Alert (not Groningen)`);
  ok(/^AMBER Alert · Ontvoerd$/.test(s.kinds[0]) && /^Vermist Kind Alert · Vermist · omgeving Amsterdam$/.test(s.kinds[1]), `kinds: ${s.kinds.join(' | ')}`);
  ok(s.acts.every(a => /Heb je informatie\? Bel direct 112\./.test(a)) && s.links[0] === 'https://www.politie.nl/amberalert' && s.imgs[0] === 'Foto van Sanne (8)', 'call 112, more-information link, photo with alt text');
  ok(s.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors ${errs.join('|')}`);
  const r = await new AxeBuilder({ page: p }).include('#amber').analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  if (w === 1440 && scheme === 'light') {
    await p.click('#amber .amb >> nth=0 >> .linkbtn'); await p.waitForTimeout(150);
    const s2 = await state(p);
    ok(s2.n === 1 && s2.titles[0] === 'Tim (6)', '"Verbergen" hides that alert for this session');
    await p.reload(); await p.waitForSelector('#amber .amb', { timeout: 20000 });
    const s3 = await state(p);
    ok(s3.n === 1 && s3.titles[0] === 'Tim (6)', `still hidden after a reload (same session): ${s3.titles.join()}`);
  }
  await ctx.close();
}
{
  const [ctx, p] = await open(1440, []);
  ok((await state(p)).hidden, 'no active alert: no banner');
  await ctx.close();
  const [c2, p2] = await open(1440, [vkaFar]);
  ok((await state(p2)).hidden, 'only a Vermist Kind Alert elsewhere: no banner');
  await c2.close();
  const [c3, p3] = await open(1440, [amber], { lang: 'en' });
  const e = await state(p3);
  ok(e.kinds[0] === 'AMBER Alert · Abducted' && /Any information\? Call 112 immediately\./.test(e.acts[0]) && e.titles[0] === 'Sanne (8)', `English: ${e.kinds[0]} | ${e.acts[0]}`);
  await c3.close();
  const [c4, p4] = await open(390, [amber], {});
  await p4.evaluate(() => document.querySelector('#mv-panels').click()); await p4.waitForTimeout(200);
  ok(!(await state(p4)).hidden, 'phone: the banner stays visible in the panels view');
  await c4.close();
}
// the real proxy: the photo from Burgernet's test feed is served by this server
{
  const api = await (await fetch(URL + 'api/amber?lat=52.3791&lon=4.9003')).json();
  const withImg = (api.alerts || []).find(a => a.image);
  if (withImg) {
    const r = await fetch(URL + withImg.image);
    ok(r.ok && /^image\//.test(r.headers.get('content-type')), `photo via /api/img: ${r.status} ${r.headers.get('content-type')}`);
  } else ok(true, 'test feed in its "closed" phase: no photo to check now');
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
