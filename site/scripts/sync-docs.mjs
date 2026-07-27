// Derive the Starlight content collection from the markdown in docs/.
//
// The files under docs/ are the source of truth: plain markdown, no frontmatter,
// rendering on GitHub, which is where most people meet them. This turns them
// into what Starlight wants — frontmatter, web paths instead of file paths —
// without a second copy for anyone to edit. Nothing written here is committed;
// it is regenerated on every build, so there is no drift to gate.
//
// What it does gate is links, and it does so by resolving them rather than by
// matching patterns. A relative link means something different depending on
// which directory the file sits in — `adr/0011-x.md` from docs/, `../adr/0011-x.md`
// from docs/guide/, `0011-x.md` from docs/adr/ all name the same record — so each
// link is resolved against the repository and then looked up:
//
//   published here    → an internal route
//   exists in the repo → a GitHub URL, since the site has no page for it
//   neither            → a hard error
//
// The last case is the one worth having. It catches a link that 404s after
// deploy, and also a link to a repository file that simply is not there.
// `--check` runs the transform and reports without writing.

import { existsSync, statSync } from "node:fs";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join, posix, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { base } from "../site.config.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "../..");
const contentRoot = join(here, "../src/content/docs");

// Trailing slash stripped so `${prefix}/guide/...` is well-formed for base "/".
const prefix = base.replace(/\/$/, "");

const GITHUB = "https://github.com/jryannel/sqlb";
const BLOB = `${GITHUB}/blob/main`;
const TREE = `${GITHUB}/tree/main`;

/**
 * Each source becomes one route on the site.
 *
 * `files` restricts a directory to named files, for docs/ where only some of the
 * loose markdown is published. `order` returns a sidebar position and `label`
 * the sidebar text when it should differ from the page's H1; both take the
 * page's slug — the filename without .md, or "index" for a README.
 */
const SOURCES = [
  {
    dir: "docs/guide",
    route: "guide",
    // Explicit, because the guide is a reading order rather than a list: a new
    // page should be placed deliberately, and one missing from here is reported
    // rather than silently appended at the end.
    sequence: ["index", "getting-started", "schema", "queries-and-hooks", "rest", "migrations"],
    order(slug) {
      return this.sequence.indexOf(slug);
    },
    // "# Guide" is the right heading on GitHub, where the file is read on its
    // own, and reads as "Guide › Guide" in a sidebar that already says Guide.
    label: (slug) => (slug === "index" ? "Overview" : null),
  },
  {
    dir: "docs/adr",
    route: "adr",
    // Derived from the number the filename already carries: an ADR list is
    // numbered by definition, so restating the sequence would be a second place
    // to forget. The index leads; the template trails, recording no decision.
    order(slug) {
      if (slug === "index") return 0;
      if (slug.startsWith("0000")) return 9999;
      return Number.parseInt(slug.slice(0, 4), 10);
    },
    // "ADR-0016: A guard is not trusted until it has failed on purpose" is the
    // right page title and too long for a sidebar row. The number carries the
    // identity — every cross-reference in these docs uses it — so it leads.
    label(slug, title) {
      if (slug === "index") return "All records";
      const m = /^ADR-(\d+|NNNN):\s*(.+)$/.exec(title);
      if (!m) return null;
      return m[1] === "NNNN" ? "Template" : `${m[1]} · ${m[2]}`;
    },
  },
  {
    dir: "docs",
    route: "project",
    // Named rather than globbed, so a new file in docs/ is not published by
    // accident — what belongs on the site is a decision each time.
    //
    // Read in this order: what it is for, how it is built, what it promises,
    // how it sits beside sqlc, and what an outside reader made of it. The
    // review is a dated snapshot and says so in its own first paragraph, which
    // is what makes it publishable rather than misleading.
    files: [
      "vision.md",
      "architecture.md",
      "compatibility.md",
      "with-sqlc.md",
      "review-adoption-readiness.md",
    ],
    order(slug) {
      return this.files.indexOf(`${slug}.md`);
    },
  },
];

const check = process.argv.includes("--check");

/**
 * Split markdown into code and prose runs, so rewrites never touch code.
 *
 * Both fenced blocks and inline spans count. The inline case is not
 * hypothetical: ADR-0020 contains `sqlb.QueryIn[T](tx)` and compatibility.md
 * contains `OnIn[T](r)` — Go generics that a link-shaped regex reads as links.
 */
function splitCode(md) {
  const parts = [];
  const fence = /^(```|~~~).*$/gm;
  let index = 0;
  let open = null;
  let match;
  while ((match = fence.exec(md)) !== null) {
    if (open === null) {
      parts.push({ code: false, text: md.slice(index, match.index) });
      open = match[1];
      index = match.index;
    } else if (match[0].startsWith(open)) {
      const end = match.index + match[0].length;
      parts.push({ code: true, text: md.slice(index, end) });
      open = null;
      index = end;
    }
  }
  parts.push({ code: open !== null, text: md.slice(index) });

  // Split the prose runs again, on inline code spans.
  const out = [];
  for (const part of parts) {
    if (part.code) {
      out.push(part);
      continue;
    }
    for (const piece of part.text.split(/(`+[^`]*`+)/)) {
      if (piece !== "") out.push({ code: piece.startsWith("`"), text: piece });
    }
  }
  return out;
}

/** Route for a page, given its source and slug. */
function routeFor(source, slug) {
  return slug === "index" ? `${prefix}/${source.route}/` : `${prefix}/${source.route}/${slug}/`;
}

/** List the markdown a source publishes, honouring an explicit file list. */
async function filesOf(source) {
  if (source.files) return source.files;
  return (await readdir(join(repo, source.dir))).filter((f) => f.endsWith(".md")).sort();
}

