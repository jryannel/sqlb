// Derive the Starlight content collection from the markdown in docs/.
//
// docs/guide and docs/adr are the source of truth: plain markdown, no
// frontmatter, rendering on GitHub, which is where most people meet them. This
// turns them into what Starlight wants — frontmatter, web paths instead of file
// paths — without a second copy for anyone to edit. Nothing written here is
// committed; it is regenerated on every build, so there is no drift to gate.
//
// What it does gate is links. A relative link points at a file in the
// repository, and only some of those have a page on the site: a sibling ADR
// does, ../review-adoption-readiness.md does not. So every relative link must
// match a rule, and one that does not is a hard error rather than a link that
// 404s after deploy. `--check` runs the transform and reports without writing.

import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
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
 * Each source directory becomes one route on the site.
 *
 * `order` returns a sidebar position and `label` the sidebar text when it should
 * differ from the page's H1. Both take the page's slug — the filename without
 * .md, or "index" for a README.
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
];

const check = process.argv.includes("--check");

/**
 * Split markdown into code and prose runs, so rewrites never touch code.
 *
 * Both fenced blocks and inline spans count. The inline case is not
 * hypothetical: ADR-0020 contains `sqlb.QueryIn[T](tx)`, which is Go generics
 * and looks exactly like a markdown link to a pass that only skips fences.
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

/**
 * Map one repo-relative link onto its web form, seen from `source`.
 * Returns null when no rule matches, which is the error case.
 */
function rewrite(link, source) {
  if (link.startsWith("#")) return link;
  if (/^(https?:|mailto:)/.test(link)) return link;

  const [path, fragment] = link.split("#");
  const hash = fragment ? `#${fragment}` : "";

  // "." addresses the directory the file sits in — its own index.
  if (path === "." || path === "./") return routeFor(source, "index") + hash;

  // A sibling page in the same source directory.
  const sibling = /^([A-Za-z0-9-]+)\.md$/.exec(path);
  if (sibling) {
    const slug = sibling[1] === "README" ? "index" : sibling[1];
    return routeFor(source, slug) + hash;
  }

  // A page in another source directory: ../adr/0011-....md from the guide, which
  // is an internal link now that the records are published rather than a trip
  // out to GitHub.
  const crossSource = /^\.\.\/([a-z]+)\/(?:([A-Za-z0-9-]+)\.md)?$/.exec(path);
  if (crossSource) {
    const other = SOURCES.find((s) => s.dir === `docs/${crossSource[1]}`);
    if (other) {
      const slug = !crossSource[2] || crossSource[2] === "README" ? "index" : crossSource[2];
      return routeFor(other, slug) + hash;
    }
  }

  // Everything else lives in the repository and has no page here, so it leaves
  // the site. A trailing slash means a directory.
  const known = {
    "../../README.md": `${BLOB}/README.md`,
    "../README.md": `${BLOB}/README.md`,
    "../with-sqlc.md": `${BLOB}/docs/with-sqlc.md`,
    "../review-adoption-readiness.md": `${BLOB}/docs/review-adoption-readiness.md`,
    "../architecture.md": `${BLOB}/docs/architecture.md`,
    "../vision.md": `${BLOB}/docs/vision.md`,
    "../compatibility.md": `${BLOB}/docs/compatibility.md`,
  };
  if (known[path]) return known[path] + hash;

  const example = /^\.\.\/\.\.\/(example\/.+)$/.exec(path);
  if (example) {
    const root = example[1].endsWith("/") ? TREE : BLOB;
    return `${root}/${example[1].replace(/\/$/, "")}${hash}`;
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

async function transform(source, problems) {
  const dir = join(repo, source.dir);
  const files = (await readdir(dir)).filter((f) => f.endsWith(".md")).sort();
  if (files.length === 0) throw new Error(`sync-docs: no markdown found in ${dir}`);

  const pages = [];
  for (const file of files) {
    const raw = await readFile(join(dir, file), "utf8");
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
        const next = rewrite(link, source);
        if (next === null) {
          problems.push(`${source.dir}/${file}: no rule for link ${link}`);
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
  const problems = [];
  const bySource = [];
  for (const source of SOURCES) {
    bySource.push({ source, pages: await transform(source, problems) });
  }

  if (problems.length > 0) {
    console.error("sync-docs: the docs cannot be published as they stand:");
    for (const p of problems) console.error(`  ${p}`);
    console.error("\nAdd a rule to rewrite() in site/scripts/sync-docs.mjs, or fix the link.");
    process.exit(1);
  }

  const total = bySource.reduce((n, { pages }) => n + pages.length, 0);
  if (check) {
    for (const { source, pages } of bySource) {
      console.log(`  ${source.dir} → /${source.route}/  ${pages.length} pages`);
    }
    console.log(`sync-docs: ${total} pages transform cleanly, every link has a rule`);
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
