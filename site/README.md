# site

The documentation site: [Astro](https://astro.build) with
[Starlight](https://starlight.astro.build).

```bash
mise run site-dev      # serve with live reload
mise run site-build    # build into site/dist, then check every link resolves
mise run site-check    # can the docs be published as they stand? (no npm install)
```

## Where the content lives

**`docs/guide/` and `docs/adr/` are the source of truth.** Both are plain
markdown with no frontmatter, so they render on GitHub and are readable in a
checkout — which is where most people will meet them. `scripts/sync-docs.mjs`
derives the Starlight collection from them on every build, one route per source
directory: `/guide/` and `/adr/`.

Adding another section is an entry in the `SOURCES` array in that script plus a
sidebar group in `astro.config.mjs`.

That means:

- **Edit `docs/guide/*.md` and `docs/adr/*.md`.** `site/src/content/docs/guide/`
  and `.../adr/` are generated and gitignored. The "Edit this page" link on the
  site points at the source, not at the copy.
- **Everything else under `src/content/docs/` is hand-written** — currently just
  the landing page, `index.mdx`.
- **Nothing generated is committed**, so there is no drift to gate against. The
  pages are rebuilt from scratch each time, which is also how a page deleted from
  the source leaves the site.

## What the scripts guard

Neither script is a formality; each one has a failure it exists to catch, and
both were checked to fail before being relied on.

**`sync-docs.mjs`** rewrites repo-relative links into web links. A sibling page
becomes a route; so does a page in the other source directory, which is why the
guide's `../adr/0011-...md` links now stay on the site instead of leaving for
GitHub. Anything with no page here — `../review-adoption-readiness.md`,
`../../example/tasks/` — becomes a GitHub URL. A link matching no rule is a
**hard error**, not a link that quietly 404s after deploy. It also fails on a
page with no H1 to take a title from, and on a guide page missing from the
`sequence` list, so adding one is a deliberate act here too.

It skips inline code as well as fenced blocks, which is not fussiness: ADR-0020
contains `sqlb.QueryIn[T](tx)`, Go generics that are indistinguishable from a
markdown link to a pass that only skips fences.

**`check-links.mjs`** reads the built HTML and verifies every internal link
resolves to a file that exists. This is the one that matters, because its
failure mode is invisible locally: links are rewritten *and* the deployment sits
under a base path, so a link can be well-formed markdown, survive the build, and
still break in production. It caught exactly that during setup, twice.

## Deployment

`site.config.mjs` holds `site` and `base`. `astro.config.mjs` configures Astro
from it, `sync-docs.mjs` prefixes generated links with it, and Starlight prefixes
its own navigation automatically. It is currently set for a GitHub Pages project
site at `/sqlb`.

Moving to a custom domain or a user page means setting `base` to `"/"` there —
and also editing the hero `link:` values in `src/content/docs/index.mdx`, which
are written out in full because Starlight does not apply the base to frontmatter.
That is the one place the base is repeated, so it is the one place that can drift.
Do not try to hold it in your head: change `base`, run `mise run site-build`, and
the link check names every href that no longer resolves. It found exactly this
when the base was first switched to `"/"` as a test.

Nothing publishes automatically. There is no deploy workflow, so `site/dist` is
built and goes nowhere until someone wires one up.
