# Django ORM/DRF vs sqlb — a capability comparison

Written 2026-08-15. Covers `main`. Purpose: derive concrete functional and
ergonomic gaps against Django's ORM + Django REST Framework (DRF), starting
from a relationships discussion (one-to-many / one-to-one / many-to-many)
that widened into "what would it take for sqlb to cover what Django covers,
in Go, with codegen, over real SQL."

Legend: ✅ parity (different shape, same capability) · ⚠️ partial/gap ·
❌ not present · 🚫 deliberately refused, argued in an ADR (not an oversight)

## How sqlb and Django start from different premises

Everything below reads more legibly with this up front: Django's ORM is a
runtime system with a migration *history* it applies and tracks; sqlb is a
declarative schema that a compiler diffs and everything else — models, REST,
OpenAPI, four clients — derives from. Several "gaps" below are the direct,
intended consequence of that difference (no migration graph to squash,
because there's no applied history to squash) rather than missing feature
work. Those are marked 🚫 with the ADR that argues the position.

---

## 1. Models & fields

| Django | sqlb | |
|---|---|---|
| `CharField`, `IntegerField`, `BooleanField`, `DateTimeField`, `JSONField`, `UUIDField`, ... | `Text`, `Int`/`BigInt`/`SmallInt`, `Bool`, `Timestamp`, `JSON`, `UUID`, plus `Numeric`, `Vector` (pgvector), `Bytes` — `schema/field.go` | ✅ |
| `choices=[...]` | `Enum(name, values...)` → text column + `CHECK` (ADR-0017) + generated Go type/constants, shareable via `.SharedAs()` | ✅ |
| `validators=[EmailValidator(), ...]` (Go-side/Python-side callables) | ❌ none found. Only table-level `Check(name, sql)` (SQL text) and DB constraints (`NOT NULL`, `UNIQUE`, `CHECK`) | ⚠️ gap |
| `GeneratedField` (Django 5) | `Computed(name, type, expr)` / `FromSQL(sql)` — DB-expression computed columns (ADR-0041) | ✅ |
| Computed **Python** property on the model | A `FromGo` tier was designed, then explicitly cut (ADR-0041) | ⚠️ gap (deliberately deferred, not refused) |
| `AutoField`/implicit `id` | No implicit PK — must declare `UUIDv7("id").PrimaryKey()` or similar | 🚫 different policy, not a gap (ADR: `0048-auto-incrementing-keys.md`) |
| `auto_now_add=True` | `Timestamps()` group: `created_at` `Default(Now())` | ✅ |
| `auto_now=True` (touch on every save) | ❌ not built — `updated_at` only fires at INSERT; example apps hand-write a Postgres trigger to get UPDATE-time touching | ⚠️ **functional gap** |

## 2. Relationships

*(covered in depth earlier in this conversation; summarized here for the full picture)*

| Django | sqlb | |
|---|---|---|
| `ForeignKey` / reverse `_set` manager | `Ref(name, Target)` (forward) + `.Inverse("name")` + `.InverseExpandable(...)` (reverse, capped/ordered subquery) — `field.go:609`, ADR-0022 | ✅, different shape: Django's reverse manager is unrestricted; sqlb's is capped/ordered by declaration |
| `OneToOneField` | ❌ no dedicated verb. `Ref()+.Unique()` compiles, but codegen doesn't know about it — the reverse side still renders as a `Collection`, not a single pointer | ⚠️ **functional gap** |
| `ManyToManyField` (auto-junction, no columns) | ❌ no declaration. A junction is an ordinary two-`Ref` table, queried in two hops | 🚫 deliberate (ADR-0056, decided 2026-08-13) — but ADR names "adopters mostly use bare junctions" as its own revisit trigger |
| `ManyToManyField(through=Model)` (junction with payload columns) | Same as sqlb's *only* mechanism — model the junction as a real table | ✅ — this is the case ADR-0056 is confident about |
| Nested expand (`select_related`/`prefetch_related` chains) | One level only; `?expand=list.workspace` refused (ADR-0025) | 🚫 deliberate |

## 3. QuerySets & query building

| Django | sqlb | |
|---|---|---|
| Lazy, chainable `QuerySet`, composed at evaluation | `Builder[T]` — lazy, chainable, composed at `.SQL()`/`.All()`/etc (`builder.go`, `doc.go`) | ✅ |
| `Q(a) \| Q(b) & ~Q(c)` | `Or(a, And(b, Not(c)))` — same tree, plain functions instead of operator overloading (Go can't overload `\|`/`&`) | ✅ same power, more verbose |
| `F('count') + 1` | `Add(Current("count"), Val(1))` via `SetExpr`/`OnConflictSet`; `EqField`/`EqCol` for column-to-column predicates | ✅ same power, no single named type |
| `.annotate(n=Count('x')).values(...)` | `.Select(Count().As("n")).GroupBy(...)`, scanned via `Collect[Shape]` | ✅ same power, no `annotate()` verb |
| `.bulk_create(objs)` | `InsertRows(rows...)` — one multi-row `VALUES` statement | ✅ |
| `.update(status="x")` (filtered, no row-load) | `UpdateRows[T]().Set(...).Where(...)` | ✅, plus a safety rail Django lacks: unscoped update/delete refused unless `.Everything()` |
| `.bulk_update(objs, ['field'])` (per-row differing values, one `CASE`-statement UPDATE) | ❌ not found — no per-row-CASE helper anywhere | ⚠️ **functional gap** |
| `transaction.atomic()` | `DB.WithTx(ctx, fn)` / `WithTxOptions` — joins an existing tx rather than nesting | ✅ |
| `transaction.on_commit(fn)` | `DB.AfterCommit(fn)` | ✅ |
| Signals (`pre_save`, `post_save`, `pre_delete`, `post_delete`) | `Hooks[T]`: `BeforeCreate`/`AfterCreate`/`BeforeUpdate`/`AfterUpdate`/`BeforeDelete`/`AfterDeleteRows`, registered per-model against an explicit `Registry` — no process-wide default (ADR-0047) | ✅, plus `BeforeQuery` (read-scoping) and `AfterCommit` that Django signals don't cleanly give you |
| `.raw()` (whole query) / `.extra()` | `Raw`/`RawPred`/`RawSel` — escape hatch scoped to one clause/expression, not a whole statement | ✅ narrower, composable |

## 4. Migrations

| Django | sqlb | |
|---|---|---|
| `makemigrations` writes files with a dependency graph; `migrate` applies and tracks them in `django_migrations` | `migrate.Diff(current, target)` is a **pure function** over two `*schema.Registry` values; nothing is applied or tracked by sqlb itself | 🚫 fundamentally different model (ADR-0014) |
| "Current" = what the tracked history says has been applied | "Current" = replay the committed migration history into a scratch DB via `shadow`, then `introspect` the result — never trusts live production directly | 🚫 deliberate, stronger drift guarantee |
| Destructive ops apply silently unless you notice in review | Destructive changes (`DROP TABLE`/`COLUMN`, narrowing type) are emitted **commented out** with a reason; found high-severity gap: the `sqlb survey` adoption command bypasses this renderer-level guard (per `docs/codebase-review-2026-08-02.md`) | ✅ stronger by default, one known escape-hatch bug tracked separately |
| Rename detection heuristics (sometimes asks "did you rename X to Y?") | Never inferred — `RenamedFrom(old)` must be declared explicitly for one release, else diffs as drop+add | 🚫 deliberate, no heuristic guessing |
| Data migrations (`RunPython`) | Explicitly out of scope — "generated columns, triggers and data backfills stay hand-written" (ADR-0014) | 🚫 deliberate |
| Migration dependency graph across apps, squashing | ❌ not found | 🚫 doesn't apply — there's no applied-history graph to squash |
| `inspectdb` | `introspect` package — DB → `*schema.Registry`, and reports what it *can't* represent instead of silently dropping it | ✅, stronger completeness contract |

## 5. Inheritance & mixins

| Django | sqlb | |
|---|---|---|
| Abstract base classes (share fields + `Meta` + methods + validators) | `schema.Group` — an ordered `[]*Field` splice, **columns only** | ⚠️ partial |
| Multi-table inheritance | ❌ not found | ❌ gap, low priority (no evidence anyone's asked) |
| A mixin can carry a validator, an index, or hook registration | Explicitly ruled out — `schema` package cannot register hooks (would break `deps-check`'s import direction and ADR-0010's "codegen is optional") | 🚫 deliberate, named "unbuilt" not "impossible" (ADR-0023) — `SoftDelete()`'s own doc admits it doesn't filter deleted rows; that requires a hand-added `BeforeQuery` hook per table |

## 6. REST layer vs DRF

| Django/DRF | sqlb | |
|---|---|---|
| `ModelViewSet` + `router.register()` | `rest.Resource[T,C,U]` — one generic function, instantiated per model by codegen from `schema.REST{}` | ✅, schema is the single source instead of a subclassable view |
| `ModelSerializer` (per-field serialize/validate, `to_representation`) | ❌ no serializer class at all — wire shape is Huma reflecting generated Go structs, field names computed as a **declared total function** of the column name (ADR-0036) | 🚫 deliberate — no per-field override, in either direction |
| `serializer.is_valid()` / `validators=[...]` | Layered instead: column-capability checks at parse time, `schema.Registry.Validate()` at declare time, DB constraints at write time, and `CreateBody.Row()` as the cross-field-validation escape hatch | ✅ same coverage, no single reusable "Validator" object |
| `PageNumberPagination` / `CursorPagination` (pick one per view) | Every list gets **both** offset (`?page=`) and keyset (`?cursor=`) from one endpoint; forced total order via auto PK tiebreak (ADR-0027) | ✅ stronger — Django's `OrderingFilter` can silently paginate inconsistently, sqlb's can't |
| Cursor pagination is bidirectional (`?cursor=` forward/back in most DRF setups) | Forward-only, no `?before=` | ⚠️ **functional gap**, named explicitly in ADR-0027 |
| `django-filter` / `SearchFilter` / `OrderingFilter`, configured per view | `filter` package — capability (`Filterable`/`Sortable`/`Searchable`) lives on the column, one source feeding REST params, OpenAPI, and generated clients; plus a `?filter=` JSON boolean tree django-filter doesn't have out of the box | ✅ stronger in expressiveness, no per-view config needed |
| Full-text search (`SearchVector`, or DRF+Postgres FTS) | `?search=` is ILIKE-OR substring only, explicitly (ADR-0037, which names its own revisit trigger) | 🚫 deliberate for now |
| `permission_classes` / `get_queryset()` filtered by `request.user` | `Scoped()` column + a `BeforeQuery`/etc hook, and **`rest.Resource` refuses to mount** if the obligation isn't met (ADR-0030) | 🚫 deliberately stronger shape: a forgotten Django `get_queryset()` filter is a silent leak; a forgotten sqlb hook is a boot-time refusal. Weaker in one way: `sqlb.Query[T]()` in hand-written app code still bypasses it |
| Nested routers (`/lists/{id}/tasks`) | Refused — flat collection + filter (`GET /tasks?list_id=eq.<id>`), on purpose (ADR-0038) | 🚫 deliberate — named cost: deleted parent gives 200+empty page, not 404 |
| `@action(detail=True/False)` | `schema.Action` + `rest.Action`/`rest.CollectionAction` — generates the transactional envelope (fetch→lock→decode→call→persist declared `Writes`), the verb body is always hand-written Go (ADR-0043) | ✅ close analog, narrower by design — no arbitrary read/query logic inside the generated envelope |
| `drf-spectacular` (introspects serializers) | Huma reflects the same generated structs used for DB binding — no separate schema to introspect, no `@extend_schema` needed for shape | ✅ |
| Signals fired synchronously to in-process receivers; webhooks are hand-rolled | SSE change-feed carrying only `{table, key, op}` — never payload — via `rest.Broker` (in-memory) or `outbox.Dispatcher` (transactional, ADR-0045); wired through the same hooks as CRUD | ✅ different shape, no outbound webhook delivery to third-party URLs built in |

---

## Gaps, sorted for follow-up

### Functional gaps (Django can do it; sqlb structurally can't yet)

1. **One-to-one relationship codegen** — `Ref()+.Unique()` compiles but the reverse side still generates a capped `Collection` instead of a single pointer. This is the smallest, most self-contained fix of the list.
2. **`bulk_update`-equivalent** — no per-row-differing-values single-statement UPDATE (Django generates a `CASE WHEN id=... THEN ...`). Today: one `Update` per row in a transaction, or hand-rolled `CASE` via `SetExpr`.
3. **`auto_now`** — no built-in touch-on-update; every project using it today hand-writes a Postgres trigger + migration (confirmed in both example apps).
4. **Backward cursor pagination** (`?before=`) — forward-only today, named as a known gap in ADR-0027 itself.
5. **Go-side per-field validators** — no `validators=[...]` equivalent; only table-level SQL `Check()` and DB constraints. Cross-field logic has an escape hatch (`Row()`); single-field custom validation (e.g., "must be a valid ISO country code") doesn't have an obvious declarative home.

### Ergonomic gaps (sqlb can do it; more verbose or differently spelled than Django)

1. **Many-to-many bare-junction sugar** — the live thread from earlier in this conversation. ADR-0056 refuses an `m2m` tag on the grounds that junctions almost always carry payload columns; Django's own `ManyToManyField` (auto-junction) vs `through=` (explicit model) split suggests the "bare junction" case is common enough elsewhere to deserve *scoped* sugar, without weakening the "junction with columns → model it" rule. ADR-0056 names this as its own revisit trigger #2.
2. **No `Q()`/`F()` named types** — same compositional power via `And`/`Or`/`Not`/`Add`/`Sub`, just no operator overloading (not really fixable in Go).
3. **No `annotate()` verb** — `Select`+aggregate+`GroupBy`+`Collect[Shape]` covers it, but it's four concepts where Django has one.
4. **Reverse relation collections require explicit `.InverseExpandable()` + cap/order** — safer default than Django's unrestricted related-manager, but each one is a small ceremony tax when the child's cardinality is genuinely unbounded.

### Not gaps — deliberate divergences worth knowing about before proposing to "fix" them

Migrations are diff-generated rather than applied/tracked (ADR-0014); nested REST routes are refused in favor of flat filters (ADR-0038); there's no DRF permission-class plug-point because scoping is a declared, mount-checked obligation instead (ADR-0030); there's no serializer class because the wire is a pure function of the schema (ADR-0036); custom actions generate only the transactional envelope, never verb logic (ADR-0043); the event stream carries addresses, never payloads (ADR-0045). Each has a "what would change our mind" section in its ADR — worth reading before reopening any of them.

---

## Where this leaves "Django ORM capabilities, in Go, with codegen, over real SQL"

Closer than the framing suggests. Most of Django's ORM surface has a
same-power sqlb equivalent already, just spelled differently because Go
lacks operator overloading and sqlb pushes more into the type system and
schema declaration than into runtime configuration. The REST layer is, if
anything, ahead of DRF's defaults in a few places (dual pagination, opt-in
capability as single source of truth, mount-time scope enforcement) at the
cost of a few named, deliberate refusals (nested routes, permission classes,
serializer overrides).

The real, non-deliberate gaps are short and concrete: one-to-one relationship
codegen, `bulk_update`, `auto_now`, backward cursor pagination, and per-field
validators. Of those, one-to-one relationship codegen is the smallest and
most isolated — a reasonable next sub-project to brainstorm on its own,
separate from the many-to-many question, which needs its own ADR-revisiting
conversation rather than a code change.
