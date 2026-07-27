package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/tasks"
)

// POST /tasks/{id}/comments — the endpoint the generator deliberately does not
// produce.
//
// Two rows have to move together: the comment is inserted and the task's
// comment_count goes up by one. A generated create issues a single INSERT under
// autocommit, so it cannot make that promise, which is why the schema exposes
// comments for read, list and delete and not for create.
//
// Three things are worth reading for here:
//
//   - WithTx makes both writes one unit. A failure after the insert rolls the
//     insert back, rather than leaving a comment the counter does not know
//     about.
//   - The counter moves with `comment_count + 1` computed by the database, not
//     by reading the old value into Go and writing it back. Two comments posted
//     in the same second both count.
//   - AfterCommit is where the side effect goes. Notifying a watcher from
//     inside the transaction would announce a comment that a later rollback
//     un-wrote; doing it after the handler returns would announce one that was
//     never committed. Registered here, it runs if and only if the commit
//     succeeded.

type commentInput struct {
	TaskID string `path:"id" format:"uuid" doc:"The task to comment on"`
	Body   struct {
		Body string `json:"body" minLength:"1" maxLength:"10000"`
	}
}

type commentOutput struct {
	Status int
	Body   tasks.Comment
}

// commentAPI holds what the endpoint needs: the hooked handle, and somewhere to
// send the notification that stands in for a real one.
type commentAPI struct {
	db  *sqlb.DB
	log *slog.Logger
}

func registerCommentRoutes(api huma.API, c *commentAPI) {
	huma.Register(api, huma.Operation{
		OperationID: "create-comment",
		Method:      http.MethodPost,
		Path:        "/tasks/{id}/comments",
		Summary:     "Comment on a task",
		Description: "Creates a comment and increments the task's comment_count in one " +
			"transaction. This is not a generated handler: the counter is the reason.",
		Tags:          []string{"comments"},
		DefaultStatus: http.StatusCreated,
	}, c.create)
}

func (c *commentAPI) create(ctx context.Context, in *commentInput) (*commentOutput, error) {
	var out commentOutput

	err := c.db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
		// Reading the task first is not a permission check — the BeforeUpdate
		// hook below already confines the update to the caller's workspace, so
		// a task in another workspace would match no rows. It is here to tell
		// "no such task" apart from "nothing to update", so the caller gets a
		// 404 rather than a comment attached to nothing.
		//
		// It runs on tx, so it sees the same snapshot the writes will use.
		task, err := sqlb.Query[tasks.Task]().
			Where(tasks.TaskCols.ID.Eq(in.TaskID)).
			One(ctx, tx)
		if errors.Is(err, sqlb.ErrNotFound) {
			return huma.Error404NotFound("no task matched")
		}
		if err != nil {
			return fmt.Errorf("reading the task: %w", err)
		}

		// workspace_id and author_id are absent here because they are ReadOnly
		// in the schema and the BeforeCreate hook supplies them. The same hook
		// serves this endpoint and every other write of a comment.
		comment, err := sqlb.InsertRows(&tasks.Comment{
			TaskID: task.ID,
			Body:   in.Body.Body,
		}).One(ctx, tx)
		if err != nil {
			return fmt.Errorf("inserting the comment: %w", err)
		}

		if _, err := tasks.UpdateTask().
			AddCommentCount(1).
			Where(tasks.TaskCols.ID.Eq(task.ID)).
			Stmt().
			Exec(ctx, tx); err != nil {
			return fmt.Errorf("bumping the comment count: %w", err)
		}

		// At-most-once and in-process: if this machine dies between the commit
		// and the callback, nothing records that the callback was owed. That is
		// the documented limit of AfterCommit and the reason a durable change
		// feed wants an outbox row written in this same transaction instead.
		// For a log line it is the right trade.
		if err := sqlb.AfterCommit(ctx, func(context.Context) error {
			c.log.Info("comment posted",
				"comment_id", comment.ID,
				"task_id", task.ID,
				"workspace_id", comment.WorkspaceID)
			return nil
		}); err != nil {
			return fmt.Errorf("registering the notification: %w", err)
		}

		out.Status = http.StatusCreated
		out.Body = comment
		return nil
	})
	if err != nil {
		return nil, asHTTP(err)
	}
	return &out, nil
}
