# vault — the row whose payload only Go may write

This settles `docs/special-cases.md`'s vault case. `secrets` is a table where
`ciphertext`, `nonce` and `key_version` — the entire payload, everything a
caller who found this endpoint would actually be there for — are
`schema.Hidden`. The owner is polymorphic: `owner_kind` and `owner_id` name a
row in one of several tables this module does not import, so there is no
`schema.Ref` for it, only two plain filterable columns.

Run it:

```bash
mise run pg-up   # once, if it is not already running
cd example/vault
SQLB_TEST_POSTGRES='postgres://sqlb:sqlb@localhost:15432/sqlb?sslmode=disable' go test ./... -v -race
```

## The finding

`example/blog`'s `Author.password_hash` already shows that `Hidden` removes
one column from a generated REST response and from the typed predicate
facade, while the table keeps `CRUD | OpList` because `email` and `name` are
still worth writing through a generated body. `vault.Secret` is the same
declaration pushed to its limit: every payload column is `Hidden`, so once
`owner_kind` and `owner_id` are set there is nothing left for a generated
create body to accept. The schema reflects that by leaving `OpCreate` and
`OpUpdate` out of `Expose` entirely —

```go
Expose(schema.REST{
    Path: "/secrets",
    Ops:  schema.OpRead | schema.OpList,
})
```

— and the consequence is not "the create body is empty", it is **the route
does not exist**. `rest_gen.go` mounts:

```go
rest.Resource[Secret, rest.None[Secret], rest.None[Secret]](api, db, rest.Options{
    Path: "/secrets",
    Ops:  rest.OpRead | rest.OpList,
    ...
})
```

`rest.None[T]` is the package's own stand-in for "no body type", used exactly
because `Ops` never grants the operation it would belong to (see its doc
comment in `rest/rest.go`). There is no `SecretCreate` and no `SecretUpdate`
REST type anywhere in the generated output — not an empty struct, not a
struct with only the owner fields, nothing. `TestNoGeneratedCreateRoute` and
`TestOpenAPIDocumentNeverMentionsThePayload` in `server_test.go` are the
proof: a `POST /secrets` is a 404/405, not a 200 that silently wrote nothing,
and the OpenAPI document has no `post` under `/secrets` at all.

That is the one genuinely new thing this example adds over `blog`'s existing
`Hidden` coverage — not the capability, which was already the best-tested one
in the repository, but what a generated surface looks like once *every*
payload column carries it, plus the polymorphic-owner shape neither `Ref` nor
`ExternalRef` fits.

### A third thing, and it is easy to conflate with the other two

`columns_gen.go` still emits `SecretUpdate`, a typed wrapper around
`sqlb.UpdateRows[Secret]`, and it *does* have `SetCiphertext`, `SetNonce` and
`SetKeyVersion` methods that compile and run
(`TestTypedUpdateFacadeStillCarriesHiddenColumns`). That is not a leak: this
wrapper is a Go-level convenience over the query engine, not a REST body, and
it is exactly the trusted-code path a real key rotation would call. What
`Hidden` strips from codegen is two different things — the REST create/update
DTOs (gone completely here, because `Ops` grants neither) and `SecretCols`,
the typed *predicate* facade a filter or a sort reaches through. `SecretCols`
has no `Ciphertext`, `Nonce` or `KeyVersion` field, checked directly by
reflection in `TestHiddenColumnsUnreachableByReflection`. The typed Update
setter facade was never on that list, and does not need to be: setting a
column is not the oracle `Field.Hidden`'s doc comment warns about —
predicating on one, or serving it back, is.

## The write path

`store.go`'s `Encrypt` is what a generated create body would have been, had
there been anything left for one to accept:

```go
func Encrypt(ctx context.Context, db sqlb.Executor, ownerKind, ownerID string, plaintext []byte) (*Secret, error)
```

It builds a `Secret` in Go and calls `sqlb.InsertRows(s).One(ctx, db)`
directly — the same call `example/blog`'s own tests use to set
`password_hash`, just now the only door in rather than a documented
exception. `Decrypt` is the read-side mirror: `Hidden` blocks a REST response
and the typed predicate facade, not a `sqlb.Query[Secret]` a trusted Go
caller in this process makes for itself, which is what
`TestEncryptRoundTrips` exercises.

The "encryption" is XOR against a published, hard-coded key
(`store.go`'s `devKey`) — **this is not a cipher**. Anyone holding the
ciphertext and this file recovers the plaintext trivially. It exists only so
the write path has something to call that is not a no-op; a real vault sends
the plaintext to a real KMS or an AEAD keyed by a secret this process never
holds, and uses `key_version` to mean something across an actual rotation
rather than being fixed at `1` forever.

## Deviations from the census's literal shape, and why

- **`ciphertext` and `nonce` are `schema.Bytes` (`bytea`), not
  `schema.Text`.** A XOR of arbitrary plaintext against a fixed-length key
  is not, in general, valid UTF-8, and Postgres `text` refuses to store what
  isn't. Storing binary output in a binary column needed no encoding step
  and no risk of an insert that fails only for some plaintexts; a real
  AEAD's output has the identical property, so `bytea` is also what it
  would need.
- **The write function is `Encrypt`/`Decrypt`, not `Store`, and it takes and
  returns `[]byte`, not `string`.** Ciphertext is binary by the above, so a
  `[]byte` in and out avoids a needless string conversion at the one call
  site that matters; `Encrypt`/`Decrypt` names what the pair of functions
  actually do, symmetrically, which `Store` alone does not have a
  counterpart for.

Both are substitutions of one working design for another; nothing about the
core claim — a `Hidden` payload leaves a generated surface with a read side
and no write side — depends on either choice.

## Deliberately not

- **A real KMS, or key rotation.** `key_version` is carried on every row and
  fixed at `1`; wiring it to an actual rotating key is the next thing a real
  vault needs and no part of what this example is testing.
- **An access-log-on-read hook.** The census this example settles mentions
  logging every read of a secret. That is a real feature and a natural
  follow-up — a `AfterQuery`-shaped hook recording who read which row — but
  it is a separate question from what a generated surface can write, which
  is what this example is actually about, and adding it here would blur the
  one finding into two.
