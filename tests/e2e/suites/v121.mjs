// 1.21: the news sites' own icons instead of coloured dots, and data saver.
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, prefs = {}, init = null, route = null } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', isMobile: mobile, hasTouch: mobile });
  await ctx.addInitScript(([l, pr]) => { // once per tab, so a reload keeps what the page saved
    if (sessionStorage.getItem('t-init')) return;
    sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l, ...pr }));
  }, [lang, prefs]);
  if (init) await ctx.addInitScript(init);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (route) await route(p);
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(1200);
  return [ctx, p, errs];
}
// the icons are fetched from one minute after the server starts: wait for them (max. 3 minutes)
let cat;
for (let i = 0; i < 36; i++) {
  cat = await (await fetch(BASEURL + 'api/catalog')).json();
  if (cat.sources.filter(s => s.icon).length > cat.sources.length * 0.7) break;
  await new Promise(r => setTimeout(r, 5000));
}
const withIcon = new Set(cat.sources.filter(s => s.icon).map(s => s.id));
ok(withIcon.size > cat.sources.length * 0.7, `most sources have an icon (${withIcon.size} of ${cat.sources.length})`);
const icon = cat.sources.find(s => s.icon);
const res = await fetch(new URL(icon.icon, BASEURL));
const buf = Buffer.from(await res.arrayBuffer());
ok(res.status === 200 && res.headers.get('content-type') === 'image/png' && buf.readUInt32BE(16) === 32 && buf.readUInt32BE(20) === 32, `icon of ${icon.id}: a 32×32 PNG`);
ok((await fetch(BASEURL + 'api/icon?s=does-not-exist')).status === 404, 'only configured sources have an icon');

