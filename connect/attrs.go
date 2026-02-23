package connect

import "fmt"

// Attr is a typed attribute key for use with Endpoint.Attributes.
// The type parameter T indicates the expected value type.
// Use string(a) to get the raw key name.
type Attr[T any] string

// Get retrieves the attribute value from the endpoint.
// Returns the zero value and false if the key is not present.
// Panics if the key exists but has the wrong type — this indicates a
// bug in the producer (server) or consumer (client helper).
func (a Attr[T]) Get(ep Endpoint) (T, bool) {
	v, ok := ep.Attributes[string(a)]
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("rig: attribute %q has type %T, want %T", string(a), v, t))
	}
	return t, true
}

// MustGet retrieves the attribute value, panicking if missing or wrong type.
func (a Attr[T]) MustGet(ep Endpoint) T {
	v, ok := a.Get(ep)
	if !ok {
		panic(fmt.Sprintf("rig: attribute %q not found", string(a)))
	}
	return v
}

// Set writes the attribute value into a map (typically Endpoint.Attributes).
func (a Attr[T]) Set(m map[string]any, v T) {
	m[string(a)] = v
}

// Well-known Postgres attributes.
var (
	PGHost     = Attr[string]("PGHOST")
	PGPort     = Attr[string]("PGPORT")
	PGUser     = Attr[string]("PGUSER")
	PGPassword = Attr[string]("PGPASSWORD")
	PGDatabase = Attr[string]("PGDATABASE")
)

// Well-known Temporal attributes.
var (
	TemporalAddress   = Attr[string]("TEMPORAL_ADDRESS")
	TemporalNamespace = Attr[string]("TEMPORAL_NAMESPACE")
)

// Well-known Redis attributes.
var (
	RedisURL = Attr[string]("REDIS_URL")
)

// Cross-cutting attributes.
var (
	// Secure indicates the endpoint requires TLS or equivalent.
	Secure = Attr[bool]("SECURE")
)

// PostgresDSN builds a Postgres connection string from endpoint attributes.
// Uses PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE with sslmode=disable.
func PostgresDSN(ep Endpoint) string {
	host, _ := PGHost.Get(ep)
	port, _ := PGPort.Get(ep)
	user, _ := PGUser.Get(ep)
	pass, _ := PGPassword.Get(ep)
	db, _ := PGDatabase.Get(ep)
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, db)
}
