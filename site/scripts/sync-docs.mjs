// Derive the Starlight content collection from docs/guide.
//
// docs/guide is the source of truth: it is plain markdown, it renders on GitHub,
// and it is what someone reads in the repo. This turns it into what Starlight
// wants — frontmatter, web paths instead of file paths — without a second copy
// for anyone to edit. Nothing here is committed; it is regenerated on every
// build, so there is no drift to gate against.
//
// The one thing it does gate is links. A relative link in the guide points at a
// file in the repository, and most of those do not exist on the site: ../adr/
// resolves to nothing under a web root. So every relative link must match a rule
// below, and one that does not is a hard error rather than a link that 404s
// after deploy. `--check` runs the transform and reports without writing.

import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { base } from "../site.config.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "../..");
const source = join(repo, "docs/guide");
const target = join(here, "../src/content/docs/guide");

// Trailing slash stripped so `${prefix}/guide/...` is well-formed for base "/".
const prefix = base.replace(/\/$/, "");

const GITHUB = "https://github.com/jryannel/sqlb";
const BLOB = `${GITHUB}/blob/main`;
const TREE = `${GITHUB}/tree/main`;

// Sidebar order. A page absent from this list is still built, but sorts last and
// is reported — adding a page to the guide should be a deliberate act here too,
// not a silent append.
const ORDER = [
  "index",
  "getting-started",
  "schema",
  "queries-and-hooks",
  "rest",
  "migrations",
];

// Sidebar titles that should not be the page's H1. Only the index needs one:
// "# Guide" is the right heading on GitHub, where the file is read on its own,
// and reads as "Guide › Guide" in a sidebar that already says Guide.
const TITLES = { index: "Overview" };

const check = process.argv.includes("--check");

/** Split markdown into fenced-code and prose runs, so rewrites skip code. */
function splitFences(md) {
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
  return parts;
}

/**
 * Map one repo-relative link onto its web form.
 * Returns null when no rule matches, which is the error case.
 */
function rewrite(link) {
  // A bare fragment addresses the current page and needs no change.
  if (link.startsWith("#")) return link;
  if (/^(https?:|mailto:)/.test(link)) return link;

  const [path, fragment] = link.split("#");
  const hash = fragment ? `#${fragment}` : "";

  // A sibling guide page becomes a route.
  const sibling = /^([a-z0-9-]+)\.md$/.exec(path);
  if (sibling) {
    const slug = sibling[1] === "README" ? "" : `${sibling[1]}/`;
    return `${prefix}/guide/${slug}${hash}`;
  }
  if (path === "README.md") return `${prefix}/guide/${hash}`;

  // Everything else lives in the repository rather than on the site, so it
  // leaves the site. A trailing slash means a directory.
  const repoRelative = {
    "../../README.md": `${BLOB}/README.md`,
    "../with-sqlc.md": `${BLOB}/docs/with-sqlc.md`,
    "../adr/": `${TREE}/docs/adr`,
  };
  if (repoRelative[path]) return repoRelative[path] + hash;

  const adr = /^\.\.\/adr\/(.+\.md)$/.exec(path);
  if (adr) return `${BLOB}/docs/adr/${adr[1]}${hash}`;

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
    if (!text || text.startsWith("#") || text.startsWith("|") || text.startsWith("```")) continue;
    return text
      .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
      .replace(/[*`_]/g, "")
      .replace(/\s+/g, " ")
      .trim();
  }
  return null;
}

async function main() {
  const files = (await readdir(source)).filter((f) => f.endsWith(".md")).sort();
  if (files.length === 0) throw new Error(`sync-docs: no markdown found in ${source}`);

  const problems = [];
  const written = [];

  for (const file of files) {
    const raw = await readFile(join(source, file), "utf8");
    const extracted = extractTitle(raw);
    if (!extracted) {
      problems.push(`${file}: no H1, so the page has no title`);
      continue;
    }
    const { title, body } = extracted;

    let rewritten = "";
    for (const part of splitFences(body)) {
      if (part.code) {
        rewritten += part.text;
        continue;
      }
      rewritten += part.text.replace(/\]\(([^)\s]+)\)/g, (whole, link) => {
        const next = rewrite(link);
        if (next === null) {
          problems.push(`${file}: no rule for link ${link}`);
          return whole;
        }
        return `](${next})`;
      });
    }

    const name = file === "README.md" ? "index" : file.replace(/\.md$/, "");
    const order = ORDER.indexOf(name);
    if (order === -1) problems.push(`${file}: not in ORDER, so its sidebar position is undefined`);

    const description = extractDescription(rewritten);
    // Per-page, because Starlight would otherwise derive it from this file's
    // location — sending "edit this page" at the generated copy, which is
    // gitignored and the wrong thing to change.
    const editUrl = `${GITHUB}/edit/main/docs/guide/${file}`;
    const frontmatter = [
      "---",
      `title: ${JSON.stringify(TITLES[name] ?? title)}`,
      description ? `description: ${JSON.stringify(description)}` : null,
      `editUrl: ${JSON.stringify(editUrl)}`,
      "sidebar:",
      `  order: ${order === -1 ? 99 : order}`,
      "---",
      "",
      "",
    ]
      .filter((line) => line !== null)
      .join("\n");

    written.push({ path: join(target, `${name}.md`), body: frontmatter + rewritten });
  }

  if (problems.length > 0) {
    console.error("sync-docs: the guide cannot be published as it stands:");
    for (const p of problems) console.error(`  ${p}`);
    console.error("\nAdd a rule to rewrite() in site/scripts/sync-docs.mjs, or fix the link.");
    process.exit(1);
  }

  if (check) {
    console.log(`sync-docs: ${written.length} pages transform cleanly, every link has a rule`);
    return;
  }

  // Rebuilt from scratch, so a page deleted from the guide leaves the site too.
  await rm(target, { recursive: true, force: true });
  await mkdir(target, { recursive: true });
  for (const { path, body } of written) await writeFile(path, body);
  console.log(`sync-docs: wrote ${written.length} pages to src/content/docs/guide/`);
}

await main();
