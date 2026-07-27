// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";

import { base, site } from "./site.config.mjs";

// The guide pages under src/content/docs/guide are generated from docs/guide by
// scripts/sync-docs.mjs and are gitignored. Edit the markdown in docs/guide;
// this directory is output.
export default defineConfig({
  site,
  base,
  integrations: [
    starlight({
      title: "sqlb",
      description:
        "A schema-first data layer for Go: declare your tables once, get typed " +
        "composable queries, a validated REST filter grammar, and domain hooks.",
      social: [
        { icon: "github", label: "GitHub", href: "https://github.com/jryannel/sqlb" },
      ],
      editLink: {
        // For the hand-written pages under src/content/docs, whose paths are
        // real. Generated guide pages override this per page in frontmatter, so
        // "edit this page" lands on docs/guide rather than on the copy.
        baseUrl: "https://github.com/jryannel/sqlb/edit/main/site/",
      },
      sidebar: [
        {
          label: "Guide",
          // autogenerate reads the sidebar.order the sync script writes, which
          // keeps page order in one place: the ORDER list in that script.
          items: [{ autogenerate: { directory: "guide" } }],
        },
        {
          label: "Decision records",
          // Collapsed: 23 records would otherwise push the guide off the screen,
          // and someone arriving at the site is reading the guide first.
          collapsed: true,
          items: [{ autogenerate: { directory: "adr" } }],
        },
        {
          label: "Reference",
          items: [
            {
              label: "API reference (pkg.go.dev)",
              link: "https://pkg.go.dev/github.com/jryannel/sqlb",
              attrs: { target: "_blank" },
            },
            {
              label: "Using sqlb with sqlc",
              link: "https://github.com/jryannel/sqlb/blob/main/docs/with-sqlc.md",
              attrs: { target: "_blank" },
            },
          ],
        },
      ],
    }),
  ],
});
