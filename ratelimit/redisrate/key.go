package redisratelimit

import "fmt"

// encode builds a prefix-free Redis key from (keyPrefix, key). Length-prefixing
// keyPrefix makes the prefix boundary unambiguous, so distinct (keyPrefix, key)
// tuples never collide: a client-supplied key cannot flatten into another
// namespace (the flatten bug idempotency/redis fixed in v0.8.0). An empty
// keyPrefix still encodes injectively ("0::" + key).
func encode(keyPrefix, key string) string {
	return fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)
}
