// Writes the test configurations, all derived from config.yaml.default so they never go stale:
//   main.yaml    the default configuration
//   push.yaml    push notifications on (the VAPID key comes from the environment at start)
//   accent.yaml  push on, an accent colour and only three sports (the 1.14 checks)
//   mock.yaml    KNMI warnings from the local mock MeteoAlarm feed
//   evil.yaml    an extra source with a hostile feed (XSS, bad links)
// Usage: node make-configs.mjs <out-dir> <fixtures-port>
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';

const [out, port = '8099'] = process.argv.slice(2);
if (!out) { console.error('usage: node make-configs.mjs <out-dir> [fixtures-port]'); process.exit(2); }
const base = readFileSync(new URL('../../config.yaml.default', import.meta.url), 'utf8');

function edit(src, pairs) {
  let s = src;
  for (const [a, b] of pairs) {
    const n = s.split(a).length - 1;
    if (n !== 1) throw new Error(`config.yaml.default changed: expected "${a.slice(0, 60)}" once, found ${n}×`);
    s = s.replace(a, b);
  }
  return s;
}
// Only the main test server fetches ransomware.live (it rate-limits per IP); the others switch it off.
const noRansomware = ['ransomware:\n  enabled: true\n', 'ransomware:\n  enabled: false\n'];
const push = [['push:\n  enabled: false\n', 'push:\n  enabled: true\n'],
  ['  subject: "mailto:you@example.nl"', '  subject: "mailto:e2e@example.nl"']];

mkdirSync(out, { recursive: true });
const files = {
  'main.yaml': base,
  'push.yaml': edit(base, [...push, noRansomware]),
  'accent.yaml': edit(base, [...push, noRansomware,
    ['  accent: ""  ', '  accent: "#00a4dc"'],
    ['  sports: [f1, road, mtb, athletics, football]', '  sports: [f1, mtb, athletics]']]),
  'mock.yaml': edit(base, [noRansomware, ['    nl: "https://feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-netherlands"',
    `    nl: "http://127.0.0.1:${port}/mock/nl.xml"`]]),
  'evil.yaml': edit(base, [noRansomware, ['\nsources:\n', `\nsources:\n  - { id: evil, name: "Evil <b>feed</b>", category: nl, url: "http://127.0.0.1:${port}/evil/evil.xml", lang: nl }\n`]]),
};
for (const [name, text] of Object.entries(files)) writeFileSync(`${out}/${name}`, text);
console.log(`test configs written to ${out}: ${Object.keys(files).join(', ')}`);
