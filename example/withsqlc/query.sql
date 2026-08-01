-- These are deliberately the queries sqlb is bad at.
--
-- sqlb's non-goal is static analytical SQL: a window function or a recursive
-- CTE goes through sqlb.Raw, which is an escape hatch rather than a feature
-- (ADR-0009). sqlc types both at compile time, which is its whole guarantee.
-- So they live here, and the filterable list endpoints live in sqlb.

-- name: AuthorLeaderboard :many
-- A window function: rank authors within their org by published posts. Nothing
-- about this benefits from being expressible as a runtime filter.
SELECT
    a.id,
    a.name,
    a.org_id,
    count(p.id) AS published_posts,
    rank() OVER (PARTITION BY a.org_id ORDER BY count(p.id) DESC) AS org_rank
FROM authors a
LEFT JOIN posts p
    ON p.author_id = a.id
    AND p.status = 'published'
    AND p.deleted_at IS NULL
GROUP BY a.id, a.name, a.org_id
ORDER BY a.org_id, org_rank;

-- name: PostViewsByStatus :many
-- A grouped aggregate over one org. sqlb can express this with Collect, but the
-- result shape is not the table shape, so the typed guarantee is worth more
-- here than the runtime filterability sqlb trades it for.
SELECT
    status,
    count(*) AS post_count,
    sum(view_count)::bigint AS total_views
FROM posts
WHERE org_id = $1
  AND deleted_at IS NULL
GROUP BY status
ORDER BY status;

-- name: GetAuthor :one
-- A plain lookup, included only so the generated Author struct exists for the
-- adoption test to point sqlb.Describe at.
SELECT * FROM authors WHERE id = $1;

-- name: ListPosts :many
-- The exception, and the one query here that is NOT what sqlc is good at.
--
-- This is stage 1 of docs/refactoring-from-sqlc.md: a filterable list endpoint
-- written the only way static SQL can express one. Every optional filter
-- becomes an arm that is always sent and usually means nothing, which is the
-- documented sqlc workaround rather than a strawman — see comparisons.md.
--
-- It is here to be replaced, not to be imitated. stage1.go calls it, stage2.go
-- through stage4.go do the same job, and refactor_test.go holds all four to the
-- same answers.
--
-- What the shape costs, beyond reading badly:
--
--   * Three predicates reach Postgres on every request, each guarded by a NULL
--     check the planner has to see through. Stage 2 sends only what was asked.
--   * The sort is baked in. `ORDER BY published_at DESC` cannot become
--     `ORDER BY view_count ASC` without a second copy of the whole query, so a
--     column the UI can sort by is a query, and n columns times two directions
--     is 2n of them.
--   * Nothing here says which columns a *client* may filter on. That decision
--     lives in whatever handler assembles these parameters, so it is reviewable
--     only by reading that handler — where ADR-0006 puts it in the schema.
SELECT * FROM posts
WHERE org_id = @org_id
  AND deleted_at IS NULL
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('min_views')::bigint IS NULL OR view_count >= sqlc.narg('min_views')::bigint)
  AND (sqlc.narg('search')::text IS NULL OR title ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY published_at DESC
LIMIT @page_limit;
