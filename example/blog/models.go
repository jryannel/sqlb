// Package blog holds the models generated from the blogschema package.
package blog

import "time"

// The structs below are what `sqlb generate` will emit from blogschema. They are
// hand-written for now so that the runtime engine can be exercised end to end
// before the generator lands, and they are the exact shape the generator has to
// produce: a `db` tag naming the column, and an `sqlb` tag carrying the
// capabilities the schema declared.
//
// Once codegen exists this file becomes models_gen.go and stops being edited.

// Org is a tenant.
type Org struct {
	ID        string    `db:"id" sqlb:"pk,default"`
	Name      string    `db:"name" sqlb:"filter,sort,search"`
	Slug      string    `db:"slug" sqlb:"filter"`
	CreatedAt time.Time `db:"created_at" sqlb:"sort,readonly,default"`
	UpdatedAt time.Time `db:"updated_at" sqlb:"sort,readonly,default"`
}

func (Org) TableName() string { return "orgs" }

// Author is a person who writes posts.
type Author struct {
	ID           string    `db:"id" sqlb:"pk,default"`
	OrgID        string    `db:"org_id" sqlb:"filter"`
	Email        string    `db:"email" sqlb:"filter,search"`
	Name         string    `db:"name" sqlb:"filter,sort,search"`
	PasswordHash string    `db:"password_hash" sqlb:"hidden"`
	CreatedAt    time.Time `db:"created_at" sqlb:"sort,readonly,default"`
	UpdatedAt    time.Time `db:"updated_at" sqlb:"sort,readonly,default"`
}

func (Author) TableName() string { return "authors" }

// PostStatus is the generated enum type for the posts.status column.
type PostStatus string

const (
	PostStatusDraft     PostStatus = "draft"
	PostStatusReview    PostStatus = "review"
	PostStatusPublished PostStatus = "published"
)

// Post is a blog post.
type Post struct {
	ID          string     `db:"id" sqlb:"pk,default"`
	OrgID       string     `db:"org_id" sqlb:"filter"`
	AuthorID    string     `db:"author_id" sqlb:"filter"`
	Title       string     `db:"title" sqlb:"filter,sort,search"`
	Body        string     `db:"body" sqlb:"search"`
	Status      PostStatus `db:"status" sqlb:"filter,sort,default"`
	ViewCount   int64      `db:"view_count" sqlb:"filter,sort,readonly,default"`
	PublishedAt *time.Time `db:"published_at" sqlb:"filter,sort"`
	CreatedAt   time.Time  `db:"created_at" sqlb:"sort,readonly,default"`
	UpdatedAt   time.Time  `db:"updated_at" sqlb:"sort,readonly,default"`
	DeletedAt   *time.Time `db:"deleted_at" sqlb:"readonly"`
}

func (Post) TableName() string { return "posts" }
