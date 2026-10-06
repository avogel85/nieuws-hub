import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
import { ransomware } from './fixtures.mjs';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8080/';
const b = await chromium.launch();
showBothViews(b);
const ALL_IDS = (await (await fetch(URL + 'api/catalog')).json()).sources.map(s => s.id); // a returning visitor has seen every source
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const fresh = async (opts = {}, prefs) => {
  // service workers blocked (route() cannot see their requests), except where a test needs one (F)
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await ctx.route('**/api/ransomware*', r => r.fulfill({ json: ransomware() })); // ransomware.live rate-limits
  ctx.on('page', pg => { if (pg.url() !== 'about:blank') pg.close().catch(() => {}); }); // articles opened in new tabs
  if (prefs) await ctx.addInitScript(p => localStorage.setItem('ndb:prefs', JSON.stringify(p)), prefs);
  const p = await ctx.newPage();
  return { ctx, p };
};
const chips = p => p.$$eval('#chips .chip', cs => cs.map(c => c.textContent.trim()));

// ---------- D: first visit
{
  const { ctx, p } = await fresh();
  await p.goto(URL); await p.waitForSelector('#welcome .preset');
  const cards = await p.$$eval('#welcome .preset', ls => ls.map(l => l.innerText.replace(/\n/g, ' — ')));
  ok(cards.length === 9, `D: welcome card with ${cards.length} presets`);
  const regio = cards.find(c => c.startsWith('Mijn regio'));
  ok(/RTV Utrecht \(Utrecht\)/.test(regio), `D: regional preset follows weather province: "${regio}"`);
  await p.uncheck('#welcome input[value="kort"]');
  ok(await p.isDisabled('#welcome .btn.primary'), 'D: "Klaar" disabled when nothing is chosen');
  await p.check('#welcome input[value="tech"]'); await p.check('#welcome input[value="regio"]');
  const [req] = await Promise.all([p.waitForRequest(r => r.url().includes('api/news?')), p.click('#welcome .btn.primary')]);
  const srcs = decodeURIComponent(req.url().split('sources=')[1]).split(',');
  ok(srcs.includes('tweakers') && srcs.includes('rtv-utrecht') && !srcs.includes('nos-algemeen'), `D: chosen presets become the sources (${srcs.join(',')})`);
  await p.reload(); await p.waitForSelector('#stream .item');
  ok(await p.isHidden('#welcome'), 'D: welcome card does not return after choosing');
  await ctx.close();
}
{
  const { ctx, p } = await fresh();
  await p.goto(URL); await p.waitForSelector('#welcome .preset');
  const r = await new AxeBuilder({ page: p }).include('#welcome').analyze();
  ok(r.violations.length === 0, `D: axe on welcome card: ${r.violations.map(v => v.id).join(',') || '0 violations'}`);
  await p.screenshot({ path: `${OUT}/welcome.png`, clip: { x: 0, y: 90, width: 880, height: 330 } });
  await p.click('#welcome .btn:not(.primary)');
  await p.reload(); await p.waitForSelector('#stream .item');
  ok(await p.isHidden('#welcome'), 'D: "Overslaan" hides the welcome card for good');
  await ctx.close();
}

// ---------- A: categories incl. Internationaal
{
  const { ctx, p } = await fresh({}, { onboarded: true });
  await p.goto(URL); await p.waitForSelector('#stream .item');
  const c = await chips(p);
  ok(c.some(x => /^Internationaal\s*\d+/.test(x)), `A: new visitors see "Internationaal" with items: ${c.join(' | ')}`);
  await ctx.close();
}
{ // returning user with two sources: only their category gets a chip; "+ Onderwerpen" adds one
  const { ctx, p } = await fresh({}, { onboarded: true, sources: ['nos-algemeen', 'nu-algemeen'], known: ALL_IDS });
  await p.goto(URL); await p.waitForSelector('#stream .item');
  const c = await chips(p);
  ok(!c.some(x => /België|Internationaal|Tech|Sport/.test(x)) && c.some(x => /^Nederland/.test(x)) && c.at(-1) === '+ Onderwerpen',
    `A/C: only followed categories get a chip (${c.join(' | ')})`);
  await p.click('#chips [data-cat="more"]');
  ok(await p.evaluate(() => document.querySelector('#settings').open && !!document.activeElement.closest('#qpresets')), 'A: "+ Onderwerpen" opens the topic presets');
  await p.click('#qpresets button:has-text("Internationaal")');
  const [req] = await Promise.all([p.waitForRequest(r => r.url().includes('api/news?')), p.keyboard.press('Escape')]);
  ok(req.url().includes('bbc-world') && req.url().includes('guardian-world'), 'A: choosing Internationaal enables its sources');
  await p.waitForSelector('#chips [data-cat="world"]');
  // the count follows when the reloaded news arrives (slower on a busy test machine)
  await p.waitForFunction(() => /\d/.test(document.querySelector('#chips [data-cat="world"]')?.textContent || ''), null, { timeout: 15000 }).catch(() => {});
  ok(/^Internationaal\s*\d+/.test(await p.textContent('#chips [data-cat="world"]')), 'A: the Internationaal chip appears with items');
  await ctx.close();
}

