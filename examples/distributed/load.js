// Sends 60 requests/s for 10s with one API key, alternating between two app
// instances, and checks that the shared limit (20/s, burst 20) holds.
import http from 'k6/http';
import { Counter } from 'k6/metrics';

const allowed = new Counter('allowed');
const limited = new Counter('limited');
const perInstance = { app1: new Counter('allowed_app1'), app2: new Counter('allowed_app2') };

const RATE = 60, SECONDS = 10, LIMIT = 20, BURST = 20;

export const options = {
  scenarios: {
    load: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: `${SECONDS}s`, preAllocatedVUs: 20 },
  },
  thresholds: {
    // Never more than burst + rate × time across both instances (+1 s slack
    // for requests in flight at the edges).
    allowed: [`count<=${BURST + LIMIT * (SECONDS + 1)}`],
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
  const host = __ITER % 2 === 0 ? 'app1' : 'app2';
  const res = http.get(`http://${host}:8080/work`, { headers: { 'X-API-Key': 'shared-key' } });
  if (res.status === 200) {
    allowed.add(1);
    perInstance[host].add(1);
  } else if (res.status === 429) {
    limited.add(1);
  }
}
