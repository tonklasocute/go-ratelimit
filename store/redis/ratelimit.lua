-- go-ratelimit: apply one or more rules to a key atomically.
--
-- Redis runs a script to completion without interleaving other commands, so
-- the read-decide-write sequence below cannot race with other app instances.
-- Every rule is checked first; state is written only if all rules allow the
-- request (all-or-nothing), so a denied request consumes nothing.
--
-- KEYS[i]       state of rule i. All keys share a {hash tag}, so they live in
--               one Redis Cluster slot.
-- ARGV[1]       n, the number of events requested (0 = peek)
-- ARGV[2]       now in microseconds, or "" to use the server's TIME
-- ARGV[3 + 4(i-1) ...] algorithm id, limit, period (µs), burst of rule i
--
-- Returns, per rule: allowed (0/1), limit, remaining, reset_after (µs),
-- retry_after (µs).
--
-- The formulas mirror the Go package github.com/tonklasocute/go-ratelimit/algorithm.
-- Lua numbers are doubles; microsecond timestamps (~1.8e15) are exact.

if redis.replicate_commands then
  redis.replicate_commands() -- needed before TIME on Redis < 5
end

local n = tonumber(ARGV[1])
local now
if ARGV[2] ~= "" then
  now = tonumber(ARGV[2])
else
  local t = redis.call("TIME")
  now = tonumber(t[1]) * 1000000 + tonumber(t[2])
end

local EPS = 1e-9

-- Round up so that waiting retry_after is always enough.
local function ceil_us(x)
  if x <= 0 then return 0 end
  return math.ceil(x - 1e-6)
end

local function floor_int(x)
  if x <= 0 then return 0 end
  return math.floor(x + EPS)
end

-- Numbers passed to redis.call are formatted with %.14g; format them exactly.
local function int(x) return string.format("%.0f", x) end
local function float(x) return string.format("%.17g", x) end

local function load(key)
  local v = redis.call("HMGET", key, "a", "b", "c", "f")
  return tonumber(v[1]) or 0, tonumber(v[2]) or 0, tonumber(v[3]) or 0, tonumber(v[4]) or 0
end

-- Each algorithm returns a decision {allowed, limit, remaining, reset, retry}
-- and a function that commits n events.
local algorithms = {}

-- 1: fixed window counter. a = window start, b = count.
algorithms[1] = function(key, limit, period, burst)
  local a, b = load(key)
  local start = now - math.fmod(now, period)
  if start > a then a, b = start, 0 end
  local reset = a + period - now
  local d = {0, limit, 0, reset, 0}
  if b + n <= limit then
    d[1], d[3] = 1, limit - b - n
  else
    d[3] = limit - b
    if n <= limit then d[5] = reset end
  end
  return d, function() redis.call("HSET", key, "a", int(a), "b", int(b + n)) end
end

-- 2: sliding window log. A sorted set of timestamps.
algorithms[2] = function(key, limit, period, burst)
  redis.call("ZREMRANGEBYSCORE", key, "-inf", int(now - period))
  local count = redis.call("ZCARD", key)
  local d = {0, limit, 0, 0, 0}
  if count > 0 then
    local last = redis.call("ZRANGE", key, -1, -1, "WITHSCORES")
    d[4] = tonumber(last[2]) + period - now
  end
  if count + n <= limit then
    d[1], d[3] = 1, limit - count - n
    if n > 0 then d[4] = math.max(d[4], period) end
  else
    d[3] = limit - count
    if n <= limit then
      local k = count + n - limit -- the k-th oldest entry must expire first
      local e = redis.call("ZRANGE", key, k - 1, k - 1, "WITHSCORES")
      d[5] = tonumber(e[2]) + period - now
    end
  end
  return d, function()
    local seq = count
    for _ = 1, n do
      local member
      repeat
        seq = seq + 1
        member = int(now) .. ":" .. seq
      until not redis.call("ZSCORE", key, member)
      redis.call("ZADD", key, int(now), member)
    end
  end
end

