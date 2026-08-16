# Webhooks and HTTP callbacks

"Webhook" names two different directions, and sqlb has an opinion about neither
— but the seam each one reaches for already exists, and they are not the same
seam.

**Receiving one** — Stripe tells you an invoice was paid, Clerk tells you a
user signed up — is an inbound `POST` your server did not ask for and cannot
type in advance. **Sending one** — notifying a subscriber's URL when a row in
*your* database changes — is an outbound call your own write triggers. Neither
is generated, because neither is CRUD: a resource exposes a table, and a
webhook is not a table.

## Receiving: it is a hand-written route, not a resource

[Surveying a codebase](../surveying-a-codebase.md) counts `/billing/webhook`
among the routes that "stay hand-written on the same router, by design and not
as a shortfall" — the same bucket as `/upload` and `/auth/2fa/*`. `rest`
mounts on a `huma.API`, and that API sits on a router you built and still own
([Mounting resources](README.md#bringing-your-own-router)), so a plain
`chi`/`net-http` route lives beside the generated resources on the same
server, in the same OpenAPI document's blind spot, on purpose.

```go
router := chi.NewRouter()
router.Use(middleware.RequestID, middleware.Recoverer)
router.Post("/billing/webhook", stripeWebhook(sys, cfg.StripeWebhookSecret)) // hand-written

api := humachi.New(router, huma.DefaultConfig("Blog", "1.0.0"))
rest.Must(blog.Register(api, db))                                            // generated
```

**Skip `huma.Register` for this route.** Signature verification needs the
exact bytes the sender signed, and a typed Huma operation decodes the body
before your handler sees it — the one thing you cannot afford here. A plain
`http.HandlerFunc` reading `r.Body` itself keeps the bytes intact:

```go
func stripeWebhook(sys *sqlb.Sys, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const maxBody = 64 * 1024
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}

		event, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), secret)
		if err != nil {
			// The signature didn't check out. Refuse, and don't say why.
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}

		switch event.Type {
		case "invoice.paid":
			var invoice stripe.Invoice
			if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			if _, err := sqlb.Update[Workspace](r.Context(), sys).
				Where(sqlb.Eq(WorkspaceStripeCustomerID, invoice.Customer.ID)).
				Set(WorkspaceBillingStatus, "paid").
				Exec(); err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}
```

The write inside it is an ordinary `sqlb` call through whichever `*sqlb.Sys`
fits the request — see the next section for why that choice is not automatic.

**Two things a resource would have given you for free, and this route has to
supply itself:**

- **Auth.** Middleware that requires your own bearer token runs on every
  route unless told otherwise, and Stripe does not have one. Add the webhook
  path to your middleware's exception list — the way
  [`example/tasks/app/app.go`](../../example/tasks/app/app.go) already
  excepts `/auth/login` and `/health` — and let the signature check be the
  route's only gate. An unauthenticated path that trusts an unverified body is
  the actual hole; a signature-checked one is not weaker for skipping a
  bearer token it was never going to have.
- **Scope.** A generated read runs through `BeforeQuery` and picks up tenant
  scoping automatically. A webhook handler's write does not arrive through a
  query, so if the row it touches is `.Scoped()`, decide explicitly whether to
  write through the hooked `*sqlb.Sys` (and supply the scope yourself, since
  there is no request context carrying it) or the unhooked one, the way
  [`example/tasks/app/app.go`](../../example/tasks/app/app.go) keeps both
  `sys` and `hooked` around for exactly this kind of caller.

## Sending: `AfterCommit`, not a new mechanism

sqlb has no dispatcher for outbound calls, and that is the same decision
[ADR-0021](../architecture.md#hooks-receive-an-event) made about hooks generally:
a hook is a typed callback with a transaction, not a rich event object with
its own delivery guarantees. Notifying a third party when a row changes is
the [`AfterCommit`](../queries/hooks.md#aftercommit-for-side-effects) case —
the same seam that publishes a change event or enqueues a job, because an
outbound `POST` has the identical requirement: it must not fire if the write
rolls back.

```go
sqlb.On[Order](reg).AfterCreate(func(ctx context.Context, o *Order) error {
    id, total := o.ID, o.Total
    return sqlb.AfterCommit(ctx, func(ctx context.Context) error {
        return callbacks.Post(ctx, subscriberURL, OrderPlaced{ID: id, Total: total})
    })
})
```

`AfterCommit` callbacks run once, after the commit, in registration order —
there is no retry, backoff, or delivery record built in. A failing callback
joins under `sqlb.ErrAfterCommit` so the caller can tell "the write didn't
happen" from "the write happened and the callback didn't"
([Hooks](../queries/hooks.md#aftercommit-for-side-effects)), but turning that
into at-least-once delivery — a queue, retries, a dead-letter table — is
application code, the same way `outbox.Dispatcher` is what turns
[change events](events.md) from at-most-once into durable. There is no
built-in outbox for outbound webhooks today; if you need that guarantee,
record the callback as a row in the same transaction and dispatch it from a
worker that tails the table, which is the same shape `outbox` uses for the
SSE feed.

## Next

- [Mounting resources](README.md) — how a hand-written route sits beside
  generated ones
- [Hooks](../queries/hooks.md) — `AfterCommit`, and what a hook is and is not
- [Change events](events.md) — the durable-delivery question, answered for
  sqlb's own SSE stream
- [Surveying a codebase](../surveying-a-codebase.md) — where webhooks land in
  the CRUD/non-CRUD split