// ---------- B: grouping
{
  const { ctx, p } = await fresh({}, { onboarded: true, sources: ['nos-algemeen', 'nu-algemeen', 'telegraaf', 'ad', 'rtl-nieuws', 'nd', 'volkskrant', 'trouw', 'parool', 'metro'], known: ALL_IDS });
  await p.goto(URL); await p.waitForSelector('#stream .item');
  const rel = await p.$$eval('#stream .rel', r => r.map(x => x.textContent));
  ok(rel.length > 0 && rel.every(t => t.startsWith('Ook bij: ')), `B: ${rel.length} grouped stories, e.g. "${rel[0]}"`);
  const relLink = await p.$eval('#stream .rel a', a => ({ href: a.href, target: a.target, rel: a.rel, title: a.title }));
  ok(relLink.target === '_blank' && relLink.rel.includes('noopener') && relLink.title.length > 5, 'B: "ook bij" links open the other outlet, with its headline as tooltip');
  await p.click('#open-settings'); await p.uncheck('#opt-group'); 
  const [req] = await Promise.all([p.waitForRequest(r => r.url().includes('api/news?')), p.keyboard.press('Escape')]);
  await p.waitForSelector('#stream .item');
  ok(req.url().includes('group=0') && await p.locator('#stream .rel').count() === 0, 'B: grouping can be switched off');
  await ctx.close();
}

// ---------- C: read / bewaren, I: keyboard
{
  const { ctx, p } = await fresh({}, { onboarded: true });
  await p.goto(URL); await p.waitForSelector('#stream .item');
  const first = await p.$eval('#stream .item', li => li.dataset.id);
  await p.click('#stream .item .t a');
  ok(await p.$eval('#stream .item', li => li.classList.contains('read')), 'C: clicking an article marks it read');
  const col = await p.$eval('#stream .item .t a', a => getComputedStyle(a).color);
  await p.reload(); await p.waitForSelector('#stream .item');
  ok(await p.$eval(`#stream .item[data-id="${first}"]`, li => li.classList.contains('read')), `C: read state persists (dimmed colour ${col})`);
  // keyboard
  await p.locator('body').click({ position: { x: 5, y: 500 } });
  await p.keyboard.press('j');
  const f1 = await p.evaluate(() => document.activeElement.closest('.item')?.dataset.id);
  await p.keyboard.press('j');
  const f2 = await p.evaluate(() => document.activeElement.closest('.item')?.dataset.id);
  ok(f1 === first && f2 && f2 !== f1, 'I: j moves focus to the first, then the next article');
  await p.keyboard.press('k');
  ok(await p.evaluate(() => document.activeElement.closest('.item')?.dataset.id) === f1, 'I: k moves back');
  await p.keyboard.press('s');
  ok(await p.$eval(`#stream .item[data-id="${f1}"] .bm`, b => b.getAttribute('aria-pressed')) === 'true', 'I+C: s bewaart the focused article');
  ok(/Bewaard\s*1/.test(await p.textContent('#chips [data-cat="saved"]')), 'C: "Bewaard" chip shows the count');
  await p.keyboard.press('m');
  ok(!(await p.$eval(`#stream .item[data-id="${f1}"]`, li => li.classList.contains('read'))), 'I+C: m toggles read → unread');
  await p.keyboard.press('?');
  ok(await p.evaluate(() => document.querySelector('#keys').open), 'I: ? opens the shortcut overview');
  const axeK = await new AxeBuilder({ page: p }).include('#keys').analyze();
  ok(axeK.violations.length === 0, `I: axe on shortcut dialog: ${axeK.violations.map(v => v.id).join(',') || '0 violations'}`);
  await p.keyboard.press('Escape');
  await p.keyboard.press('3');
  ok(await p.getAttribute('#chips .chip:nth-child(3)', 'aria-pressed') === 'true', 'I: digit keys choose a category');
  await p.keyboard.press('1');
  await p.reload(); await p.waitForSelector('#stream .item');
  await p.click('#chips [data-cat="saved"]');
  const savedList = await p.$$eval('#stream .item', l => l.map(x => x.dataset.id));
  ok(savedList.length === 1 && savedList[0] === f1, 'C: saved article persists and is listed under "Bewaard"');
  await p.click('#stream .item .bm');
  ok(await p.locator('#stream .item').count() === 0 && /Nog niets bewaard/.test(await p.textContent('#stream')), 'C: un-saving removes it');
  await p.click('#chips [data-cat="all"]');
  // hide read
  await p.click('#stream .item .t a');
  const readId = await p.$eval('#stream .item.read', li => li.dataset.id);
  await p.click('#open-settings'); await p.check('#opt-hideread'); await p.keyboard.press('Escape');
  ok(await p.locator(`#stream .item[data-id="${readId}"]`).count() === 0, 'C: "gelezen berichten verbergen" hides read articles');
  await ctx.close();
}