-- 3: sliding window counter. a = window start, b = current, c = previous.
algorithms[3] = function(key, limit, period, burst)
  local a, b, c = load(key)
  local start = now - math.fmod(now, period)
  if start > a then
    if start - period == a then c = b else c = 0 end
    a, b = start, 0
  end
  local elapsed = math.max(now - a, 0)
  local est = c * (period - elapsed) / period + b
  local d = {0, limit, 0, 0, 0}
  local current = b
  if est + n <= limit + EPS then
    d[1], d[3] = 1, floor_int(limit - est - n)
    current = b + n
  else
    d[3] = floor_int(limit - est)
    if n <= limit then
      if b + n <= limit then
        d[5] = ceil_us(period - (limit - b - n) * period / c - (now - a))
      else
        local e = math.max(period - (limit - n) * period / b, 0)
        d[5] = ceil_us(a + period - now + e)
      end
    end
  end
  if current > 0 then
    d[4] = a + 2 * period - now
  elseif c > 0 then
    d[4] = a + period - now
  end
  return d, function() redis.call("HSET", key, "a", int(a), "b", int(b + n), "c", int(c)) end
end

-- 4: token bucket. a = last refill, f = tokens.
algorithms[4] = function(key, limit, period, burst)
  local a, _, _, f = load(key)
  if a == 0 then
    a, f = now, burst
  elseif now > a then
    f = math.min(burst, f + (now - a) * limit / period)
    a = now
  end
  local wait = math.max(a - now, 0)
  local per_token = period / limit
  local d = {0, burst, 0, 0, 0}
  local tokens = f
  if tokens + EPS >= n then
    d[1] = 1
    tokens = tokens - n
  elseif n <= burst then
    d[5] = ceil_us(wait + (n - tokens) * per_token)
  end
  d[3] = floor_int(tokens)
  d[4] = ceil_us(wait + (burst - tokens) * per_token)
  return d, function() redis.call("HSET", key, "a", int(a), "f", float(math.max(f - n, 0))) end
end

-- 5: leaky bucket as a meter. a = last leak, f = level.
algorithms[5] = function(key, limit, period, burst)
  local a, _, _, f = load(key)
  if now > a then
    f = math.max(f - (now - a) * limit / period, 0)
    a = now
  end
  local wait = math.max(a - now, 0)
  local per_unit = period / limit
  local d = {0, burst, 0, 0, 0}
  local level = f
  if level + n <= burst + EPS then
    d[1] = 1
    level = level + n
  elseif n <= burst then
    d[5] = ceil_us(wait + (level + n - burst) * per_unit)
  end
  d[3] = floor_int(burst - level)
  d[4] = ceil_us(wait + level * per_unit)
  return d, function() redis.call("HSET", key, "a", int(a), "f", float(f + n)) end
end

-- 6: GCRA. a = theoretical arrival time (TAT).
algorithms[6] = function(key, limit, period, burst)
  local stored = load(key)
  local interval = math.ceil(period / limit)
  local tolerance = burst * interval
  local tat = math.max(stored, now)
  local new_tat = tat + n * interval
  local allow_at = new_tat - tolerance
  local d = {0, burst, 0, 0, 0}
  if now >= allow_at then
    d[1] = 1
    d[3] = math.floor((now - allow_at) / interval)
    d[4] = new_tat - now
  else
    if n <= burst then d[5] = allow_at - now end
    d[3] = math.floor(math.max(now - (tat - tolerance), 0) / interval)
    d[4] = tat - now
  end
  return d, function() redis.call("HSET", key, "a", int(new_tat)) end
end

local decisions, commits = {}, {}
local allowed = true
for i = 1, #KEYS do
  local base = 3 + (i - 1) * 4
  local alg = algorithms[tonumber(ARGV[base])]
  if not alg then
    return redis.error_reply("go-ratelimit: unknown algorithm " .. ARGV[base])
  end
  local d, commit = alg(KEYS[i], tonumber(ARGV[base + 1]), tonumber(ARGV[base + 2]), tonumber(ARGV[base + 3]))
  decisions[i], commits[i] = d, commit
  if d[1] == 0 then allowed = false end
end

if allowed and n > 0 then
  for i = 1, #KEYS do
    commits[i]()
    -- The state is fresh again after reset_after, so let Redis drop it then.
    redis.call("PEXPIRE", KEYS[i], math.max(math.ceil(decisions[i][4] / 1000), 1))
  end
end

local out = {}
for i = 1, #KEYS do
  for j = 1, 5 do out[#out + 1] = decisions[i][j] end
end
return out
