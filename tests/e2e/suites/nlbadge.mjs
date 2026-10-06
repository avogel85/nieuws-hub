import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const now = Date.now(), iso = ms => new Date(ms).toISOString();
const alert = (id, text, near, extra = {}) => ({ id, text, text_en: 'EN ' + text, start: iso(now - 20 * 60e3), stop: iso(now + 60 * 60e3), near, ...extra });
async function open(w, alerts, lang = 'nl') {
  const ctx = await b.newContext({ viewport: { width: w, height: 900 } });
  await ctx.addInitScript(l => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l })), lang);
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (alerts) await p.route('**/api/nlalert*', r => r.fulfill({ json: { enabled: true, fetched_at: iso(now), alerts, active: alerts.length } }));
  await p.goto(URL); await p.waitForFunction(() => { const a = document.querySelector('#ab-nl'); return a.hidden || !/…/.test(a.textContent); });
  await p.waitForTimeout(200);
  return [ctx, p, errs];
}
// only what is really on screen counts: a hidden badge takes no space
const badge = p => p.evaluate(() => { const a = document.querySelector('#ab-nl'), shown = a.getBoundingClientRect().width > 0;
  return { text: shown ? a.textContent : '(hidden)', title: a.title, dot: getComputedStyle(a.querySelector('.d')).backgroundColor,
  order: [...document.querySelectorAll('#alertbar .ab')].filter(x => x.getBoundingClientRect().width > 0).map(x => x.id).join(',') }; });

for (const [alerts, want, name] of [
  [[alert('a1', 'Brand met veel rook in Kalverdijkje Leeuwarden. Blijf uit de rook! Sluit ramen en deuren.', false)], 'NL-Alert: in Leeuwarden', 'long place → last word'],
  [[alert('a2', 'Gevaarlijke stof vrijgekomen in Bonksel, Asten. Ga naar binnen.', false)], 'NL-Alert: in Asten', 'comma → after comma'],
  [[alert('a3', 'Brand met veel rook in Het Haagje Hoogeveen. Blijf uit de rook!', false)], 'NL-Alert: in Het Haagje Hoogeveen', '20 characters kept'],
  [[alert('a4', 'Grote brand in Alphen aan den Rijn. Sluit ramen.', false)], 'NL-Alert: in Alphen aan den Rijn', 'multi-word town kept'],
  [[alert('a5', 'Stroomstoring. Blijf thuis.', false)], 'NL-Alert: actief', 'no "in": active'],
  [[alert('b1', 'Brand in Zwolle. Ramen dicht.', false), alert('b2', 'Brand in Den Haag. Ramen dicht.', true)], 'NL-Alert: in Den Haag (+1) (in jouw omgeving)', 'own area first, +1 (screen readers also hear "in jouw omgeving")'],
  [[alert('c1', 'NL-Alert ingetrokken voor brand in Zwaag.', false, { withdrawn: true }), alert('c2', 'Oud bericht in Goes.', false, { stop: iso(now - 60e3) })], '(hidden)', 'withdrawn/expired: not in the top bar'],
  [[], '(hidden)', 'no alerts: not in the top bar'],
]) {
  const [ctx, p, errs] = await open(1440, alerts);
  const bd = await badge(p);
  ok(bd.text === want && errs.length === 0, `${name}: "${bd.text}"${bd.title ? ' [' + bd.title.replace(/\n/g, ' | ') + ']' : ''}`);
  if (name === 'long place → last word') {
    ok(bd.order.replace(/^ab-knmi,/, '') === 'ab-p2k,ab-nl,ab-nctv' && bd.dot === 'rgb(192, 31, 54)' && /Kalverdijkje Leeuwarden/.test(bd.title), `between Alarmeringen and Dreigingsniveau (${bd.order}), red dot, full sentence as tooltip`);
    await p.click('#ab-nl'); await p.waitForTimeout(600);
    ok(await p.evaluate(() => { const r = document.querySelector('#panel-nlalert').getBoundingClientRect(); return r.top >= 0 && r.top < 300 && document.activeElement?.closest('#panel-nlalert'); }), 'click scrolls to the NL-Alert panel and focuses it');
    const ax = await new AxeBuilder({ page: p }).include('#alertbar').analyze();
    ok(ax.violations.length === 0, `axe on top bar: ${ax.violations.map(v => v.id).join(',') || 0}`);
  }
  if (name === 'no alerts: not in the top bar') {
    ok(bd.order.replace(/^ab-knmi,/, '') === 'ab-p2k,ab-nctv', `top bar without NL-Alert: ${bd.order}`);
    const pn = await p.evaluate(() => document.querySelector('#panel-nlalert')?.textContent || '');
    ok(/Geen actieve NL-Alerts/.test(pn), 'the NL-Alert panel still says there are no active alerts');
  }
  await ctx.close();
}
// hidden panel is shown again on click; phone; English
{
  const ctx = await b.newContext({ viewport: { width: 360, height: 800 } });
  await ctx.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, panels: { order: [], collapsed: {}, hidden: { nlalert: true } } })));
  const p = await ctx.newPage();
  await p.route('**/api/nlalert*', r => r.fulfill({ json: { enabled: true, fetched_at: iso(now), alerts: [alert('a1', 'Brand met veel rook in Kalverdijkje Leeuwarden. Blijf uit de rook!', true)] } }));
  await p.goto(URL); await p.waitForFunction(() => /Leeuwarden/.test(document.querySelector('#ab-nl').textContent));
  ok(await p.evaluate(() => !document.querySelector('#panel-nlalert')), 'panel hidden by the visitor');
  const sw = await p.evaluate(() => document.documentElement.scrollWidth);
  await p.click('#ab-nl'); await p.waitForSelector('#panel-nlalert');
  ok(sw <= 360, `360px: badge fits, panel is shown again after a click (scrollWidth ${sw})`);
  await ctx.close();
  const [c2, p2] = await open(1440, [alert('a1', 'Brand met veel rook in Kalverdijkje Leeuwarden. Blijf uit de rook!', true)], 'en');
  const bd = await badge(p2);
  ok(bd.text === 'NL-Alert: in Leeuwarden (in your area)' || bd.text === 'NL-Alert: in Leeuwarden', `English: "${bd.text}"`);
  await c2.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
