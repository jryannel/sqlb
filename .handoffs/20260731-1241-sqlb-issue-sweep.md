# Handoff — sqlb-issue-sweep (2026-07-31 12:41)

## Where

- Repo / worktree: `/Users/jryannel/dev/tmp/sqlb/.claude/worktrees/pgvector-pgx-port-99033a`
  (worktree of `/Users/jryannel/dev/tmp/sqlb`, whose `main` is checked out separately)
- Branch: `claude/roundtrip-fixpoint-53` · 1 unshipped commit (`3b1db86`) · working tree clean
- Four branches were produced this session; two are merged, two are open **and both
  currently conflict with `main`** (see *In progress*).

## Goal

Work the open GitHub issues on `jryannel/sqlb`, highest-leverage first. The user's
stated priority order, most recently restated mid-session:

1. **The bootstrap is the multiplier** — `RenderSchema(introspect(db))` must emit all
   69 table declarations so adopting a database is "review 68" rather than "write 68".
2. **Computed fields decide whether the four-surface goal is reached** — projects,
   tasks and work packages are the highest-churn endpoints and are unusable on the
   generated path without them.
3. Then the rest of the roadmap (`#18` actions, `#21` impact).

## Done so far

**Merged to `main`:**

- `2911e6f` — **#17 computed fields** (PR #58). `schema.Computed` with `FromSQL`,
  `Needs` for the per-viewer tier, `Builder.Bind`, `Describe(...).Computed`, a
  generated `ComputedColumns()` method, the mount-time obligation for unsupplied
  binds, `example/computed/declared.go`, and `pgtest/computed_test.go` against
  Postgres 18. **`FromGo` was deliberately not built** — ADR-0041 called it the tier
  most likely to be cut and nothing reached for it. #17 stays open for that tier only.
- `36ab07e` — **#54/#55/#56/#57 adoption fixes** (PR #60). `IndexNamed`/
  `UniqueIndexNamed`, semantic jsonb default comparison, `ExternalRef(...).Enforced()`,
  introspect `Only`/`Exclude`, a skipped column taking its dependents, and
  `sqlb check -database <dsn>` as the drift gate. Also fixed the implicit-index
  resolution order bug found while building it.
- `3696d9a` — **#61**, landed by someone else (codegen import bugs + a compile check).
  Not mine; worth reading before touching `codegen/`, it overlaps the emitters.

**Open PRs (mine):**

- **PR #59** — `sqlb eject` (#19), branch `claude/eject-adr-0042`, head `19015c0`,
  ADR-0042. Emits a pgx+stdlib package (schema.sql, models, store, handlers,
  support, README); what it does not carry is refused by name; `example/blog/ejected/`
  is committed and `pgtest/eject_test.go` compares it byte-for-byte against the
  generated resources.
- **PR #62** — the fixpoint (#53), branch `claude/roundtrip-fixpoint-53`, head
  `3b1db86`. `RenderSchema` writes a `vector` column (**the bootstrap blocker**),
  `schema.Types()` + a test that walks it and compiles the output, index
  `Opclasses`/`With` carried through schema/introspect/DDL/schema-source,
  `FieldDesc.CheckName` so an enum's CHECK keeps its name, and
  `pgtest/fixpoint_test.go` comparing the two **databases** through `pg_catalog`.

## In progress / not finished

- **Both open PRs conflict with `main` and need a rebase.** Nothing is broken; they
  were branched before #58/#60/#61 landed.
  - **PR #59** is the awkward one: it was stacked on the computed-fields branch, and
    #58 was **squash**-merged, so its `c6d4700` is duplicated in `main` under a new
    sha. A plain rebase will conflict on every computed-fields file. Drop that commit
    instead:
    ```bash
    git rebase --onto origin/main c6d4700 claude/eject-adr-0042
    ```
  - **PR #62** needs a plain rebase; the overlap with #60 is
    `codegen/schemasrc.go`, `introspect/build.go`, `introspect/introspect.go`,
    `migrate/ddl.go`, `schema/field.go`, `schema/table.go`,
    `docs/migrations/adopting.md`. Expect small, mechanical conflicts (both sides add
    adjacent doc paragraphs and adjacent struct fields).
    ```bash
    git rebase origin/main   # on claude/roundtrip-fixpoint-53
    ```
  - After either rebase: re-run `mise run generate-check` and `cd pgtest && go test ./...`
    before force-pushing.
- **#18 (declared actions) — requested, ADR first, nothing written.** The user asked
  twice; the second request was interrupted by this handoff. `TaskCreate` #14 tracks
  it. Research was done and is worth not repeating — see *Design already settled* below.

## Next steps

