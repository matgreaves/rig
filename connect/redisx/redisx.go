// Package redisx provides a Redis client built on rig endpoints.
//
// In tests, construct from a resolved environment endpoint:
//
//	rdb := redisx.Connect(env.Endpoint("redis"))
//	defer rdb.Close()
//
// In service code, construct from parsed wiring:
//
//	w, _ := connect.ParseWiring(ctx)
//	rdb := redisx.Connect(w.Egress("redis"))
package redisx

import (
	"github.com/matgreaves/rig/connect"
	"github.com/redis/go-redis/v9"
)

// URL extracts the REDIS_URL attribute from the endpoint.
func URL(ep connect.Endpoint) string {
	v, _ := connect.RedisURL.Get(ep)
	return v
}

// Connect creates a Redis client from a rig endpoint.
// It reads REDIS_URL from the endpoint attributes and parses it
// to configure the client. An optional *redis.Options can be provided
// to override defaults; Addr and DB are always set from the endpoint URL.
func Connect(ep connect.Endpoint, opts ...*redis.Options) *redis.Client {
	url := URL(ep)
	if url == "" {
		return redis.NewClient(&redis.Options{})
	}

	parsed, err := redis.ParseURL(url)
	if err != nil {
		// Fall back to a default client if the URL is malformed.
		return redis.NewClient(&redis.Options{})
	}

	// Merge user-provided options, preserving Addr and DB from the URL.
	if len(opts) > 0 && opts[0] != nil {
		o := opts[0]
		o.Addr = parsed.Addr
		o.DB = parsed.DB
		if o.Network == "" {
			o.Network = parsed.Network
		}
		return redis.NewClient(o)
	}

	return redis.NewClient(parsed)
}
