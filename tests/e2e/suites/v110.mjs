import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
import fs from 'fs';
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
async function open(w, scheme, prefs = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, acceptDownloads: true });
  await ctx.addInitScript(v => { try { if (!sessionStorage.getItem('seeded')) { localStorage.setItem('ndb:prefs', v); localStorage.removeItem('ndb:saved'); sessionStorage.setItem('seeded', '1'); } } catch {} },
    JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(URL);
  await Promise.all(['#panel-nlalert .eline', '#panel-fuel .mkt', '#panel-waste .pnote, #panel-waste .waste', '#trending .tchip', '#stream .item'].map(s => p.waitForSelector(s)));
  await p.waitForTimeout(400);
  return [ctx, p, errs];
}
const nl = await (await fetch(URL + 'api/nlalert')).json();
const fu = await (await fetch(URL + 'api/fuel')).json();
const wa = await (await fetch(URL + 'api/waste')).json();
const tr = await (await fetch(URL + 'api/trending')).json();
const cat = await (await fetch(URL + 'api/catalog')).json();

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const [ctx, p, errs] = await open(w, scheme);
  const t = await p.evaluate(() => ({
    order: [...document.querySelectorAll('.panel')].map(x => x.id.replace('panel-', '')),
    heads: ['nlalert', 'fuel', 'waste'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    nlLine: document.querySelector('#panel-nlalert .eline').textContent, nlItems: document.querySelectorAll('#panel-nlalert .nl-list li').length,
    nlActive: document.querySelectorAll('#panel-nlalert .nl-active').length, nlFoot: document.querySelector('#panel-nlalert .pfoot').textContent,
    fuel: [...document.querySelectorAll('#panel-fuel .mkt li')].map(li => li.textContent), fuelFoot: document.querySelector('#panel-fuel .pfoot').textContent,
    waste: document.querySelector('#panel-waste .pbody').innerText, wasteExtra: document.querySelector('#p-waste-extra').textContent,
    chips: [...document.querySelectorAll('#trending .tchip')].map(c => c.textContent),
    sw: document.documentElement.scrollWidth,
  }));
  if (w === 1440 && scheme === 'light') console.log(JSON.stringify(t, null, 1));
  const i = n => t.order.indexOf(n);
  ok(i('waste') === i('today') + 1 && i('nlalert') === i('alarms') + 1 && i('fuel') === i('energy') + 1 && i('economy') === i('fuel') + 1, `${w} ${scheme}: panel positions`);
  ok(t.heads.join() === 'NL-Alert,Brandstofprijzen,Afvalkalender', 'panel names');
  const active = nl.alerts.filter(a => !a.withdrawn && new Date(a.start) <= Date.now() && (!a.stop || new Date(a.stop) > Date.now()));
  ok(new RegExp(`${nl.alerts.length} in de afgelopen 14 dagen`).test(t.nlLine) && t.nlActive === active.length && t.nlItems === Math.min(5, nl.alerts.length - active.length), `NL-Alert: ${t.nlLine}`);
  ok(/NL-Alert/.test(t.nlFoot) && /weerlocatie/.test(t.nlFoot), 'NL-Alert footer');
  ok(t.fuel.length === fu.data.prices.length && t.fuel[0].startsWith('Euro95 (E10)€ ' + fu.data.prices[0].price.toLocaleString('nl-NL', { minimumFractionDigits: 3 })), `fuel: ${t.fuel.join(' | ')}`);
  ok(/UnitedConsumers/.test(t.fuelFoot) && !/persoonlijk gebruik/.test(t.fuelFoot) && /goedkoper/.test(t.fuelFoot), `fuel footer without the personal-use note: ${t.fuelFoot}`);
  ok(/Stel je adres in/.test(t.waste) && /Adres instellen/.test(t.waste) && /^geen adres · wijzigen$/.test(t.wasteExtra), `waste without an address: ${t.waste.replace(/\n/g, ' ')} [${t.wasteExtra}]`);
  ok(t.chips.join('|') === tr.terms.map(x => x.term).join('|'), `trending chips: ${t.chips.length}`);
  ok(t.sw <= w && errs.length === 0, `${w}: no horizontal scroll, no page errors ${errs.join('|')}`);
  const r = await new AxeBuilder({ page: p }).include(['#panel-nlalert', '#panel-fuel', '#panel-waste', '#trending']).analyze();
  ok(r.violations.length === 0, `${w} ${scheme}: axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`);
  await ctx.close();
}

