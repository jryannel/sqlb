package vault_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/vault"
	_ "github.com/jryannel/sqlb/example/vault/vaultschema"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// The bootstrap below is example/fxapp/main_test.go's freshDatabase, copied
// rather than imported: fxapp is a separate module and this one is too, so
// there is nothing to import it from.

const pgEnv = "SQLB_TEST_POSTGRES"

var (
	once     sync.Once
	admin    *pgxpool.Pool
	dsnFor   func(database string) string
	startErr error
)

func startPostgres() {
	ctx := context.Background()
	base := os.Getenv(pgEnv)
	if base == "" {
		startErr = fmt.Errorf("%s is not set; run `mise run pg-up` first", pgEnv)
		return
	}
	u, err := url.Parse(base)
	if err != nil {
		startErr = fmt.Errorf("%s is not a valid URL: %w", pgEnv, err)
		return
	}
	dsnFor = func(database string) string {
		v := *u
		v.Path = "/" + database
		return v.String()
	}
	if admin, err = pgxpool.New(ctx, dsnFor("postgres")); err != nil {
		startErr = fmt.Errorf("opening the admin connection: %w", err)
		return
	}
	if err := admin.Ping(ctx); err != nil {
		startErr = fmt.Errorf("%s is set but nothing answered: %w", pgEnv, err)
	}
}

func databaseName(t *testing.T) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())
	return "vault_" + name
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func mustExec(t *testing.T, query string) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), query); err != nil {
		t.Fatalf("exec failed: %v\n%s", err, strings.TrimSpace(query))
	}
}

// freshDatabase returns a pool connected to a fresh, migrated database:
// migrate.Diff between nothing and the declared registry, applied statement
// by statement. No goose file, because nothing here needs a second run to
// replay against — this is the baseline every migration example builds on.
func freshDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	once.Do(startPostgres)
	if startErr != nil {
		t.Fatalf("vault: %v", startErr)
	}

	name := databaseName(t)
	mustExec(t, `DROP DATABASE IF EXISTS `+quoteIdent(name))
	mustExec(t, `CREATE DATABASE `+quoteIdent(name))
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
	})

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsnFor(name))
	if err != nil {
		t.Fatalf("connecting to %s: %v", name, err)
	}
	t.Cleanup(pool.Close)

	changes, err := migrate.Diff(nil, schema.DefaultRegistry(), migrate.MinPostgres(18))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, c := range changes {
		if c.Up == "" {
			continue
		}
		if _, err := pool.Exec(ctx, c.Up); err != nil {
			t.Fatalf("applying change %q: %v\n%s", c.Comment, err, c.Up)
		}
	}
	return pool
}

// TestEncryptRoundTrips proves the write path: Encrypt bypasses the
// generated create body (which has nothing to name ciphertext, nonce or
// key_version with — rest_gen.go mounts rest.None[Secret] on both sides) and
// writes through sqlb.InsertRows directly. Reading it back at the Go level
// works exactly as it would for any other row: Hidden gates the REST surface
// and the generated typed-column facade, not a plain query this process
// makes for itself.
func TestEncryptRoundTrips(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	plaintext := []byte("correct horse battery staple")
	s, err := vault.Encrypt(ctx, db, "user", "00000000-0000-0000-0000-000000000001", plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if string(s.Ciphertext) == string(plaintext) {
		t.Fatal("ciphertext equals the plaintext; Encrypt did nothing")
	}

	got, err := sqlb.Query[vault.Secret]().Where(sqlb.F("id").Eq(s.ID)).One(ctx, db)
	if err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if string(vault.Decrypt(&got)) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", vault.Decrypt(&got), plaintext)
	}
}

// TestHiddenColumnsAreAbsentFromTheFacade is example/blog's
// TestHiddenColumnsAreAbsentFromTheFacade, generalised from one column to a
// table whose whole payload is Hidden. The model still maps every column —
// Go code needs to write them — but none of the three payload columns is
// selectable, and none has a case in SecretCols (columns_gen.go has no
// SecretCols.Ciphertext at all; that absence is a compile-time property this
// test cannot exercise directly, so it is asserted the same indirect way
// blog's test does: by column count).
func TestHiddenColumnsAreAbsentFromTheFacade(t *testing.T) {
	model := sqlb.ModelOf[vault.Secret]()
	for _, hidden := range []string{"ciphertext", "nonce", "key_version"} {
		if model.Column(hidden) == nil {
			t.Errorf("model has no column %q; Go code needs to be able to write it", hidden)
		}
	}
	if got, want := len(model.Columns), 8; got != want {
		t.Errorf("model maps %d columns, want %d (id, owner_kind, owner_id, ciphertext, nonce, key_version, created_at, updated_at)", got, want)
	}
	if got, want := len(model.Selectable()), 5; got != want {
		t.Errorf("REST projection has %d columns, want %d (id, owner_kind, owner_id, created_at, updated_at)", got, want)
	}
	for _, col := range model.Selectable() {
		if col.Name == "ciphertext" || col.Name == "nonce" || col.Name == "key_version" {
			t.Errorf("%s is in the REST projection", col.Name)
		}
	}
}

