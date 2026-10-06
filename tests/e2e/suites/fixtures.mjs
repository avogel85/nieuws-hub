// Fixed API answers for sources that rate-limit or need a key, with dates moved to "now".
import { readFileSync } from 'node:fs';
const read = name => JSON.parse(readFileSync(new URL(`../fixtures/${name}`, import.meta.url)));

// ransomware.live (rate-limited per IP): made-up organisations, discovered in the last days
export function ransomware() {
  const rw = read('ransomware.json');
  rw.sources[0].fetched_at = new Date().toISOString();
  rw.victims.forEach((v, i) => { v.discovered = new Date(Date.now() - (i * 2 + 1) * 864e5).toISOString(); });
  return rw;
}
// NS (needs an API key): two disruptions and two engineering works
export const trains = () => read('trains.json');