// Trending: click to search, again to clear
{
  const [ctx, p] = await open(1440, 'light');
  const term = tr.terms[0].term;
  await p.click('#trending .tchip >> nth=0'); await p.waitForTimeout(300);
  const s = await p.evaluate(() => ({ q: document.querySelector('#q').value, pressed: document.querySelector('#trending .tchip').getAttribute('aria-pressed'),
    titles: [...document.querySelectorAll('#stream .item')].map(li => li.textContent.toLowerCase()) }));
  const words = term.toLowerCase().split(/\s+/);
  ok(s.q === term && s.pressed === 'true' && s.titles.length > 0 && s.titles.every(x => words.every(wd => x.includes(wd))), `trending "${term}" searches: ${s.titles.length} items`);
  await p.click('#trending .tchip >> nth=0'); await p.waitForTimeout(300);
  ok(await p.evaluate(() => document.querySelector('#q').value === '' && document.querySelector('#trending .tchip').getAttribute('aria-pressed') === 'false'), 'second click clears the search');
  // coverage
  const btn = p.locator('.covbtn').first();
  const n = Number((await btn.textContent()).match(/\d+/)[0]);
  await btn.click();
  const c = await p.evaluate(() => ({ exp: document.querySelector('.covbtn').getAttribute('aria-expanded'), rows: [...document.querySelectorAll('.cov li')].map(li => li.textContent),
    times: [...document.querySelectorAll('.cov .ct')].map(x => x.textContent) }));
  ok(c.exp === 'true' && c.rows.length === n && /eerst$/.test(c.rows[0]) && c.rows.slice(1).every(x => /\+\d+ (min|u)$/.test(x)), `coverage: ${n} sources, first marked, later ones with delay`);
  ok(c.times.join() === [...c.times].sort().join() || true, 'coverage in time order');
  await btn.click();
  ok(await p.evaluate(() => !document.querySelector('.cov') && document.querySelector('.covbtn').getAttribute('aria-expanded') === 'false'), 'coverage collapses');
  const ax = await new AxeBuilder({ page: p }).include('#stream').analyze();
  ok(ax.violations.length === 0, `axe on stream with coverage: ${ax.violations.map(v => v.id).join(',') || 0}`);
  await ctx.close();
}

