# Agent skills

Skills an agent can load when working on a project that uses sqlb. These are the
**static** half — the part that is true in every repository.

The project-specific half is not here, because it cannot be: which columns your
resources accept is different in every project, so it is **generated**. Set
`Options.SkillDir` and `sqlb generate` writes
`<SkillDir>/sqlb-schema/SKILL.md` — the tables, the mounted paths, what each
resource may be filtered, sorted, searched and expanded on, the declared verbs,
and the obligations the schema carries. `sqlb check` names it when it has
drifted, which is the only reason writing instructions into a repository is
safe. [`example/tasks`](../example/tasks/.claude/skills/sqlb-schema/SKILL.md) has
one committed. [ADR-0049](../docs/adr/0049-the-skill-is-generated.md) is the
argument.

A repository with several registries wants one `SkillDir` per registry, placed
beside the module it describes: a nested `.claude/skills` is directory-scoped, so
sixteen skills all named `sqlb-schema` are sixteen correctly-scoped skills rather
than a collision. To share one directory instead — the repository root, so every
skill is offered from the first turn — give each registry its own
`Options.SkillName`.

| Skill | Covers |
|---|---|
| [`sqlb-queries`](sqlb-queries/SKILL.md) | Where the builder ends and `Raw`, sqlc or hand-written SQL begins — plus four failure modes that compile, pass their tests, and are wrong at runtime |
| [`sqlb-adoption`](sqlb-adoption/SKILL.md) | Whether an existing codebase should adopt sqlb at all: a five-step census producing a ratio and a pilot, with the two stop conditions that end the evaluation early |

They answer different questions and neither subsumes the other: `sqlb-adoption`
runs once per codebase and mostly decides *whether*; `sqlb-queries` applies every
time someone writes a query afterwards.

## Installing

The ecosystem CLI ([`skills`](https://github.com/vercel-labs/skills)) takes an
`owner/repo` shorthand and places files where each agent tool expects them:

```bash
npx skills add jryannel/sqlb
```

One skill by name, rather than all of them:

```bash
npx skills add jryannel/sqlb --skill sqlb-queries
```

Project scope is the default, which is what you want — the skill lands in the
repository, so the team and any cloud agents share it. `--global` installs across
all projects instead.

Note that `npx` here is *your* invocation, not part of sqlb's build. Nothing in
this repository depends on Node, and adding a skill does not change that.

**Or just copy it.** A skill is a directory with a `SKILL.md` in it, so for
Claude Code:

```bash
mkdir -p .claude/skills && cp -r skills/sqlb-queries .claude/skills/
```

## Why these are written down at all

This repository prefers a failing check to a documented rule — `generate-check`,
`eject-check`, `impact-check` and the rest exist because a convention that is
only written down drifts. A skill is a written-down rule, so it needs the
argument.

`sqlb-queries` is the case where no check is possible. When a query reaches past
the row, the wrong code compiles, passes its tests, and answers the request:
an aggregate over an empty set scans `NULL` into an `int64`, a day filter against
`timestamptz` matches nothing and returns no error, `OnConflictDoNothing` makes a
retried payment arrive as "not found". Nothing in CI will catch those, which is
why they are written down rather than gated.

`sqlb-adoption` is a different exception: it is about a codebase sqlb has not
been told about yet, so there is nothing in *this* repository for a check to run
against. Its failure mode is not a wrong number but a missing stop condition —
an evaluation that reports "sqlb replaces the API" when the honest answer is
"the least novel third of it", or that surveys the routes before finding out the
tables are blocked.

## Keeping it honest

Every code sample in `sqlb-queries/SKILL.md` was compiled and rendered against
the tree, not written from memory — which caught three wrong signatures and one
stale claim in the process. The traps carry their evidence: the `timestamptz`
one is asserted by `pgtest/census_test.go`, and that test fails loudly if the
missing cast is ever added, so the skill's claim and the code cannot silently
disagree.

Every shell command in `sqlb-adoption/SKILL.md` was run against synthetic
fixtures on BSD awk, which is what the stated platform has, and its `sqlb survey`
flags were checked against the command's own help rather than the prose.

`docs/special-cases.md` is the measured census these boundaries come from, and it
says of itself that its status column will rot. It has: two rows have closed
since it was written. Prefer this skill for *behaviour*, the census for
*proportion* — how much of a real corpus each shape accounts for.
