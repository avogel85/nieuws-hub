// A tiny static file server for the test fixtures (mock MeteoAlarm feed, hostile RSS feed).
// Before serving, the dates in the fixtures are moved to "now", so they never age out:
//   mock/nl.xml   Limburg's warning expired an hour ago, all others active now
//   evil/evil.xml items published in the last half hour
// Usage: node serve-fixtures.mjs <port>
import { createServer } from 'node:http';
import { readFileSync } from 'node:fs';

const port = Number(process.argv[2] || 8099);
const at = h => new Date(Date.now() + h * 3600e3).toISOString().replace(/\.\d{3}Z$/, '+00:00');
function nlXml() {
  return readFileSync(new URL('./fixtures/mock/nl.xml', import.meta.url), 'utf8').replace(/<entry>[\s\S]*?<\/entry>/g, e => {
    const [on, off] = /Limburg/.test(e) ? [-5, -1] : [-2, 10];
    return e.replace(/(<cap:(?:onset|effective)>)[^<]*/g, `$1${at(on)}`).replace(/(<cap:expires>)[^<]*/g, `$1${at(off)}`)
      .replace(/(<(?:updated|published)>)[^<]*/g, `$1${at(-2)}`);
  });
}
function evilXml() {
  let i = 0;
  return readFileSync(new URL('./fixtures/evil/evil.xml', import.meta.url), 'utf8')
    .replace(/<pubDate>[^<]*<\/pubDate>/g, () => `<pubDate>${new Date(Date.now() - (10 + 20 - i++) * 60e3).toUTCString()}</pubDate>`);
}
const routes = { '/mock/nl.xml': nlXml, '/evil/evil.xml': evilXml };
createServer((req, res) => {
  const f = routes[req.url.split('?')[0]];
  if (!f) { res.writeHead(404).end(); return; }
  res.writeHead(200, { 'Content-Type': 'application/xml; charset=utf-8', 'Cache-Control': 'no-store' }).end(f());
}).listen(port, '127.0.0.1', () => console.log(`fixtures on http://127.0.0.1:${port}/`));