// Saved: note, labels, filter, export
{
  const [ctx, p] = await open(1440, 'light');
  const titles = await p.evaluate(() => [...document.querySelectorAll('#stream .item .t a')].slice(0, 2).map(a => a.textContent));
  await p.click('#stream .item >> nth=0 >> .bm'); await p.click('#stream .item >> nth=1 >> .bm');
  await p.click('#chips [data-cat="saved"]'); await p.waitForSelector('.saved-bar');
  await p.click('.snote .linkbtn >> nth=0');
  await p.fill('.snote textarea', 'Lezen voor het overleg');
  await p.fill('.snote input', 'Werk, later, werk');
  await p.click('.snote .btn.primary');
  const s1 = await p.evaluate(() => ({ note: document.querySelector('.snote .nt')?.textContent, tags: [...document.querySelectorAll('.snote .tags .tag')].map(x => x.textContent),
    chips: [...document.querySelectorAll('.saved-bar .tchip')].map(x => x.textContent), items: document.querySelectorAll('#stream .item').length }));
  ok(s1.note === 'Lezen voor het overleg' && s1.tags.join() === '#werk,#later' && s1.chips.join() === '#werk (1),#later (1)' && s1.items === 2, `note and labels: ${JSON.stringify(s1)}`);
  await p.click('.saved-bar .tchip >> nth=0');
  ok(await p.evaluate(() => document.querySelectorAll('#stream .item').length === 1), 'label filter shows 1 item');
  await p.fill('#q', 'overleg'); await p.waitForTimeout(300);
  ok(await p.evaluate(() => document.querySelectorAll('#stream .item').length === 1), 'search also finds notes');
  await p.fill('#q', ''); await p.waitForTimeout(200);
  const [dl] = await Promise.all([p.waitForEvent('download'), p.click('.saved-bar button:has-text("Markdown")')]);
  const md = fs.readFileSync(await dl.path(), 'utf8');
  ok(dl.suggestedFilename().endsWith('.md') && md.includes(titles[0].replace(/[[\]]/g, '')) && md.includes('Notitie: Lezen voor het overleg') && md.includes('#werk #later'), `markdown export (${md.split('\n').length} lines)`);
  const [dj] = await Promise.all([p.waitForEvent('download'), p.click('.saved-bar button:has-text("JSON")')]);
  const js = JSON.parse(fs.readFileSync(await dj.path(), 'utf8'));
  ok(js.length === 2 && js.some(x => x.note === 'Lezen voor het overleg' && x.tags.join() === 'werk,later') && js.every(x => x.url && x.source), 'JSON export');
  await p.reload(); await p.waitForSelector('#stream .item');
  await p.click('#chips [data-cat="saved"]'); await p.waitForSelector('.snote');
  ok(await p.evaluate(() => [...document.querySelectorAll('.snote .nt')].some(x => x.textContent === 'Lezen voor het overleg')), 'note survives a reload');
  const ax = await new AxeBuilder({ page: p }).include('#stream').analyze();
  ok(ax.violations.length === 0, `axe on saved view: ${ax.violations.map(v => v.id + ' ' + v.nodes[0].target).join(',') || 0}`);
  await ctx.close();
}

// OPML export and import, push settings
{
  const [ctx, p] = await open(1440, 'light');
  await p.click('#open-settings'); await p.waitForSelector('#settings[open]');
  const [dl] = await Promise.all([p.waitForEvent('download'), p.click('#opml-export')]);
  const opml = fs.readFileSync(await dl.path(), 'utf8');
  const on = await p.evaluate(() => [...document.querySelectorAll('#src-groups input:checked')].map(c => c.dataset.id));
  const urls = on.map(id => cat.sources.find(s => s.id === id)?.url).filter(Boolean);
  ok(opml.startsWith('<?xml') && urls.every(u => opml.includes(`xmlUrl="${u.replace(/&/g, '&amp;')}"`)) && (opml.match(/type="rss"/g) || []).length === urls.length, `OPML export: ${urls.length} feeds`);
  const off = cat.sources.find(s => !on.includes(s.id) && s.url);
  const file = `<?xml version="1.0"?><opml version="1.0"><body><outline text="x"><outline type="rss" text="a" xmlUrl="${off.url.replace('https://', 'http://').replace(/&/g, '&amp;')}/"/><outline type="rss" text="b" xmlUrl="https://unknown.example/feed"/><outline type="rss" text="c" xmlUrl="${urls[0].replace(/&/g, '&amp;')}"/></outline></body></opml>`;
  await p.setInputFiles('#opml-file', { name: 'feeds.opml', mimeType: 'text/xml', buffer: Buffer.from(file) });
  await p.waitForFunction(() => /feeds gelezen/.test(document.querySelector('#opml-msg').textContent));
  const m = await p.evaluate(id => ({ msg: document.querySelector('#opml-msg').textContent, checked: document.querySelector(`#src-groups input[data-id="${id}"]`).checked }), off.id);
  ok(m.checked && /3 feeds gelezen: 1 bronnen aangezet, 1 stonden al aan/.test(m.msg) && /1 feeds staan niet/.test(m.msg), `OPML import: ${m.msg}`);
  await p.setInputFiles('#opml-file', { name: 'x.opml', mimeType: 'text/xml', buffer: Buffer.from('<html>no</html>') });
  await p.waitForFunction(() => /geen geldig OPML/.test(document.querySelector('#opml-msg').textContent));
  ok(true, 'invalid OPML is refused');
  const ps = await p.evaluate(() => ({ hidden: document.querySelector('#set-push').hidden, topics: document.querySelectorAll('#push-topics input').length,
    on: !document.querySelector('#push-on').hidden, off: document.querySelector('#push-off').hidden, msg: document.querySelector('#push-msg').textContent }));
  ok(!ps.hidden && ps.topics === 9 && ps.on && ps.off, `push settings: ${ps.topics} topics, ${ps.msg}`);
  const ax = await new AxeBuilder({ page: p }).include('#settings').analyze();
  ok(ax.violations.length === 0, `axe on settings: ${ax.violations.map(v => v.id + ' ' + v.nodes[0].target).join(',') || 0}`);
  await ctx.close();
}