// TestTypedUpdateFacadeStillCarriesHiddenColumns is the correction this
// example exists to make. docs/special-cases.md's vault case, following the
// exchange report, says the typed update facade "omits" a Hidden column the
// way the generated create body does. Measured against what `sqlb generate`
// actually emits, that is not what happened: SecretUpdate.SetCiphertext (and
// SetNonce, SetKeyVersion) exist in columns_gen.go and work. What Hidden
// strips is SecretCols — the predicate-building facade a filter or a sort
// reaches through — and the REST-facing create/update bodies, which
// rest_gen.go never generates at all here because Secret declares neither
// OpCreate nor OpUpdate. The typed Update *setter* facade is a third thing,
// and it was never touched: it is exactly the mechanism a real vault's key
// rotation would call, in-process, the same way Encrypt already does for
// create. "The generated write surface is gone" was the wrong way to say
// what is true here; "the REST-reachable write surface is gone, and the
// in-process one still exists on purpose" is what a reader would find by
// running this.
func TestTypedUpdateFacadeStillCarriesHiddenColumns(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	s, err := vault.Encrypt(ctx, db, "team", "00000000-0000-0000-0000-000000000002", []byte("rotate me"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	rotated, err := vault.UpdateSecret().
		SetCiphertext([]byte("rotated-ciphertext")).
		SetKeyVersion(2).
		Where(sqlb.F("id").Eq(s.ID)).
		Stmt().One(ctx, db)
	if err != nil {
		t.Fatalf("SecretUpdate.SetCiphertext exists in columns_gen.go and should compile and run: %v", err)
	}
	if string(rotated.Ciphertext) != "rotated-ciphertext" || rotated.KeyVersion != 2 {
		t.Errorf("rotated = %+v, want ciphertext/key_version updated", rotated)
	}
}

// TestHiddenColumnsUnreachableByReflection is the compile-time half of
// TestHiddenColumnsAreAbsentFromTheFacade, asked the way the census does: via
// reflect, on what codegen actually emitted, rather than inferred from a
// column count.
//
// There is no SecretCreate or SecretUpdate REST body type to reflect on —
// rest_gen.go instantiates rest.Resource[Secret, rest.None[Secret],
// rest.None[Secret]] because Secret declares neither OpCreate nor OpUpdate,
// and rest.None carries no fields at all. What codegen does still emit is
// SecretCols, the typed predicate facade, and it is the thing this test
// checks: none of the three hidden columns has a field there, so
// `SecretCols.Ciphertext.Eq(...)` is not code that can be written, let alone
// compiled. The row struct's own json tags are the other half — "-" is what
// keeps a hidden column out of a hand-marshalled response too, not just a
// generated one.
func TestHiddenColumnsUnreachableByReflection(t *testing.T) {
	cols := reflect.TypeOf(vault.SecretCols)
	for _, name := range []string{"Ciphertext", "Nonce", "KeyVersion"} {
		if _, ok := cols.FieldByName(name); ok {
			t.Errorf("SecretCols has a field %s; a predicate against a hidden column should not compile", name)
		}
	}

	row := reflect.TypeOf(vault.Secret{})
	for _, name := range []string{"Ciphertext", "Nonce", "KeyVersion"} {
		f, ok := row.FieldByName(name)
		if !ok {
			t.Fatalf("Secret has no field %s; Go code needs to be able to write it", name)
		}
		if tag := f.Tag.Get("json"); tag != "-" {
			t.Errorf("Secret.%s json tag = %q, want %q", name, tag, "-")
		}
	}
}

// TestPolymorphicOwnerHasNoForeignKey is the cost of two plain columns
// instead of a Ref: nothing here stops owner_id naming a row of any kind, in
// any table, that does or does not exist.
func TestPolymorphicOwnerHasNoForeignKey(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	_, err := vault.Encrypt(ctx, db, "user", "ffffffff-ffff-ffff-ffff-ffffffffffff", []byte("orphaned"))
	if err != nil {
		t.Fatalf("a secret owned by a row that does not exist anywhere was refused: %v; "+
			"owner_kind/owner_id is meant to carry no foreign key at all", err)
	}
}
