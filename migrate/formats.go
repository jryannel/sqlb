package migrate

import (
	"fmt"
	"strings"
)

// Goose renders pressly/goose migrations: one file per migration, with Up and
// Down separated by annotations.
var Goose Format = gooseFormat{}

// GolangMigrate renders golang-migrate migrations: separate .up.sql and
// .down.sql files per version.
var GolangMigrate Format = golangMigrateFormat{}

// Plain renders bare SQL with no runner-specific annotations, for projects
// applying migrations by hand or with a tool that needs neither.
var Plain Format = plainFormat{}

// ByName resolves a format from configuration.
func ByName(name string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "goose":
		return Goose, nil
	case "golang-migrate", "golangmigrate", "migrate":
		return GolangMigrate, nil
	case "plain", "sql":
		return Plain, nil
	}
	return nil, fmt.Errorf("migrate: unknown format %q (want goose, golang-migrate or plain)", name)
}

type gooseFormat struct{}

func (gooseFormat) Name() string { return "goose" }

func (f gooseFormat) Render(m Migration, opts Options) (map[string]string, error) {
	var b strings.Builder

	// NO TRANSACTION is a file-level directive in goose, so it has to be
	// decided for the migration as a whole. Split has already ensured that a
	// file needing it contains nothing that wanted a transaction.
	concurrent := false
	for _, c := range m.Changes {
		if c.Stage == StageConcurrent {
			concurrent = true
			break
		}
	}
	if concurrent {
		b.WriteString("-- +goose NO TRANSACTION\n")
		b.WriteString("-- Index changes cannot run inside a transaction, so this file is not\n")
		b.WriteString("-- wrapped in one. It contains only index changes for that reason.\n")
	}
	if !opts.Handwritten {
		b.WriteString(Header + "\n\n")
	}

	b.WriteString("-- +goose Up\n")
	for _, c := range m.Changes {
		writeSection(&b, statement(c.Up, c, opts), upComment(c), true)
	}

	b.WriteString("\n-- +goose Down\n")
	for i := len(m.Changes) - 1; i >= 0; i-- {
		c := m.Changes[i]
		down := statement(c.Down, c, opts)
		if strings.TrimSpace(c.Down) == "" {
			b.WriteString("-- Not reversible automatically: " +
				reversalNote(c) + "\n")
			continue
		}
		writeSection(&b, down, "", true)
	}

	return map[string]string{
		fmt.Sprintf("%s_%s.sql", m.Version, m.Name): b.String(),
	}, nil
}

// writeSection emits one statement, wrapping it in goose's explicit delimiters
// when it contains internal semicolons.
func writeSection(b *strings.Builder, sql, comment string, goose bool) {
	if sql == "" {
		return
	}
	for _, line := range strings.Split(comment, "\n") {
		if line != "" {
			b.WriteString("-- " + line + "\n")
		}
	}
	if goose && needsStatementBlock(sql) {
		b.WriteString("-- +goose StatementBegin\n")
		b.WriteString(sql + "\n")
		b.WriteString("-- +goose StatementEnd\n")
		return
	}
	b.WriteString(sql + "\n")
}

func reversalNote(c Change) string {
	if c.Destructive {
		return "the original definition is gone once this is applied"
	}
	return "no reverse SQL was generated for this change"
}

type golangMigrateFormat struct{}

func (golangMigrateFormat) Name() string { return "golang-migrate" }

func (golangMigrateFormat) Render(m Migration, opts Options) (map[string]string, error) {
	var up, down strings.Builder
	if !opts.Handwritten {
		up.WriteString(Header + "\n\n")
		down.WriteString(Header + "\n\n")
	}

	for _, c := range m.Changes {
		writeSection(&up, statement(c.Up, c, opts), upComment(c), false)
	}
	for i := len(m.Changes) - 1; i >= 0; i-- {
		c := m.Changes[i]
		if strings.TrimSpace(c.Down) == "" {
			down.WriteString("-- Not reversible automatically: " + reversalNote(c) + "\n")
			continue
		}
		writeSection(&down, statement(c.Down, c, opts), "", false)
	}

	return map[string]string{
		fmt.Sprintf("%s_%s.up.sql", m.Version, m.Name):   up.String(),
		fmt.Sprintf("%s_%s.down.sql", m.Version, m.Name): down.String(),
	}, nil
}

type plainFormat struct{}

func (plainFormat) Name() string { return "plain" }

func (plainFormat) Render(m Migration, opts Options) (map[string]string, error) {
	var b strings.Builder
	if !opts.Handwritten {
		b.WriteString(Header + "\n\n")
	}
	for _, c := range m.Changes {
		writeSection(&b, statement(c.Up, c, opts), upComment(c), false)
	}
	return map[string]string{
		fmt.Sprintf("%s_%s.sql", m.Version, m.Name): b.String(),
	}, nil
}