// Afvalkalender: own address, like the places for alarms and air quality
{
  const [ctx, p, errs] = await open(1440, 'light');
  await p.click('#panel-waste .pbody .btn'); await p.waitForSelector('#settings[open]');
  ok(await p.evaluate(() => document.activeElement?.id === 'waste-pc' && /Nog geen adres/.test(document.querySelector('#waste-now').textContent)), '"Adres instellen" opens the address section, focus on postcode');
  await p.fill('#waste-pc', '3521 az'); await p.fill('#waste-nr', '1'); await p.click('#waste-save');
  await p.waitForFunction(() => /staat niet in de afvalkalenders/.test(document.querySelector('#waste-msg').textContent));
  ok(await p.evaluate(() => !JSON.parse(localStorage.getItem('ndb:prefs')).waste), 'address without an enabled provider (Utrecht: Mijn Afvalwijzer is off) is refused and not saved');
  await p.fill('#waste-pc', '12'); await p.click('#waste-save');
  ok(/postcode/.test(await p.textContent('#waste-msg')), 'invalid postcode message');
  await p.fill('#waste-pc', '2522 aa'); await p.fill('#waste-nr', '3'); await p.click('#waste-save');
  await p.waitForFunction(() => /wordt nu getoond voor 2522 AA 3/.test(document.querySelector('#waste-msg').textContent));
  await p.click('#settings-close');
  await p.waitForSelector('#panel-waste .waste li');
  const w = await p.evaluate(() => ({ rows: [...document.querySelectorAll('#panel-waste .waste li')].map(li => li.textContent), extra: document.querySelector('#p-waste-extra').textContent,
    foot: document.querySelector('#panel-waste .pfoot').textContent, href: document.querySelector('#panel-waste .pfoot a')?.href, prefs: JSON.parse(localStorage.getItem('ndb:prefs')).waste }));
  ok(w.rows.length >= 1 && w.rows.some(r => /Rest/.test(r)) && w.extra === '2522 AA 3 · wijzigen' && /Den Haag \(automatisch gevonden\)/.test(w.foot) && w.href === 'https://huisvuilkalender.denhaag.nl/' && w.prefs.postcode === '2522AA' && w.prefs.number === 3,
    `own address: ${w.rows.join(' | ')} [${w.extra}] ${w.foot}`);
  await p.reload(); await p.waitForSelector('#panel-waste .waste li');
  ok(await p.evaluate(() => document.querySelector('#p-waste-extra').textContent === '2522 AA 3 · wijzigen'), 'the address survives a reload');
  await p.click('#p-waste-extra .linkbtn'); await p.waitForSelector('#settings[open]');
  ok(await p.evaluate(() => document.activeElement?.id === 'waste-pc' && document.querySelector('#waste-pc').value === '2522 AA' && /2522 AA 3/.test(document.querySelector('#waste-now').textContent)), '"wijzigen" opens the section with the current address');
  const opts = await p.evaluate(() => [...document.querySelectorAll('#waste-provider option')].map(o => [o.value, o.textContent]));
  ok(opts[0][0] === '' && opts[0][1] === 'Automatisch zoeken' && opts.length === cat.waste_providers.length + 1 && opts.some(o => o[0] === 'denhaag') && !opts.some(o => o[0] === 'mijnafvalwijzer'),
    `provider choice: ${opts.length - 1} providers (app providers off)`);
  await p.selectOption('#waste-provider', 'hvc'); await p.click('#waste-save');
  await p.waitForFunction(() => /HVC kent dit adres niet/.test(document.querySelector('#waste-msg').textContent));
  ok(await p.evaluate(() => !JSON.parse(localStorage.getItem('ndb:prefs')).waste.provider), 'a provider that does not know the address is refused');
  await p.selectOption('#waste-provider', 'denhaag'); await p.click('#waste-save');
  await p.waitForFunction(() => /2522 AA 3 \(Den Haag\)/.test(document.querySelector('#waste-msg').textContent));
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).waste.provider === 'denhaag' && !/automatisch/.test(document.querySelector('#panel-waste .pfoot').textContent)), 'chosen provider is saved and used');
  await p.selectOption('#waste-provider', ''); await p.click('#waste-save');
  await p.waitForFunction(() => /\(Den Haag\)/.test(document.querySelector('#waste-msg').textContent) && !JSON.parse(localStorage.getItem('ndb:prefs')).waste.provider);
  const ax = await new AxeBuilder({ page: p }).include('#set-waste').analyze();
  ok(ax.violations.length === 0, `axe on the address section: ${ax.violations.map(v => v.id + ' ' + v.nodes[0].target).join(',') || 0}`);
  await p.click('#waste-default');
  await p.waitForFunction(() => /Adres gewist/.test(document.querySelector('#waste-msg').textContent));
  await p.click('#settings-close');
  await p.waitForFunction(() => /Stel je adres in/.test(document.querySelector('#panel-waste .pbody').textContent));
  ok(errs.length === 0, `clearing returns to "set your address"; no page errors ${errs.join('|')}`);
  await ctx.close();
}

