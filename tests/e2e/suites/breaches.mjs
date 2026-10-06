import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const prefs = extra => ({ v: 2, onboarded: true, ...extra });
async function page(w, scheme, p) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme });
  await ctx.addInitScript(v => { try { localStorage.setItem('ndb:prefs', v); } catch {} }, JSON.stringify(p));
  const pg = await ctx.newPage();
  await pg.goto(URL); await pg.waitForSelector('#panel-breaches .bgrp');
  return [ctx, pg];
}
const api = await (await fetch(URL + 'api/breaches')).json();

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p] = await page(w, scheme, prefs());
  await p.waitForTimeout(800);
  const t = await p.evaluate(() => ({
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    head: document.querySelector('#panel-breaches h2').textContent.trim(),
    groups: [...document.querySelectorAll('#panel-breaches .bgrp')].map(g => ({ h: g.querySelector('h3').textContent,
      items: [...g.querySelectorAll('li')].map(li => ({ href: li.querySelector('a')?.href, title: li.querySelector('a')?.textContent,
        tip: li.querySelector('a')?.title, meta: [...li.querySelectorAll('.m > span')].map(x => x.textContent).join(' · '), dc: li.querySelector('.bdc')?.textContent,
        noTr: !!li.querySelector('[translate="no"] a') })) })),
    foot: document.querySelector('#panel-breaches .pfoot')?.textContent,
    fresh: document.querySelector('#p-breaches-fresh')?.textContent,
    sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t.groups, null, 1));
  const i = t.order.indexOf('breaches');
  ok(i > 0 && t.order[i - 1] === 'advisories' && t.order[i + 1] === 'ransomware', `${w} ${scheme}: Datalekken between Security-adviezen and Ransomware NL`);
  ok(t.head === 'Datalekken', 'heading "Datalekken"');
  ok(t.groups.length === 2 && t.groups[0].h === 'Nederland' && t.groups[1].h === 'Elders', 'two groups: Nederland, Elders');
  ok(t.groups[0].items.length === Math.min(3, api.data.nl.length) && t.groups[1].items.length === 3, `3 NL + 3 other (${t.groups.map(g => g.items.length)})`);
  const all = t.groups.flatMap(g => g.items);
  ok(all.every(x => /^https:\/\/haveibeenpwned\.com\/Breach\/[\w.-]+$/.test(x.href) && x.noTr && x.tip), 'each links to its HIBP page, with summary tooltip, not translated');
  ok(all.every(x => /accounts · .*gemeld \d/.test(x.meta)), 'meta: accounts · domain · leak date · added date');
  ok(t.groups[0].items[0]?.title === api.data.nl[0].title, `newest NL first: ${t.groups[0].items[0]?.title}`);
  ok(all.some(x => /e-mailadressen|namen|geboortedata/.test(x.dc || '')), 'leaked data types shown in Dutch');
  ok(/Have I Been Pwned \(CC BY 4\.0\)/.test(t.foot) && !/e-mailadres/.test(t.foot), 'attribution + licence, no e-mail check link');
  ok(/bijgewerkt/.test(t.fresh || ''), `freshness: "${t.fresh}"`);
  ok(t.sw <= w, `${w}: no horizontal scroll`);
  const r = await new AxeBuilder({ page: p }).include('#panel-breaches').analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe on Datalekken: ${r.violations.map(v => v.id).join(',') || 0}`);
  await p.locator('#panel-breaches').screenshot({ path: `${OUT}/breaches-${w}-${scheme}.png` });
  await ctx.close();
}

// English
{
  for (const w of [1440, 360]) {
  const [ctx, p] = await page(w, 'light', prefs({ lang: 'en' }));
  await p.waitForTimeout(500);
  const t = await p.evaluate(() => ({ head: document.querySelector('#panel-breaches h2').textContent.trim(),
    h3: [...document.querySelectorAll('#panel-breaches .bgrp h3')].map(x => x.textContent), meta: [...document.querySelectorAll('#panel-breaches .m')[0].children].map(x => x.textContent).join(' · '),
    dc: document.querySelector('#panel-breaches .bdc')?.textContent, foot: document.querySelector('#panel-breaches .pfoot')?.textContent }));
  console.log(JSON.stringify(t));
  ok(t.head === 'Data breaches' && t.h3.join() === 'Netherlands,Elsewhere', 'English headings');
  ok(/accounts · .*added \d/.test(t.meta) && /(Email addresses|Names|Dates of birth)/.test(t.dc), 'English meta and original data types');
  ok(/Source: Have I Been Pwned/.test(t.foot) && /Only verified/.test(t.foot), 'English footer');
  ok(await p.evaluate(() => document.documentElement.scrollWidth) <= w, `${w} English: no horizontal scroll`);
  await ctx.close();
  }
}

// Skeleton height ≈ real height (no layout shift when the data arrives)
for (const w of [1440, 412]) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 } });
  await ctx.addInitScript(v => { try { localStorage.setItem('ndb:prefs', v); } catch {} }, JSON.stringify(prefs()));
  const p = await ctx.newPage();
  await p.route('**/api/breaches', async r => { await new Promise(x => setTimeout(x, 1500)); r.continue(); });
  await p.goto(URL); await p.waitForSelector('#panel-breaches .tsk');
  const a = await p.evaluate(() => document.querySelector('#p-breaches-body').getBoundingClientRect().height);
  await p.waitForSelector('#panel-breaches .bgrp');
  const c = await p.evaluate(() => document.querySelector('#p-breaches-body').getBoundingClientRect().height);
  ok(Math.abs(a - c) < 60, `${w}: skeleton ${a}px vs content ${Math.round(c)}px`);
  await ctx.close();
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
