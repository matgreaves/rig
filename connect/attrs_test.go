package connect

import "testing"

func TestAttr_Get(t *testing.T) {
	ep := Endpoint{
		Attributes: map[string]any{
			"PGHOST": "127.0.0.1",
			"COUNT":  42,
		},
	}

	// Present, correct type.
	v, ok := PGHost.Get(ep)
	if !ok || v != "127.0.0.1" {
		t.Errorf("PGHost.Get = (%q, %v), want (127.0.0.1, true)", v, ok)
	}

	// Missing key.
	v, ok = PGPort.Get(ep)
	if ok || v != "" {
		t.Errorf("PGPort.Get = (%q, %v), want ('', false)", v, ok)
	}

}

func TestAttr_Get_WrongType_Panics(t *testing.T) {
	ep := Endpoint{
		Attributes: map[string]any{
			"COUNT": 42,
		},
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("Get did not panic on wrong type")
		}
	}()
	Attr[string]("COUNT").Get(ep)
}

func TestAttr_Get_NilAttributes(t *testing.T) {
	ep := Endpoint{}
	v, ok := PGHost.Get(ep)
	if ok || v != "" {
		t.Errorf("PGHost.Get on nil attrs = (%q, %v), want ('', false)", v, ok)
	}
}

func TestAttr_MustGet(t *testing.T) {
	ep := Endpoint{
		Attributes: map[string]any{
			"PGHOST": "localhost",
		},
	}
	if v := PGHost.MustGet(ep); v != "localhost" {
		t.Errorf("MustGet = %q, want localhost", v)
	}
}

func TestAttr_MustGet_Panics(t *testing.T) {
	ep := Endpoint{}
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustGet did not panic on missing attribute")
		}
	}()
	PGHost.MustGet(ep)
}

func TestAttr_Set(t *testing.T) {
	m := make(map[string]any)
	PGHost.Set(m, "10.0.0.1")
	if m["PGHOST"] != "10.0.0.1" {
		t.Errorf("Set: m[PGHOST] = %v, want 10.0.0.1", m["PGHOST"])
	}
}

func TestAttr_String(t *testing.T) {
	if s := string(PGHost); s != "PGHOST" {
		t.Errorf("string(PGHost) = %q, want PGHOST", s)
	}
	if s := string(TemporalAddress); s != "TEMPORAL_ADDRESS" {
		t.Errorf("string(TemporalAddress) = %q, want TEMPORAL_ADDRESS", s)
	}
}

func TestAttr_Bool(t *testing.T) {
	ep := Endpoint{
		Attributes: map[string]any{
			"SECURE": true,
		},
	}
	v, ok := Secure.Get(ep)
	if !ok || !v {
		t.Errorf("Secure.Get = (%v, %v), want (true, true)", v, ok)
	}

	// Missing returns false (zero value).
	ep2 := Endpoint{}
	v, ok = Secure.Get(ep2)
	if ok || v {
		t.Errorf("Secure.Get on empty = (%v, %v), want (false, false)", v, ok)
	}
}

func TestPostgresDSN(t *testing.T) {
	ep := Endpoint{
		Attributes: map[string]any{
			"PGHOST":     "127.0.0.1",
			"PGPORT":     "5432",
			"PGUSER":     "postgres",
			"PGPASSWORD": "postgres",
			"PGDATABASE": "testdb",
		},
	}
	want := "postgres://postgres:postgres@127.0.0.1:5432/testdb?sslmode=disable"
	if got := PostgresDSN(ep); got != want {
		t.Errorf("PostgresDSN = %q, want %q", got, want)
	}
}

func TestPostgresDSN_Missing(t *testing.T) {
	ep := Endpoint{}
	want := "postgres://:@:/?sslmode=disable"
	if got := PostgresDSN(ep); got != want {
		t.Errorf("PostgresDSN = %q, want %q", got, want)
	}
}