/**
 * Map every published file to its route, keyed by repo-relative path. A
 * directory holding a README maps too, so a link to the directory reaches its
 * index.
 */
async function buildRouteIndex() {
  const routes = new Map();
  for (const source of SOURCES) {
    for (const file of await filesOf(source)) {
      const slug = file === "README.md" ? "index" : file.replace(/\.md$/, "");
      routes.set(posix.join(source.dir, file), routeFor(source, slug));
      if (slug === "index") routes.set(source.dir, routeFor(source, "index"));
    }
  }
  return routes;
}

/**
 * Resolve one link as written, from a file in `fromDir`, to its web form.
 * Returns null when the target cannot be found at all, which is the error case.
 */
function rewrite(link, fromDir, routes) {
  if (link.startsWith("#")) return link;
  if (/^(https?:|mailto:)/.test(link)) return link;

  const [rawPath, fragment] = link.split("#");
  const hash = fragment ? `#${fragment}` : "";

  // Resolve against the containing directory, then key on the repo-relative
  // path — the same target written three different ways lands on one key.
  const resolved = posix.normalize(posix.join(fromDir, rawPath)).replace(/\/$/, "");
  if (resolved.startsWith("..")) return null; // outside the repository

  const route = routes.get(resolved);
  if (route) return route + hash;

  // Not published, but real: link out to the repository.
  const onDisk = join(repo, resolved);
  if (existsSync(onDisk)) {
    const root = statSync(onDisk).isDirectory() ? TREE : BLOB;
    return `${root}/${resolved}${hash}`;
  }

  return null;
}

/** First H1 becomes the title; the H1 itself is dropped, Starlight renders it. */
function extractTitle(md) {
  const m = /^#\s+(.+)$/m.exec(md);
  if (!m) return null;
  return { title: m[1].trim(), body: md.replace(m[0], "").trimStart() };
}

/** First prose paragraph, flattened, for the page description. */
function extractDescription(body) {
  for (const block of body.split(/\n\s*\n/)) {
    const text = block.trim();
    if (!text || /^[#|>`-]/.test(text)) continue;
    return text
      .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
      .replace(/[*`_]/g, "")
      .replace(/\s+/g, " ")
      .trim()
      .slice(0, 300);
  }
  return null;
}

async function transform(source, routes, problems) {
  const files = await filesOf(source);
  if (files.length === 0) throw new Error(`sync-docs: no markdown found in ${source.dir}`);

  const pages = [];
  for (const file of files) {
    const path = join(repo, source.dir, file);
    if (!existsSync(path)) {
      problems.push(`${source.dir}/${file}: listed in SOURCES but not on disk`);
      continue;
    }
    const raw = await readFile(path, "utf8");
    const extracted = extractTitle(raw);
    if (!extracted) {
      problems.push(`${source.dir}/${file}: no H1, so the page has no title`);
      continue;
    }
    const { title, body } = extracted;
    const slug = file === "README.md" ? "index" : file.replace(/\.md$/, "");

    let rewritten = "";
    for (const part of splitCode(body)) {
      if (part.code) {
        rewritten += part.text;
        continue;
      }
      rewritten += part.text.replace(/\]\(([^)\s]+)\)/g, (whole, link) => {
        const next = rewrite(link, source.dir, routes);
        if (next === null) {
          problems.push(`${source.dir}/${file}: ${link} resolves to nothing in the repository`);
          return whole;
        }
        return `](${next})`;
      });
    }

    const order = source.order(slug);
    if (order < 0) {
      problems.push(`${source.dir}/${file}: no sidebar position, so its order is undefined`);
    }

    const label = source.label?.(slug, title) ?? null;
    const description = extractDescription(rewritten);
    // Per-page, because Starlight would otherwise derive it from the generated
    // file's location and send "edit this page" at the copy.
    const editUrl = `${GITHUB}/edit/main/${source.dir}/${file}`;

    const frontmatter = [
      "---",
      `title: ${JSON.stringify(title)}`,
      description ? `description: ${JSON.stringify(description)}` : null,
      `editUrl: ${JSON.stringify(editUrl)}`,
      "sidebar:",
      label ? `  label: ${JSON.stringify(label)}` : null,
      `  order: ${order}`,
      "---",
      "",
      "",
    ]
      .filter((line) => line !== null)
      .join("\n");

    pages.push({
      path: join(contentRoot, source.route, `${slug}.md`),
      body: frontmatter + rewritten,
    });
  }
  return pages;
}

async function main() {
  const routes = await buildRouteIndex();
  const problems = [];
  const bySource = [];
  for (const source of SOURCES) {
    bySource.push({ source, pages: await transform(source, routes, problems) });
  }

  if (problems.length > 0) {
    console.error("sync-docs: the docs cannot be published as they stand:");
    for (const p of problems) console.error(`  ${p}`);
    console.error("\nFix the link, or publish its target by adding it to SOURCES.");
    process.exit(1);
  }

  const total = bySource.reduce((n, { pages }) => n + pages.length, 0);
  if (check) {
    for (const { source, pages } of bySource) {
      console.log(`  ${source.dir} → /${source.route}/  ${pages.length} pages`);
    }
    console.log(`sync-docs: ${total} pages transform cleanly, every link resolves`);
    return;
  }

  for (const { source, pages } of bySource) {
    const dir = join(contentRoot, source.route);
    // Rebuilt from scratch, so a page deleted from docs/ leaves the site too.
    await rm(dir, { recursive: true, force: true });
    await mkdir(dir, { recursive: true });
    for (const { path, body } of pages) await writeFile(path, body);
    console.log(`sync-docs: wrote ${pages.length} pages to src/content/docs/${source.route}/`);
  }
}

await main();
