-- Ejected from a sqlb schema by `sqlb eject`. This file is yours now.
--
-- It is the same DDL `sqlb migrate` would have written for a first
-- migration, so applying it produces the database the schema declared.
-- Computed columns are absent by construction: they were expressions,
-- never storage, and store.go carries them in the SELECT list instead.

-- create table authors
CREATE TABLE "authors" (
    "id" uuid NOT NULL DEFAULT uuid_generate_v7(),
    "org_id" uuid NOT NULL,
    "email" text NOT NULL,
    "name" text NOT NULL,
    "password_hash" text NOT NULL,
    "created_at" timestamptz NOT NULL DEFAULT now(),
    "updated_at" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "authors_pkey" PRIMARY KEY ("id"),
    CONSTRAINT "authors_email_key" UNIQUE ("email")
);

-- create table orgs
CREATE TABLE "orgs" (
    "id" uuid NOT NULL DEFAULT uuid_generate_v7(),
    "name" text NOT NULL,
    "slug" text NOT NULL,
    "created_at" timestamptz NOT NULL DEFAULT now(),
    "updated_at" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "orgs_pkey" PRIMARY KEY ("id"),
    CONSTRAINT "orgs_slug_key" UNIQUE ("slug")
);
COMMENT ON TABLE "orgs" IS 'A tenant. Every other table is scoped to one.';

-- create table posts
CREATE TABLE "posts" (
    "id" uuid NOT NULL DEFAULT uuid_generate_v7(),
    "org_id" uuid NOT NULL,
    "author_id" uuid NOT NULL,
    "title" text NOT NULL,
    "body" text NOT NULL,
    "status" text NOT NULL DEFAULT 'draft',
    "view_count" bigint NOT NULL DEFAULT 0,
    "published_at" timestamptz,
    "created_at" timestamptz NOT NULL DEFAULT now(),
    "updated_at" timestamptz NOT NULL DEFAULT now(),
    "deleted_at" timestamptz,
    CONSTRAINT "posts_pkey" PRIMARY KEY ("id"),
    CONSTRAINT "posts_status_check" CHECK ("status" IN ('draft', 'review', 'published')),
    CONSTRAINT "published_posts_have_a_date" CHECK (status <> 'published' OR published_at IS NOT NULL)
);
COMMENT ON TABLE "posts" IS 'A blog post.';

-- add foreign key authors_org_id_fkey
ALTER TABLE "authors" ADD CONSTRAINT "authors_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "orgs" ("id") ON DELETE CASCADE;

-- add foreign key posts_org_id_fkey
ALTER TABLE "posts" ADD CONSTRAINT "posts_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "orgs" ("id") ON DELETE CASCADE;

-- add foreign key posts_author_id_fkey
ALTER TABLE "posts" ADD CONSTRAINT "posts_author_id_fkey" FOREIGN KEY ("author_id") REFERENCES "authors" ("id") ON DELETE RESTRICT;

-- index authors_org_id_idx
CREATE INDEX "authors_org_id_idx" ON "authors" ("org_id");

-- index posts_author_id_idx
CREATE INDEX "posts_author_id_idx" ON "posts" ("author_id");

-- index posts_org_id_status_idx
CREATE INDEX "posts_org_id_status_idx" ON "posts" ("org_id", "status");