// ---------- site icons in the news list, the coverage list and the settings
for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, { scheme, mobile: w === 360 });
  await p.waitForFunction(() => [...document.querySelectorAll('#stream .item .src img.sico')].some(i => i.complete && i.naturalWidth));
  const s = await p.evaluate(ids => {
    const rows = [...document.querySelectorAll('#stream .item')].slice(0, 40).map(li => {
      const img = li.querySelector('.src img.sico'), dot = li.querySelector('.src .dot');
      const onScreen = li.getBoundingClientRect().top < innerHeight; // further down: loading="lazy"
      return { name: li.querySelector('.src').textContent, img: !!img, ok: img ? (!onScreen || (img.complete && img.naturalWidth === 32)) : null, dot: !!dot, alt: img?.alt,
        w: img?.getBoundingClientRect().width, src: img?.getAttribute('src') };
    });
    const i = document.querySelector('#stream .item .src img.sico');
    return { rows, bg: i && getComputedStyle(i).backgroundColor, sw: document.documentElement.scrollWidth };
  }, [...withIcon]);
  const ids = Object.fromEntries(cat.sources.map(x => [x.name, x.id]));
  const wrong = s.rows.filter(r => r.img !== withIcon.has(ids[r.name]) || r.img === r.dot);
  ok(wrong.length === 0, `${w} ${scheme}: icon exactly for sources that have one, else the dot (${s.rows.filter(r => r.img).length} icons, ${s.rows.filter(r => r.dot).length} dots)`);
  ok(s.rows.filter(r => r.img).every(r => r.ok && r.alt === '' && r.w === 16 && r.src.startsWith('api/icon?s=')), 'icons loaded from this server, 16 px, decorative (alt="")');
  ok(scheme === 'dark' ? s.bg === 'rgb(236, 238, 242)' : s.bg === 'rgba(0, 0, 0, 0)', `${scheme}: icon background ${s.bg} (a light tile in dark mode)`);
  if (w === 1440) await p.locator('#stream .item >> nth=0').screenshot({ path: `${OUT}/icons-${scheme}.png` });
  if (w === 360) await p.locator('#stream').screenshot({ path: `${OUT}/icons-360.png`, clip: undefined }).catch(() => {});
  await axe(p, '#stream', `${w} ${scheme} news list`);
  ok(errs.length === 0 && s.sw <= w, `${w} ${scheme}: no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
{
  const [ctx, p] = await open(1440);
  await p.click('#open-settings'); await p.waitForTimeout(400);
  const n = await p.$$eval('#src-groups .nm img.sico', l => l.length);
  ok(n > 30, `settings: source list with icons (${n})`);
  await ctx.close();
}
{ // an icon that fails to load falls back to the dot
  const [ctx, p, errs] = await open(1440, { route: p => p.route('**/api/icon*', r => r.fulfill({ status: 404, body: '' })) });
  await p.waitForTimeout(800);
  const s = await p.evaluate(() => { // the icons on the first screen have been tried (further down: loading="lazy")
    const top = [...document.querySelectorAll('#stream .item')].filter(li => li.getBoundingClientRect().top < innerHeight);
    return { n: top.length, imgs: top.filter(li => li.querySelector('.src img')).length, dots: top.filter(li => li.querySelector('.src .dot')).length };
  });
  ok(s.n > 3 && s.imgs === 0 && s.dots === s.n, `broken icons are replaced by dots (${s.dots} of ${s.n} on screen)`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}

// ---------- a slow category stays filled with all sources chosen (1.21.1)
{
  const all = cat.sources.map(s => s.id);
  const [ctx, p, errs] = await open(1440, { prefs: { sources: all, known: all } });
  const req = await p.evaluate(() => performance.getEntriesByType('resource').map(e => e.name).find(n => n.includes('api/news?')));
  await p.click('#chips [data-cat="onderzoek"]'); await p.waitForTimeout(500);
  const n = await p.$$eval('#stream .item', l => l.length);
  ok(/per_source=10/.test(req) && n > 5, `all ${all.length} sources: the Onderzoek category shows ${n} articles`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}

// ---------- data saver
{
  const [ctx, p, errs] = await open(1440, { prefs: { images: true, saver: 'on' } });
  const s = await p.evaluate(() => ({ icons: document.querySelectorAll('#stream .sico').length, thumbs: document.querySelectorAll('#stream .thumb').length,
    meta: document.querySelector('#news-meta').textContent, title: document.querySelector('#news-meta .saver')?.title }));
  ok(s.icons === 0 && s.thumbs === 0, `saver on: no icons, no thumbnails (${s.icons}/${s.thumbs})`);
  ok(/· databesparing$/.test(s.meta) && /Databesparing \(aan\): minder vaak verversen/.test(s.title), `note in the news line: "${s.meta}"`);
  await p.waitForSelector('#panel-satellite .btn', { timeout: 20000 });
  ok(!(await p.$('#panel-satellite .satwrap')) && /Satellietbeeld van \d\d:\d\d laden/.test(await p.textContent('#panel-satellite .btn')), 'satellite image only on request');
  await p.click('#panel-satellite .btn'); await p.waitForSelector('#panel-satellite .satwrap img');
  ok(true, 'tapping loads the satellite image');
  await axe(p, '#panel-satellite', 'satellite with data saver');
  // switching it off in the settings takes effect at once
  await p.click('#open-settings'); await p.waitForTimeout(300);
  ok(await p.isChecked('input[name="saver"][value="on"]'), 'settings show "Aan"');
  await p.click('#saver-seg label:has(input[value="off"])'); await p.keyboard.press('Escape'); await p.waitForTimeout(500);
  const s2 = await p.evaluate(() => ({ icons: document.querySelectorAll('#stream .sico').length, meta: document.querySelector('#news-meta').textContent }));
  ok(s2.icons > 0 && !/databesparing/.test(s2.meta), `saver off: icons back, no note (${s2.icons})`);
  await p.reload(); await p.waitForSelector('#stream .item');
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).saver) === 'off', 'the choice is stored');
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
for (const [name, init, why] of [
  ['browser Save-Data', () => Object.defineProperty(navigator, 'connection', { value: { saveData: true, effectiveType: '4g', addEventListener() {} } }), 'databesparing van je browser'],
  ['2G connection', () => Object.defineProperty(navigator, 'connection', { value: { saveData: false, effectiveType: '2g', addEventListener() {} } }), 'trage verbinding'],
  ['low battery', () => { navigator.getBattery = async () => ({ level: 0.12, charging: false, addEventListener() {} }); }, 'bijna lege batterij'],
  ['charging battery', () => { navigator.getBattery = async () => ({ level: 0.12, charging: true, addEventListener() {} }); }, ''],
]) {
  const [ctx, p, errs] = await open(1440, { init });
  await p.waitForTimeout(500);
  const t = await p.evaluate(() => document.querySelector('#news-meta .saver')?.title || '');
  ok(why ? t.includes(`(${why})`) : !t, `automatic, ${name}: ${why ? 'on (' + why + ')' : 'off'}`);
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{
  const [ctx, p] = await open(1440, { lang: 'en', prefs: { saver: 'on' } });
  const s = await p.evaluate(() => ({ meta: document.querySelector('#news-meta').textContent, title: document.querySelector('#news-meta .saver')?.title }));
  ok(/· data saver$/.test(s.meta) && /Data saver \(on\): refreshing less often/.test(s.title), `English: "${s.meta}"`);
  await p.click('#open-settings'); await p.waitForTimeout(300);
  ok(/Data saver/.test(await p.textContent('#lbl-saver')) && (await p.textContent('#saver-seg')).replace(/\s+/g, ' ').trim() === 'Automatic On Off', 'English settings');
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
