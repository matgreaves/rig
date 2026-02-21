package rig

import "context"

// PostgresDef defines a service backed by the builtin Postgres type.
// Rig manages the database name, user, and password — the API is minimal.
type PostgresDef struct {
	image    string
	egresses map[string]egressDef
	hooks    hooksDef
}

func (*PostgresDef) rigService() {}

// Postgres creates a Postgres service definition. The database name is
// derived from the service name in the environment, and user/password
// default to "postgres"/"postgres".
//
//	rig.Postgres()
//	rig.Postgres().Image("postgres:15")
func Postgres() *PostgresDef {
	return &PostgresDef{}
}

// Image overrides the default Postgres Docker image (postgres:16-alpine).
func (d *PostgresDef) Image(image string) *PostgresDef {
	d.image = image
	return d
}

// Egress adds a dependency on a service, named after the target.
func (d *PostgresDef) Egress(service string, ingress ...string) *PostgresDef {
	return d.EgressAs(service, service, ingress...)
}

// EgressAs adds a dependency with a custom local name.
func (d *PostgresDef) EgressAs(name, service string, ingress ...string) *PostgresDef {
	if d.egresses == nil {
		d.egresses = make(map[string]egressDef)
	}
	eg := egressDef{service: service}
	if len(ingress) > 0 {
		eg.ingress = ingress[0]
	}
	d.egresses[name] = eg
	return d
}

// InitSQL registers SQL statements to run via psql after the database is
// healthy. Statements are executed server-side via docker exec — no SQL
// driver needed in the test process. Can be called multiple times.
//
//	rig.Postgres().InitSQL("CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL)")
func (d *PostgresDef) InitSQL(statements ...string) *PostgresDef {
	d.hooks.init = append(d.hooks.init, sqlHook{statements: statements})
	return d
}

// InitHook registers a client-side init hook function.
func (d *PostgresDef) InitHook(fn func(ctx context.Context, w Wiring) error) *PostgresDef {
	d.hooks.init = append(d.hooks.init, hookFunc(fn))
	return d
}

// PrestartHook registers a client-side prestart hook function.
func (d *PostgresDef) PrestartHook(fn func(ctx context.Context, w Wiring) error) *PostgresDef {
	d.hooks.prestart = append(d.hooks.prestart, hookFunc(fn))
	return d
}