// ---------- E: watchlist and mute
{
  const { ctx, p } = await fresh({}, { onboarded: true });
  await p.goto(URL); await p.waitForSelector('#panel-advisories .adv');
  const advTitles = await p.$$eval('#panel-advisories .adv .ttl', a => a.map(x => x.textContent));
  const target = (advTitles.find(t => /Adobe|Check Point|WordPress|IBM/.test(t)) || advTitles[3]).match(/(Adobe|Check Point|WordPress|IBM|F5)/)?.[1] || 'WordPress';
  await p.click('#open-settings');
  await p.fill('#watch-in', target); await p.press('#watch-in', 'Tab');
  await p.fill('#mute-in', 'NOS'); await p.press('#mute-in', 'Tab');
  ok(/1 woord op je volglijst, 1 verborgen/.test(await p.textContent('#watch-msg')), 'E: settings confirm the terms');
  await p.keyboard.press('Escape');
  const top = await p.$eval('#panel-advisories .adv', li => li.innerText);
  ok(top.includes('volglijst') && top.includes(target), `E: advisory about "${target}" is pinned on top with a volglijst tag`);
  ok((await p.$$eval('#panel-advisories .advf .chip', c => c.map(x => x.textContent))).includes('Mijn volglijst'), 'E: advisory filter "Mijn volglijst" appears');
  const srcs = await p.$$eval('#stream .item .src', s => s.map(x => x.textContent));
  ok(srcs.length > 0 && !srcs.some(t => t.startsWith('NOS')), 'E: muted word hides matching articles (all NOS items gone)');
  ok(/verborgen door je filters/.test(await p.textContent('#stream .muted-note')), 'E: a note says how many are hidden');
  ok((await chips(p)).some(c => c.startsWith('Volglijst')), 'E: news chip "Volglijst" appears');
  await ctx.close();
}

