package rig

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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

// InitSQLDir reads all .sql files from a directory, sorts them by filename,
// and registers them as SQL init hooks. This is the directory-based equivalent
// of InitSQL — use it with ordered migration files:
//
//	rig.Postgres().InitSQLDir("./migrations")
//
// The directory is resolved relative to the working directory at call time.
// Panics if the directory cannot be read.
func (d *PostgresDef) InitSQLDir(dir string) *PostgresDef {
	if !filepath.IsAbs(dir) {
		wd, err := os.Getwd()
		if err != nil {
			panic("rig: InitSQLDir: " + err.Error())
		}
		dir = filepath.Join(wd, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		panic("rig: InitSQLDir: " + err.Error())
	}
	var stmts []string
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			panic("rig: InitSQLDir: " + err.Error())
		}
		stmts = append(stmts, string(data))
	}
	if len(stmts) > 0 {
		d.hooks.init = append(d.hooks.init, sqlHook{statements: stmts})
	}
	return d
}

// Exec registers an exec init hook that runs a command inside the container
// after it becomes healthy. The command is executed server-side via docker exec.
//
//	rig.Postgres().Exec("psql", "-U", "postgres", "-c", "CREATE EXTENSION pg_trgm")
func (d *PostgresDef) Exec(cmd ...string) *PostgresDef {
	d.hooks.init = append(d.hooks.init, execHook{command: cmd})
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