1. Rebase PR #62 onto `main`, verify, force-push.
2. Rebase PR #59 with `--onto` as above (dropping the merged computed commit), verify,
   force-push.
3. Write **ADR-0043** for #18, then implement. Do not start the code first — the user
   explicitly asked for the ADR first, twice.

## Design already settled for #18 (do not re-derive)

Research done this session, before the interruption:

- **The trap, from `docs/vision.md` and the issue itself:** a DSL that tries to
  *express* the transition will be fought and ejected. Generate the **envelope only**
  and call a plain Go func — the seam `BeforeCreate` already uses.
- **Declaration:** `TableDef.Action(schema.Action{...})` returning `*TableDef`, mirroring
  the established `Expose(schema.REST{…})` / `AddIndex(schema.Index{…})` idiom.
  The issue's `.Body[CompleteTaskInput]()` is not valid Go (methods cannot take type
  parameters), so the spelling has to change regardless.
- **The body is declared in the DSL** (`schema.Text("note").Nullable()`, reusing the
  field vocabulary), *not* reflected from an app Go type. Reason: the value of #18 is
  that verbs enter the **client** emitters (TS/Dart/CLI/OpenAPI), and those read the
  declaration; reflecting an app type would also invert the dependency — models are
  generated *from* the schema.
- **`Do` is bound at registration, not in the schema.** Generated `Register` takes an
  `Actions` struct when any action is declared, so the *compiler* asks for the func;
  a nil field is refused at mount, ADR-0030's shape. Two layers, compiler then startup.
- **Envelope:** parse id → scoped fetch through `BeforeQuery` (this is the ADR-0030
  inheritance the issue names) → 404 → decode body → `Do(ctx, *T, In) error` inside
  the transaction (`rest/tx.go`'s `writer`/`write`) → persist the declared `Writes`
  columns → 200 with the row. Lock the row (`FOR UPDATE`) when `Writes` is non-empty.
- **Collection actions** (path without `{id}`): no fetch, `func(ctx, In) error`, 204.
- **Errors:** a `Do` returning `*rest.Problem` maps to its status — that is the escape
  hatch for "cannot complete an archived task" → 409.
- Emitters to touch: `codegen/rest.go` (bodies + Register), `rest/` (`Action`,
  `CollectionAction`), `codegen/tsclient.go`, `codegen/gocli.go`,
  `codegen/dartclient.go`. Each has a per-operation function to extend
  (`updateList`/`update <id>`/`Future<T> updateX`).

## How to verify

```bash
mise run ci          # the full gate; needs Docker
```

Or the pieces that matter most here:

```bash
go test ./... && go vet ./... && mise run lint
mise run generate-check      # committed generated code matches the schema
mise run eject-check         # only on the #59 branch
cd pgtest && go test ./...   # Postgres 18 + pgvector, in containers
```

## Open questions / decisions pending

- **Who rebases the two open PRs** — I did not force-push anything under review
  without being asked.
- **#17 stays open for the `FromGo` tier.** Recommendation: leave it cut until an
  application asks for it by name; the ADR already records that as the trigger.
- **#21 (`sqlb impact`) is half-built.** The REST-contract diff exists (ADR-0039);
  what the issue asks for beyond it is the blast-radius report (which endpoints,
  which client types, which DDL statements one edit touches). Worth retitling.
- **This file is force-added.** `.gitignore` deliberately ignores `.handoffs/` with a
  documented reason ("working scaffolding … not project documentation"). I left that
  rule alone rather than reversing it, and committed the file to a branch of its own
  so it survives worktree cleanup without landing in a PR under review.

## Key files & pointers

- `docs/adr/0041-computed-fields.md` — computed fields, and the four things building
  it taught that the design did not have.
- `docs/adr/0042-the-exit-is-generated.md` — eject, and where the fidelity line sits.
- `docs/adr/0014-migrations-and-import.md:Revisions` — the fixpoint entry, and why the
  comparison is between databases rather than registries.
- `pgtest/fixpoint_test.go:116` — `TestRoundTripIsAFixpoint`, the loop.
- `pgtest/fixpoint_test.go:205` — `TestRebuiltDatabaseMatchesTheOriginal`, the
  assertion that actually found the lost constraint name.
- `codegen/schemasrc.go:429` — the vector case that unblocks the bootstrap.
- `schema/type.go:Types()` — the canonical type list the bootstrap test walks.
- `rest/scope.go:checkObligations` — the mount-time obligation an action would extend.
- `rest/tx.go:newWriter` — the transaction the action envelope should run inside.
- `codegen/rest.go:renderRegister` — where a generated `Register(api, db, Actions{…})`
  would be emitted.