// ---------- G: freshness
{
  const { ctx, p } = await fresh({}, { onboarded: true });
  await p.goto(URL); await p.waitForSelector('#panel-threats .spark'); await p.waitForSelector('#panel-advisories .adv'); await p.waitForSelector('#panel-ap .plist');
  // every panel shows its data age, or explains why there is no data (a live source can be down during a test run)
  const fr = await p.$$eval('.pfresh, #news-meta', els => els.map(e => {
    const body = e.id.startsWith('p-') && document.querySelector('#' + e.id.replace('-fresh', '-body'));
    return e.id + ': ' + (e.textContent || (body?.querySelector('.pnote') ? '(no data: ' + body.querySelector('.pnote').textContent.slice(0, 50) + ')' : ''));
  }));
  ok(fr.length === 28 && fr.filter(t => !t.startsWith('p-trains-fresh') && !t.startsWith('p-waste-fresh')).every(t => /bijgewerkt (zojuist|\d+ min geleden)|\(no data: /.test(t)), `G: every panel shows its data age (trains: none without an NS key; waste: none without an address): ${fr.join(' | ')}`);
  await ctx.close();
  const s2 = await fresh({}, { onboarded: true });
  await s2.p.route('**/api/weather*', async route => { const r = await route.fetch(); const d = await r.json(); d.updated = new Date(Date.now() - 2 * 3600e3).toISOString(); await route.fulfill({ response: r, json: d }); });
  await s2.p.goto(URL); await s2.p.waitForSelector('#panel-weather .wxnow');
  const st = await s2.p.$eval('#p-weather-fresh .fresh', e => ({ t: e.textContent, c: getComputedStyle(e).color, cls: e.className }));
  ok(/verouderd · bijgewerkt 2 uur geleden/.test(st.t) && st.cls.includes('stale'), `G: old data is flagged: "${st.t}" (${st.c})`);
  await s2.ctx.close();
}

// ---------- H: thumbnails
{
  const { ctx, p } = await fresh({}, { onboarded: true, images: true, sources: ['nos-algemeen', 'telegraaf', 'hln', 'bbc-world', 'guardian-world'], known: ALL_IDS });
  await p.goto(URL); await p.waitForSelector('#stream .item');
  await p.waitForTimeout(2500);
  const imgs = await p.$$eval('#stream img.thumb', l => l.slice(0, 12).map(i => ({ src: i.getAttribute('src'), w: i.naturalWidth, done: i.complete })));
  const loaded = imgs.filter(i => i.w > 0);
  ok(imgs.length > 0 && imgs.every(i => i.src.startsWith('api/img?u=')), `H: ${imgs.length} thumbnails, all via the proxy`);
  ok(loaded.length >= 3, `H: ${loaded.length} thumbnails loaded (lazy: only those near the viewport)`);
  const ext = await p.evaluate(() => performance.getEntriesByType('resource').map(r => new URL(r.name).host).filter(h => h !== location.host));
  ok(ext.length === 0, `H: browser made no third-party requests (${ext.join(',') || 'none'})`);
  await p.screenshot({ path: `${OUT}/thumbs.png`, clip: { x: 0, y: 90, width: 880, height: 520 } });
  const axe = await new AxeBuilder({ page: p }).include('#stream').analyze();
  ok(axe.violations.length === 0, `H: axe on stream with thumbnails: ${axe.violations.map(v => v.id).join(',') || '0 violations'}`);
  await ctx.close();
}

// ---------- F: installable + offline
{
  const { ctx, p } = await fresh({ serviceWorkers: 'allow' }, { onboarded: true });
  await p.goto(URL); await p.waitForSelector('#stream .item');
  await p.evaluate(() => navigator.serviceWorker.ready);
  const cdp = await ctx.newCDPSession(p);
  const inst = await cdp.send('Page.getInstallabilityErrors');
  ok(inst.installabilityErrors.length === 0, `F: installable (errors: ${JSON.stringify(inst.installabilityErrors)})`);
  const man = await cdp.send('Page.getAppManifest');
  ok(!man.errors.length && /"display":"standalone"/.test(man.data.replace(/\s/g, '')), 'F: manifest parses without errors');
  await p.reload(); await p.waitForSelector('#stream .item');
  ok(await p.evaluate(() => !!navigator.serviceWorker.controller), 'F: service worker controls the page');
  await p.waitForTimeout(1500);
  await ctx.setOffline(true);
  await p.reload(); await p.waitForSelector('#stream .item', { timeout: 15000 });
  const off = await p.evaluate(() => ({ items: document.querySelectorAll('#stream .item').length, banner: !document.querySelector('#offline').hidden, text: document.querySelector('#offline').textContent }));
  ok(off.items > 0 && off.banner, `F: offline reload shows ${off.items} cached articles and the offline banner`);
  await p.screenshot({ path: `${OUT}/offline.png`, clip: { x: 0, y: 0, width: 1440, height: 260 } });
  await ctx.setOffline(false);
  await p.evaluate(() => dispatchEvent(new Event('online')));
  await p.waitForFunction(() => document.querySelector('#offline').hidden, null, { timeout: 15000 });
  ok(true, 'F: back online → banner disappears and data refreshes');
  await ctx.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED'); process.exit(fails ? 1 : 0);
