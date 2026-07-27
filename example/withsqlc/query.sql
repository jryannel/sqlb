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
