// Sends 60 requests/s for 20s with one API key, alternating between two app
// instances, and checks that the shared limit (20/s, burst 20) holds across
// both: about BURST + LIMIT × SECONDS requests succeed in total, not twice
// that. Every request is also written to /results/k6.csv (see compose) so
// per-second counts can be checked with per-second.sh.
import http from 'k6/http';
import exec from 'k6/execution';
import { Counter } from 'k6/metrics';

const RATE = 60, SECONDS = 20, LIMIT = 20, BURST = 20;

const allowed = new Counter('allowed');
const limited = new Counter('limited');
const allowedBy = { app1: new Counter('allowed_app1'), app2: new Counter('allowed_app2') };

export const options = {
  scenarios: {
    load: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: `${SECONDS}s`, preAllocatedVUs: 30 },
  },
  thresholds: {
    // At most burst + rate × time across both instances (+1 s of slack for
    // requests in flight at the edges), and not far below it either.
    allowed: [`count<=${BURST + LIMIT * (SECONDS + 1)}`, `count>=${Math.floor(LIMIT * SECONDS * 0.95)}`],
    limited: ['count>0'],
  },
};

export function setup() {
  // Wait until both instances answer.
  for (const host of ['app1', 'app2']) {
    for (let i = 0; i < 30; i++) {
      if (http.get(`http://${host}:8080/work`, { headers: { 'X-API-Key': 'warmup' } }).status === 200) break;
    }
  }
}

export default function () {
  // Strict alternation across all VUs: even requests to app1, odd to app2.
  const host = exec.scenario.iterationInTest % 2 === 0 ? 'app1' : 'app2';
  const res = http.get(`http://${host}:8080/work`, { headers: { 'X-API-Key': 'shared-key' } });
  if (res.status === 200) {
    allowed.add(1);
    allowedBy[host].add(1);
  } else if (res.status === 429) {
    limited.add(1);
  }
}

export function handleSummary(data) {
  const count = (name) => (data.metrics[name] ? data.metrics[name].values.count : 0);
  const ok = count('allowed'), denied = count('limited');
  const sustained = (ok - BURST) / SECONDS;
  const failed = Object.values(data.metrics).some((m) => m.thresholds && Object.values(m.thresholds).some((t) => !t.ok));
  const lines = [
    '',
    'Shared rate limit across 2 instances',
    `  configured        ${LIMIT} req/s per API key, burst ${BURST}`,
    `  offered           ${ok + denied} requests in ${SECONDS}s (${RATE} req/s)`,
    `  allowed           ${ok}  (app1 ${count('allowed_app1')}, app2 ${count('allowed_app2')})`,
    `  rejected (429)    ${denied}`,
    `  expected allowed  ≈ ${BURST + LIMIT * SECONDS} (burst + limit × duration)`,
    `  sustained rate    ${sustained.toFixed(2)} req/s after the burst`,
    `  thresholds        ${failed ? 'FAILED' : 'passed'}`,
    '',
  ];
  return { stdout: lines.join('\n') };
}
