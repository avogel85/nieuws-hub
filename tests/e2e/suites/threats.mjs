import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || (process.env.BASE || 'http://127.0.0.1:8080/');
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [768, 'light'], [360, 'dark']]) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme });
  const p = await ctx.newPage();
  await p.goto(URL);
  await p.waitForSelector('#panel-threats .spark'); await p.waitForSelector('#panel-advisories .adv');
  const t = await p.evaluate(() => {
    const q = s => document.querySelector(s), qa = s => [...document.querySelectorAll(s)];
    return {
      infocon: q('#panel-threats .infocon')?.textContent,
      kpi: q('#panel-threats .kpi')?.textContent, delta: q('#panel-threats .delta')?.textContent,
      sparkPts: q('#panel-threats .spark .line')?.getAttribute('d').split('L').length,
      stats: q('#panel-threats .tstats')?.textContent,
      portsH: q('#th-ports')?.textContent, ports: qa('#panel-threats .port').map(li => li.querySelector('.pn').textContent + ' ' + li.querySelector('.val').textContent).slice(0, 3),
      tabs: qa('#panel-threats [role=tab]').map(x => x.textContent + (x.getAttribute('aria-selected') === 'true' ? '*' : '')),
      ipRows: qa('#tp-ips tbody tr').length, firstIP: q('#tp-ips tbody tr')?.textContent, countries: qa('#tp-ips .cbar').map(x => x.textContent).slice(0, 3),
      foot: q('#panel-threats .pfoot')?.textContent,
      advs: qa('#panel-advisories .adv').length, adv0: q('#panel-advisories .adv')?.innerText.replace(/\n/g, ' | '),
      sw: document.documentElement.scrollWidth, iw: innerWidth,
    };
  });
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t, null, 1));
  ok(/^Infocon (groen|geel|oranje|rood)$/.test(t.infocon), `${w} ${scheme}: Infocon badge "${t.infocon}"`);
  ok(/unieke bronnen op/.test(t.kpi) && /gemiddelde/.test(t.delta) && t.sparkPts >= 25, `${w} ${scheme}: 30-day KPI + sparkline (${t.sparkPts} points)`);
  ok(/Meest aangevallen poorten/.test(t.portsH) && t.ports.length === 3, `${w} ${scheme}: ports ${t.ports.join(', ')}`);
  ok(t.tabs.length === 4 && t.ipRows === 10 && t.countries.length > 0, `${w} ${scheme}: origin tabs ${t.tabs.join(' / ')}, 10 IP rows`);
  ok(/SANS Internet Storm Center/.test(t.foot) && /Feodo/.test(t.foot) && /ip-api/.test(t.foot) && /CC BY-NC-SA/.test(t.foot), `${w} ${scheme}: sources + licence footer`);
  ok(t.advs >= 5 && /NCSC-\d{4}-\d{4}/.test(t.adv0) && /kans: /.test(t.adv0) && /schade: /.test(t.adv0), `${w} ${scheme}: advisories with kans/schade badges`);
  ok(t.sw <= t.iw, `${w} ${scheme}: no horizontal scroll (${t.sw}/${t.iw})`);
  const r = await new AxeBuilder({ page: p }).include('#panel-threats').include('#panel-advisories').analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe threats+advisories ${r.violations.length} violations`);
  for (const v of r.violations) console.log(`   - ${v.id}: ${v.help} → ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`);
  await p.locator('#panel-threats').screenshot({ path: `${OUT}/threats-${w}-${scheme}.png` });
  await p.locator('#panel-advisories').screenshot({ path: `${OUT}/adv-${w}-${scheme}.png` });
  await ctx.close();
}

// Keyboard: tabs with arrow keys, persisted; advisory filters.
{
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 } });
  const p = await ctx.newPage();
  await p.goto(URL); await p.waitForSelector('#tt-ips');
  await p.focus('#tt-ips'); await p.keyboard.press('ArrowRight');
  const s1 = await p.evaluate(() => ({ active: document.activeElement.id, sel: document.querySelector('#tt-feodo').getAttribute('aria-selected'), hidden: document.querySelector('#tp-ips').hidden, rows: document.querySelectorAll('#tp-feodo tbody tr').length, text: document.querySelector('#tp-feodo .tstats').textContent }));
  ok(s1.active === 'tt-feodo' && s1.sel === 'true' && s1.hidden && s1.rows > 0, `ArrowRight switches to Feodo tab (${s1.rows} rows: "${s1.text}")`);
  await p.keyboard.press('End'); await p.keyboard.press('Home');
  ok(await p.evaluate(() => document.activeElement.id === 'tt-ips'), 'Home returns to first tab');
  await p.keyboard.press('ArrowLeft'); // wraps to last (IOC's since 1.31)
  await p.reload(); await p.waitForSelector('#tt-threatfox');
  ok(await p.getAttribute('#tt-threatfox', 'aria-selected') === 'true', 'selected tab persists after reload');
  await p.screenshot({ path: `${OUT}/feodo-tab.png`, clip: await p.locator('#panel-threats .tsec:nth-of-type(3)').boundingBox() });
  const total = await p.locator('#panel-advisories .adv').count();
  await p.click('#panel-advisories .advf .chip:nth-child(2)');
  const high = await p.evaluate(() => [...document.querySelectorAll('#panel-advisories .adv .top .tag:first-child')].map(x => x.textContent));
  ok(high.length > 0 && high.every(x => x === 'Hoog' || x === 'Kritiek'), `filter Hoog/kritiek: ${high.length} items, all high/critical`);
  await p.click('#panel-advisories .advf .chip:nth-child(3)');
  const newer = await p.evaluate(() => [...document.querySelectorAll('#panel-advisories .adv time')].map(t => (Date.now() - new Date(t.dateTime)) / 3.6e6));
  ok(newer.every(hrs => hrs <= 24), `filter nieuw (24 u): ${newer.length} items, all ≤ 24 h old`);
  await p.click('#panel-advisories .advf .chip:nth-child(1)');
  ok(await p.locator('#panel-advisories .adv').count() === total, 'filter Alle restores the list');
  const cveHref = await p.getAttribute('#panel-advisories .adv .cve', 'href');
  ok(/^https:\/\/advisories\.ncsc\.nl\/advisory\?id=NCSC-/.test(cveHref || ''), `CVE links point to the NCSC advisory (${cveHref})`);
  await ctx.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED'); process.exit(fails ? 1 : 0);
