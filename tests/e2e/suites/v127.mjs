// 1.27: the bar above the panels (Ga naar paneel, Alles inklappen) and the summary line of a collapsed panel.
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
async function open(w, { scheme = 'light', lang = 'nl', mobile = false, prefs = {} } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 900 }, colorScheme: scheme, serviceWorkers: 'block', isMobile: mobile, hasTouch: mobile });
  await ctx.addInitScript(([l, x]) => { // once per tab, so a reload keeps what the page saved
    if (sessionStorage.getItem('t-init')) return;
    sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l, ...x }));
  }, [lang, prefs]);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForSelector('#pbar', { state: 'attached' }); await p.waitForTimeout(2500); // let the panels load
  if (mobile) { await p.click('#mv-panels'); await p.waitForTimeout(300); }
  return [ctx, p, errs];
}
const inView = (p, sel) => p.evaluate(s => { const r = document.querySelector(s).getBoundingClientRect(); const hdr = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--hdr')); return { top: Math.round(r.top), hdr, vh: innerHeight }; }, sel);

for (const [w, scheme, mobile] of [[1440, 'light', false], [1440, 'dark', false], [900, 'light', false], [360, 'light', true]]) {
  const tag = `${w} ${scheme}`;
  const [ctx, p, errs] = await open(w, { scheme, mobile });
  const bar = await p.evaluate(() => ({ jump: document.querySelector('#pb-jump').textContent, all: document.querySelector('#pb-all').textContent, first: document.querySelector('#side').firstElementChild.id,
    toPanels: getComputedStyle(document.querySelector('#to-panels')).display }));
  ok(bar.first === 'pbar' && /Ga naar paneel/.test(bar.jump) && /Alles inklappen/.test(bar.all), `${tag}: bar on top of the panels (${bar.jump.trim()} | ${bar.all.trim()})`);
  ok(w === 900 ? bar.toPanels !== 'none' : bar.toPanels === 'none', `${tag}: "Panelen ↓" in the news bar only on a tablet (${bar.toPanels})`);
  if (w === 900) {
    await p.click('#to-panels'); await p.waitForTimeout(600);
    const v = await inView(p, '#pbar');
    ok(v.top >= 0 && v.top < v.vh / 2 && await p.evaluate(() => document.activeElement?.id) === 'pb-jump', `tablet: Panelen ↓ scrolls to the bar and focuses "Ga naar paneel" (top ${v.top})`);
  }
  // sticky: scroll far down, the bar stays just below the header
  await p.evaluate(() => { const s = document.querySelector('#side'); scrollTo(0, s.getBoundingClientRect().top + scrollY + 3000); }); await p.waitForTimeout(300);
  const st = await inView(p, '#pbar');
  ok(Math.abs(st.top - st.hdr) <= 2, `${tag}: bar sticks below the header while scrolling (top ${st.top}, header ${st.hdr})`);
  // jump: search, Enter, the panel lands below the bar with focus and a highlight
  await p.click('#pb-jump');
  ok(!(await p.$eval('#pb-pop', e => e.hidden)) && await p.evaluate(() => document.activeElement?.id) === 'pb-q', `${tag}: the list opens with focus in the search field`);
  const n = await p.$$eval('#pb-list button', l => l.length), order = await p.$$eval('#side .panel', l => l.length);
  ok(n === order && n > 20, `${tag}: all ${n} shown panels in the list`);
  const abc = await p.$$eval('#pb-list button', l => l.map(x => x.textContent));
  ok(abc.every((x, i) => !i || abc[i - 1].localeCompare(x, 'nl', { sensitivity: 'base' }) <= 0) && abc[0] === 'Aardbevingen en natuurrampen' && abc.at(-1) === 'Weer', `${tag}: sorted A to Z (${abc[0]} … ${abc.at(-1)})`);
  if (w === 1440 && scheme === 'light') { await p.screenshot({ path: `${OUT}/panelbar-open.png`, clip: { x: 860, y: 0, width: 580, height: 700 } }); }
  await axe(p, '#pbar', `${tag} bar with open list`);
  await p.keyboard.type('ener');
  const names = await p.$$eval('#pb-list button', l => l.map(x => x.textContent));
  ok(names.join() === 'Energieprijzen', `${tag}: "ener" finds ${names.join(', ')}`);
  await p.keyboard.press('Enter'); await p.waitForTimeout(900);
  const j = await p.evaluate(() => { const el = document.querySelector('#panel-energy'), r = el.getBoundingClientRect(), bar = document.querySelector('#pbar').getBoundingClientRect();
    return { top: Math.round(r.top), barBottom: Math.round(bar.bottom), flash: el.classList.contains('flash'), focus: document.activeElement?.closest('section')?.id, closed: document.querySelector('#pb-pop').hidden }; });
  ok(j.closed && j.focus === 'panel-energy' && j.flash, `${tag}: Enter jumps to Energieprijzen, focus on its heading, highlighted`);
  ok(j.top >= j.barBottom - 2 && j.top < j.barBottom + 60, `${tag}: the panel is just below the bar (panel ${j.top}, bar ends ${j.barBottom})`);
  // a collapsed panel opens when you jump to it; arrows and Escape
  await p.click('#panel-fuel .ptoggle');
  ok(await p.$eval('#panel-fuel', e => e.classList.contains('collapsed')), `${tag}: Brandstofprijzen collapsed by hand`);
  await p.click('#pb-jump'); await p.keyboard.type('brand'); await p.keyboard.press('ArrowDown');
  ok(await p.evaluate(() => document.activeElement?.dataset.id) === 'fuel', `${tag}: arrow down moves into the list`);
  await p.keyboard.press('Enter'); await p.waitForTimeout(700);
  ok(!(await p.$eval('#panel-fuel', e => e.classList.contains('collapsed'))) && !(await p.$eval('#p-fuel-body', e => e.hidden)), `${tag}: jumping to a collapsed panel opens it`);
  await p.click('#pb-jump'); await p.keyboard.press('Escape');
  ok(await p.$eval('#pb-pop', e => e.hidden) && await p.evaluate(() => document.activeElement?.id) === 'pb-jump', `${tag}: Escape closes the list and returns focus`);
  await p.click('#pb-jump'); await p.mouse.click(5, 300);
  ok(await p.$eval('#pb-pop', e => e.hidden), `${tag}: a click elsewhere closes the list`);
  // collapse all: one line per panel with a summary
  await p.click('#pb-all'); await p.waitForTimeout(300);
  const c = await p.evaluate(() => ({ all: [...document.querySelectorAll('#side .panel')].every(x => x.classList.contains('collapsed') && x.querySelector('.pbody').hidden && x.querySelector('.ptoggle').getAttribute('aria-expanded') === 'false'),
    btn: document.querySelector('#pb-all').textContent, h: (ps => ps.at(-1).getBoundingClientRect().bottom - ps[0].getBoundingClientRect().top)([...document.querySelectorAll('#side .panel')]), n: document.querySelectorAll('#side .panel').length,
    sums: Object.fromEntries([...document.querySelectorAll('#side .panel')].map(x => [x.id.replace('panel-', ''), x.querySelector('.psum').textContent.replace(/^: /, '')])),
    empty: [...document.querySelectorAll('#side .panel')].filter(x => !x.querySelector('.psum').textContent.trim()).map(x => x.id),
    over: [...document.querySelectorAll('#side .psum')].some(x => x.getBoundingClientRect().right > innerWidth + 1) }));
  if (w === 1440 && scheme === 'light') { console.log(JSON.stringify(c.sums, null, 1)); await p.evaluate(() => scrollTo(0, 0)); await p.locator('#side').screenshot({ path: `${OUT}/panels-collapsed.png` }); }
  if (w === 360) await p.locator('#side').screenshot({ path: `${OUT}/panels-collapsed-360.png` });
  ok(c.all && /Alles uitklappen/.test(c.btn), `${tag}: all ${c.n} panels collapsed, button now "${c.btn.trim()}"`);
  ok(c.h / c.n < (w === 900 ? 110 : 60), `${tag}: about one line per panel (${Math.round(c.h / c.n)} px each)`);
  ok(/°/.test(c.sums.weather) && /^\d+ · /.test(c.sums.air || '0 · ') && /storing|werkt/.test(c.sums.outages) && /nu €\s?\d|goedkoopst|bijgewerkt/.test(c.sums.energy), `${tag}: summaries: weer "${c.sums.weather}", energie "${c.sums.energy}"`);
  ok(c.empty.length <= 2 && !c.over, `${tag}: every collapsed panel has a summary or its freshness (${c.empty.join(', ') || 'all'}), nothing sticks out`);
  await axe(p, '#side', `${tag} collapsed panels`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, `${tag}: no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
{ // remembered after a reload, expand all, the g key, the summary of one collapsed panel
  const [ctx, p] = await open(1440);
  await p.click('#pb-all'); await p.waitForTimeout(200);
  await p.reload(); await p.waitForSelector('#pbar'); await p.waitForTimeout(800);
  ok(await p.evaluate(() => [...document.querySelectorAll('#side .panel')].every(x => x.classList.contains('collapsed'))) && /Alles uitklappen/.test(await p.textContent('#pb-all')), 'collapsed panels are remembered after a reload');
  await p.click('#pb-all'); await p.waitForTimeout(200);
  ok(await p.evaluate(() => [...document.querySelectorAll('#side .panel')].every(x => !x.classList.contains('collapsed') && !x.querySelector('.pbody').hidden)), 'Alles uitklappen opens every panel');
  ok(await p.evaluate(() => getComputedStyle(document.querySelector('#panel-weather .psum')).display) === 'none', 'an open panel shows no summary line');
  await p.click('#panel-weather .ptoggle'); await p.waitForTimeout(200);
  const one = await p.evaluate(() => ({ sum: document.querySelector('#panel-weather .psum').innerText, name: document.querySelector('#p-weather-h').textContent, fresh: getComputedStyle(document.querySelector('#p-weather-fresh')).display }));
  ok(/°/.test(one.sum) && /^Weer: \d/.test(one.name) && one.fresh === 'none', `one collapsed panel: "${one.sum}", heading read as "${one.name.slice(0, 40)}"`);
  await p.evaluate(() => document.activeElement?.blur()); await p.keyboard.press('g');
  ok(await p.evaluate(() => document.activeElement?.id) === 'pb-q', 'the g key opens the panel search');
  await p.keyboard.type('weer'); await p.keyboard.press('Enter'); await p.waitForTimeout(500);
  ok(!(await p.$eval('#panel-weather', e => e.classList.contains('collapsed'))), 'g, "weer", Enter opens the collapsed Weer panel');
  await p.click('#pb-jump'); await p.keyboard.type('xyz');
  ok(/Geen paneel gevonden/.test(await p.textContent('#pb-list')), 'no match: a short note');
  await ctx.close();
}
{ // the g key from the news view on a phone-sized window switches to the panels
  const [ctx, p] = await open(390);
  await p.evaluate(() => document.activeElement?.blur()); await p.keyboard.press('g'); await p.waitForTimeout(300);
  ok(await p.evaluate(() => document.documentElement.dataset.mview) === 'panels' && await p.evaluate(() => document.activeElement?.id) === 'pb-q', 'narrow window: g switches to the panels view and opens the search');
  await ctx.close();
}
{ // English
  const [ctx, p] = await open(1440, { lang: 'en' });
  ok(/Go to panel/.test(await p.textContent('#pb-jump')) && /Collapse all/.test(await p.textContent('#pb-all')), 'English bar');
  await p.click('#pb-all'); await p.waitForTimeout(200);
  const s = await p.textContent('#panel-outages .psum');
  ok(/working normally|disruption|degraded|outage|updated/i.test(s), `English summary: ${s}`);
  ok(/Go to a panel/.test(await p.evaluate(() => document.querySelector('#keys').textContent)), 'shortcut listed in the ? overview');
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