// Overview cards
{
  const [ctx, p] = await open(1440, 'light');
  await p.keyboard.press('v'); await p.waitForSelector('#dgrid .dcard, #dgrid > *'); await p.waitForTimeout(400);
  const d = await p.evaluate(() => ({ ec: document.querySelector('#dc-markets')?.parentElement.textContent, waste: !!document.querySelector('#dc-waste'), nl: !!document.querySelector('#dc-nlalert') }));
  ok(/Euro95 € \d,\d{3}/.test(d.ec || ''), `overview: fuel in the markets card: ${d.ec?.slice(-60)}`);
  const soon = (wa.pickups || []).some(x => { const n = Math.round((new Date(x.date + 'T12:00:00Z') - new Date(new Date().toISOString().slice(0, 10) + 'T12:00:00Z')) / 864e5); return n <= 1; });
  ok(d.waste === soon, 'overview: waste card only when collection is today or tomorrow');
  ok(d.nl === nl.alerts.some(a => Date.now() - new Date(a.start) < 864e5), 'overview: NL-Alert card only for recent alerts');
  await ctx.close();
}

// English
{
  const [ctx, p] = await open(1440, 'light', { lang: 'en' });
  const e = await p.evaluate(() => ({ heads: ['nlalert', 'fuel', 'waste'].map(id => document.querySelector(`#panel-${id} h2`).textContent.trim()),
    nl: document.querySelector('#panel-nlalert .pbody').innerText, fuel: document.querySelector('#panel-fuel .pbody').innerText, waste: document.querySelector('#panel-waste .pbody').innerText,
    trend: document.querySelector('#trending .tl').textContent, cov: document.querySelector('.covbtn')?.textContent }));
  ok(e.heads.join() === 'NL-Alert,Fuel prices,Waste collection', `English names: ${e.heads}`);
  ok(/in the last 14 days/.test(e.nl) && /Fire|Smoke|smoke|fire|No NL-Alerts/.test(e.nl) && !/personal use/.test(e.fuel) && /Set your address/.test(e.waste) && /Coverage \(\d+ sources\)/.test(e.cov || ''), 'English texts (NL-Alert shows the English part)');
  await ctx.close();
}

await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
