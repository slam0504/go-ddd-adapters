package redisidempotency

import "github.com/redis/go-redis/v9"

// All three scripts touch exactly KEYS[1] (the single composite key). This
// single-key invariant is what lets the adapter run unchanged on
// *redis.ClusterClient / *redis.Ring: a Lua EVAL may only span one hash slot,
// and one key trivially lands in one slot (see doc.go). Do NOT add a second
// key to any script without a shared {…} hash tag.

// beginScript reserves (or reads) the record. KEYS[1]=composite key;
// ARGV[1]=fingerprint, ARGV[2]=new lease token, ARGV[3]=lease TTL (ms).
// Returns {status, token, response}: status is one of new/inprogress/
// completed/mismatch; token is set only for "new"; response only for
// "completed".
var beginScript = redis.NewScript(`
local e = redis.call('HGETALL', KEYS[1])
if #e == 0 then
  redis.call('HSET', KEYS[1], 'fp', ARGV[1], 'st', 'P', 'tok', ARGV[2])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return {'new', ARGV[2], ''}
end
local h = {}
for i = 1, #e, 2 do h[e[i]] = e[i+1] end
if h['fp'] ~= ARGV[1] then return {'mismatch', '', ''} end
if h['st'] == 'P' then return {'inprogress', '', ''} end
return {'completed', '', h['resp'] or ''}
`)

// finishScript marks the reservation completed and stores its response.
// KEYS[1]=composite key; ARGV[1]=lease token, ARGV[2]=response,
// ARGV[3]=retention (ms). Returns 0 when applied, -1 on conflict (vanished /
// already-completed / stale-or-forged token).
var finishScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'st')
if not st then return -1 end
if st == 'C' then return -1 end
if redis.call('HGET', KEYS[1], 'tok') ~= ARGV[1] then return -1 end
redis.call('HSET', KEYS[1], 'st', 'C', 'resp', ARGV[2])
redis.call('HDEL', KEYS[1], 'tok')
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 0
`)

// cancelScript releases an in-progress reservation. KEYS[1]=composite key;
// ARGV[1]=lease token. Returns 0 when released, -1 on conflict. A completed
// record is never deleted (CancelDoesNotDeleteCompleted) — it conflicts
// instead, leaving the stored response intact.
var cancelScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'st')
if not st then return -1 end
if st == 'C' then return -1 end
if redis.call('HGET', KEYS[1], 'tok') ~= ARGV[1] then return -1 end
redis.call('DEL', KEYS[1])
return 0
`)
