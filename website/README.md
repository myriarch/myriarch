# myriarch website

The sources of the project website (myriarch.dev): Hugo extended with the Docsy theme. The site is built from the files in
this directory and from the Markdown files of the repository that it mounts (`docs/`, the decision records, selected root
files). It is a purely static site. The decision is [ADR-0031](../docs/adr/0031-website-with-hugo-and-docsy.md); the
development guide has the contributor view ([Website](../docs/development.md#website)).

The workflow [`.github/workflows/website.yml`](../.github/workflows/website.yml) builds, checks and deploys the site; see
[Deployment](#deployment). Nothing else in this directory deploys anything.

## Commands

Run them from the repository root. The first run needs network access (Go proxy, npm registry, release downloads); later runs
use the caches in `.cache/`.

| Command | What it does |
|---|---|
| `make website` | production build into `website/public` (`hugo --panicOnWarning`) |
| `make website-serve` | Hugo development server on <http://127.0.0.1:1313> with live reload |
| `make website-preview` | production build, served as static files (with gzip, like a host) on <http://127.0.0.1:1313> |
| `make website-check` | the website gate: build and every check below |
| `ONLY=website make check-local` | the same gate through the gate runner |

All of them call `ci/website.sh`: it puts `.cache/bin` on the `PATH`, installs the pinned tools (`ci/install-tools.sh hugo sass
lychee`), runs `pnpm install --frozen-lockfile` here and keeps Hugo's module cache in `.cache/hugo`. Environment: `WEBSITE_PORT`
(default 1313; 8080 and the ports of the end-to-end tests are refused), `WEBSITE_BASEURL` (another base URL; previews use `/`),
`WEBSITE_ENV=preview` (the production build plus `noindex` and `Disallow: /`; see [Previews](#previews)), `TOOLS_BIN` (the tool directory; a git worktree points it at the main working
copy). Requirements: Go (Hugo fetches the Docsy theme as a Go module), Node.js, pnpm and Python 3; `make website-check` needs
Node.js 22 or newer because of markdownlint-cli2 (`engines` in `package.json`; the script says so before it fails).

## Layout

```text
go.mod, go.sum              the pin of the Docsy module (github.com/docsy/docsy/theme)
package.json, pnpm-lock.yaml  Bootstrap, Font Awesome Free, lunr, Mermaid (shipped), markdownlint-cli2 and the Vercel CLI (deployment); exact versions
config/_default/            hugo.yaml (site), module.yaml (mounts), params.yaml, menus.yaml
content/                    pages that exist only on the website, content adapters (_content.gotmpl)
data/repo_pages.yaml        repository files that adapters render (the root files)
data/third_party.yaml       the notices of /legal/third-party/
data/release.yaml           the release status (latest final release or newest candidate): the one value behind the status chip of the landing page and the version in the structured data of the home page; set by hand after a publication (docs/releasing.md)
data/llms.yaml              the content of /llms.txt (see SEO and GEO)
layouts/                    render hooks (_markup), partials (repo/*, seo/*, csp.html), shortcodes, overrides of Docsy files, 404.html, robots.txt, home.llms.txt (the template of /llms.txt)
assets/scss/                brand tokens (_variables_project.scss) and project styles
assets/js/                  landing-mark.js: the loop of the mark on the landing page and its click-to-pause behaviour (the site's own script; the rest comes with the theme);
                            offline-search.js: the override of the search script of the theme, the index loads on first use (see Search)
static/                     site.webmanifest
scripts/                    the checks (check-*.py), their tests (test_checks.py, testdata/), the browser checks (*.mjs), the preview server (preview-server.py)
overrides.txt               SHA-256 of the upstream file behind every override of a Docsy file
lychee.toml, .markdownlint-cli2.jsonc   configuration of the link check and of markdownlint
```

The build output `public/` and Hugo's `resources/` are git-ignored, as are `node_modules/` and `.hugo_build.lock`.

## What is mounted

`config/_default/module.yaml` lists the mounts. Explicit mounts replace Hugo's defaults, so the defaults (content, static,
layouts, assets, data, i18n, archetypes) are listed first.

- `../docs` becomes `content/docs`. The decision records are left out of that mount (`! adr/**`): a content adapter renders
  them. `docs/adr/README.md` and `docs/design/README.md` become the section indexes.
- The root files (`ROADMAP.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, `GOVERNANCE.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`,
  `MAINTAINERS.md`, `ADOPTERS.md`, `CONTRIBUTOR_LADDER.md`) are mounted as assets under `repo/` and rendered by
  `content/project/_content.gotmpl` from the list in `data/repo_pages.yaml`. To add a root file, mount it in `module.yaml` and add
  an entry to the list.
- `../assets/brand` is `static/brand`; the favicons, the touch icon, the manifest icons and the Open Graph image are mounted at
  the places that `layouts/_partials/favicons.html` and `params.images` expect.
- `node_modules` provides Bootstrap and Font Awesome (the theme mounts them relative to its own module directory, where they do not
  exist), lunr and Mermaid. `../api/openapi.yaml` is `assets/api/openapi.yaml`: the REST API reference (`docs/reference/api`,
  generated from the contract by `make docs-gen` and mounted with the rest of `docs/`) offers exactly that file for download, so no copy can drift.

## Link rules

The Markdown files are written for GitHub, and the site renders the same files. `layouts/_markup/render-link.html` maps each link:

1. A link with a scheme (`https:`, `mailto:`, `tel:`) stays; external links get `rel="noopener"`; any other scheme fails the build.
2. `#fragment` stays. Heading ids follow the GitHub slugs, so existing anchors work.
3. A site-absolute link (`/docs/`) is allowed in `website/content` only; a link to a page that does not exist fails the build.
4. A relative link is resolved against the repository path of the page (`docs/tasks/x.md` plus `../concepts/y.md` is
   `docs/concepts/y.md`). If a page of the site renders the target, the link points to it, including the section index
   (`_index.md`, `README.md`) of a directory; any other repository file or directory links to GitHub (`blob` or `tree` on the
   branch of `params.repo`). A link that leaves the repository fails the build.

`render-image.html` resolves an image to a resource of the page or of the site and fails the build otherwise: an image from
another host, or from GitHub, would be a request to a third party.

The repository path of a page comes from `layouts/_partials/repo/source-path.html` (`params.sourcePath` for adapter pages, the
file name relative to the repository root otherwise). A page that stands for other files lists them in `params.represents`
(the index of the API reference: `api/openapi.yaml`), a placeholder in `params.placeholder.replaces`; both are found by the link hook.

Do not write a double opening brace followed by `<` or `%` in a Markdown file that the site renders (not even in a code block):
Hugo takes it for a shortcode.

## Adding a page

- **A documentation page**: a file in `docs/` with front matter (`title`, `weight`, `description`; see the
  [development guide](../docs/development.md#documentation)). It appears in the site by its path and weight.
- **A website-only page**: a file in `content/` with `title` and `description`; `weight`, `linkTitle`, `params` and the other keys
  that `python3 -I scripts/check-docs.py front-matter --site` allows are optional. Site-absolute links are fine here.
- **A placeholder** for a page that a later change will write: a file at the website path of the page it replaces
  (`docs/operations/upgrade.md` is `content/docs/operations/upgrade.md`) with

  ```yaml
  params:
    placeholder:
      tracking: "operations-manual"
      replaces: "docs/operations/upgrade.md"
      planned: "v0.2.0"
  ```

  and the `placeholder` shortcode as its body. The tracking value is never rendered; it is a lowercase topic slug (`operations-manual`) or a GitHub issue reference, which must be quoted (`"#123"`; an unquoted `#` starts a YAML comment). An optional `planned: "v0.2.0"` under `placeholder` makes the notice say
  that the page is planned for that release. When `docs/operations/upgrade.md`
  exists, the placeholder has to go in the same change (`scripts/check-placeholders.py`).

## Search

Search runs in the browser: Docsy's offline search with lunr (`offlineSearch: true` in `params.yaml`). Hugo writes the index as a
fingerprinted file of the site (`/offline-search-index.<hash>.json`, about 1.2 MB of JSON and 375 KB compressed for about 150 pages), and a
script turns it into a lunr index in the browser. That is one long task (about 0.1 s on a laptop, about 0.5 s with the throttling of
Lighthouse's mobile profile). The script of the theme did it at `DOMContentLoaded` of every page: it was the only long task of the landing
page and cost 440 to 490 ms of Total Blocking Time (PageSpeed Insights, October 2026: performance 72 on mobile and 80 on desktop), although most
visitors never search. `assets/js/offline-search.js` is therefore an override of the theme's script that changes how the index is loaded and
what happens when that fails:

- The first focus, pointerdown or input event on any search box fetches the index and builds the lunr index. All boxes of a page share one
  promise, so this happens once, however often and in whichever box the visitor searches. A page that nobody searches in never requests the
  file, and Lighthouse, which does not search, does not either.
- A query that is typed and submitted (Enter, or the box loses the focus) before the index has arrived is rendered as soon as it has. The
  rendering, the popover, the submit handling and the maximum number of results (`offlineSearchMaxResults`) are the code of the theme.
- A failed load is not final. The theme made one attempt per page view, at the page load, and logged a failure. The first use of a box can
  come much later, and then two failures are likely: the network is down for a moment, or the tab was opened before a deployment and still
  names the fingerprinted index file of the old build (`offline-search-index.<hash>.json`), which the host no longer serves (it answers
  every path without a file with the 404 page). The script logs "Could not load the search index" and forgets the attempt: the next query
  that is submitted (Enter, or the box loses the focus with a changed value) requests the index again, one request per query. A query that
  was waiting for the failed load gets the hint "The search index could not be loaded. Reload the page to search." in its popover. A tab
  from before a deployment cannot load the index until the page is reloaded; the hint says so.
- The focus stays in the box, which is a deliberate difference, too: when the index was ready, the theme's script dispatched a change
  event on every box, which also removed the focus from the box. That was harmless when the index was ready long before the first query;
  now it arrives while the visitor types the first query, so the override renders only the queries that were submitted in the meantime and
  leaves the focus alone.
- `scripts/check-requests.mjs` fails when a page of `request-pages.txt` requests the index while it loads, when the search requests it other
  than once, and when the search does not recover from a failed load: it answers the index request with a network error or with the 404 page
  (through the Fetch domain of the DevTools protocol, not by the server) and expects the retry with the next query, the hint for a query that
  waited for the failed load, and one console error per failed load (see [Checks](#checks)).

## SEO and GEO

The site tells search engines and AI assistants what it is, which URL is the original of a page and what they may read (SEO is search
engine optimization; GEO is the same for generative engines, which means making the site easy for AI assistants to read and cite). All of it
is part of the build and uses no third party. `scripts/check-seo.py` fails the website gate when a page or a file leaves the rules below.

| What | Where it comes from | What the build writes |
|---|---|---|
| Canonical link | `layouts/_partials/seo/canonical-*.html`, `params.seo.canonicalBase` | One absolute `<link rel="canonical">` per page, always below the production origin. The origin is fixed: a preview is built with `WEBSITE_BASEURL=/` and still names the production page as the original. `canonicalBase` must equal `baseURL` of `hugo.yaml`. The 404 page has none |
| Title of the home page | `params.seo.homeTitle` | The `<title>` of `/`; every other page is titled `<page title> \| myriarch`. The Open Graph and Twitter titles of the home page stay `myriarch`, which the description completes |
| Structured data | `layouts/_partials/seo/` | JSON-LD for schema.org, see [Structured data](#structured-data) |
| `llms.txt` | `data/llms.yaml`, `layouts/home.llms.txt` | `/llms.txt`, announced by a `<link rel="describedby">` in the head and by a visually hidden line at the start of the body of every page (`llms-directive.html` of Docsy), see [llms.txt](#llmstxt) |
| `robots.txt` | `layouts/robots.txt` | Production allows every crawler and names the sitemap; a preview disallows everything |
| 404 page | `layouts/404.html`, `layouts/_partials/head.html` | `noindex`, no canonical link and no structured data, see [The 404 page](#the-404-page) |

`layouts/_partials/seo/head.html` is the one partial that `head.html` calls; it writes the canonical link and chooses the structured data.

**Spelling.** [ADR-0015](../docs/adr/0015-project-name-and-naming-scheme.md) decides it: `myriarch` in identifiers and in the middle of a
sentence, `Myriarch` at the start of a sentence or a title. The home title, the text of `data/llms.yaml` and the structured data follow it.
The site title of `hugo.yaml` (`myriarch`) is a name in that sense: it ends every other title and is the `name` in the structured data.
`check-seo.py` lints the home title and `llms.txt`; names of programs, hosts and paths (`myriarchd`, `myriarch-updater`, `myriarch.dev`)
are identifiers and always lower case.

### Structured data

- **The home page** has one `SoftwareSourceCode` (name, description, `codeRepository`, license Apache-2.0, `programmingLanguage`, keywords,
  author and copyright holder "The Myriarch Authors", and `version`) and one `WebSite`. The version is the latest release of
  `data/release.yaml` without the leading `v` (`0.1.0`, or `0.2.0-rc.1` while only a candidate exists); it is left out while no release
  exists. It is the property `version`: `softwareVersion` is defined for `SoftwareApplication`, not for `SoftwareSourceCode`. The home page
  does not use `SoftwareApplication` on purpose, because Google's Rich Results Test reports errors for it without ratings, and ratings are
  never made up. The `WebSite` has no `SearchAction`: the search runs in the browser and has no URL that takes a query.
- **A page with a visible breadcrumb** (the documentation layout: `/docs/`, `/project/` and `/legal/`) has one `BreadcrumbList`: the home
  page, then the items of the visible breadcrumb with their absolute URLs, the current page included. The home page is added because a
  page at the first level shows only one item.
- **Every other page** has no structured data.

The facts are in `config/_default/params.yaml` (the block `seo`: license, languages, keywords; and `description`, `repo.url`, `copyright`)
and in `data/release.yaml`; no template repeats them. `jsonify` writes `<`, `>` and `&` as escapes, so no value can end the script element.
`check-seo.py` parses every block, compares it with these sources and with the visible breadcrumb, and rejects a property that its type does
not define on schema.org (`PROPERTIES` in the script lists the ones the site uses; look a new property up on schema.org before you add it).

### llms.txt

`/llms.txt` follows [llmstxt.org](https://llmstxt.org/): the name of the site as the heading, a one-paragraph summary in a block quote,
paragraphs of background, then sections of links; the section "Optional" holds what a reader may skip. It is curated, not generated from
the pages: `data/llms.yaml` holds the summary, what the project does, for whom, its status and its independence, the key terms and the
links, and explains its keys. A link names a page by its path, and the build fails when the page does not exist. The URL written is the
canonical one, so the file is the same in a preview. Docsy's own template (`all.llms.txt`, a listing of the main menu and the sections of the
documentation) is replaced for the home page by `layouts/home.llms.txt`; `outputs.home` in `hugo.yaml` switches the output format `LLMS` on.

`check-seo.py` enforces: links lead only to pages of the site and to the repository, never to a placeholder page, and no link appears
twice; the file is Markdown without HTML tags and at most 64 KiB, and it is spelled as described above. Write only what the documentation
backs, and name neither the release status nor the support levels, which change.

### Crawler policy

Every crawler may read the whole site, those of search engines and those of AI assistants alike. In production `robots.txt` blocks no path
(an empty `Disallow:`) and says so in a comment. The reasons are that the documentation is public on GitHub too, that the site holds nothing that is
not meant to be read, and that people ask assistants about the documentation. The firewall of the host runs Bot Protection and the AI-bot
rule in log mode only ([Setup](#setup-once-by-a-maintainer), step 6): it records bot traffic and never blocks or challenges it. A crawler
that harms the site is handled as an incident ([Firewall incidents](#firewall-incidents)), not by a rule in `robots.txt`, which
`check-seo.py` would reject. Evaluating crawler requests, for example the logs of the host, is not part of this: the privacy page says that the
project receives no host logs ([Privacy](#privacy)), so it would need a decision record first.

### The 404 page

The host answers a path without a file with status 404 and the content of `404.html`. The file also exists under its own address, which a
static host may answer with status 200, so the page carries `noindex` in production too (`check-indexing.py` exempts it for that reason). It
has no canonical link and no structured data, and its content links back to the home page, the documentation and the project.
`check-seo.py` checks all of it.

### Runbook

The steps of the maintainer. None of them needs a file in the repository.

1. **Search Console** (after the first production deployment, and when the domain changes). Add `myriarch.dev` as a *Domain* property and
   verify it with the DNS TXT record that Search Console shows, at the registrar; a verification file or meta tag would need a change in
   the repository. Submit `https://myriarch.dev/sitemap.xml` under *Sitemaps*. Inspect `https://myriarch.dev/` with *URL inspection*: the
   user-declared canonical must be the page itself.
2. **Bing Webmaster Tools.** Import the site from Search Console (*Import your sites from Google Search Console*); the sitemap comes with it.
3. **Structured data.** Test `https://myriarch.dev/` with the [Schema Markup Validator](https://validator.schema.org/): `WebSite` and
   `SoftwareSourceCode` without errors. Test one page of the documentation with the
   [Rich Results Test](https://search.google.com/test/rich-results): the `BreadcrumbList` is valid. `SoftwareSourceCode` is not a rich
   result type of Google, and the test reports errors for `SoftwareApplication` without ratings, which is why the home page uses the
   first.
4. **The live site.** `curl -sI https://myriarch.dev/llms.txt` answers 200 with `text/plain`; `curl -s https://myriarch.dev/robots.txt`
   shows an empty `Disallow:` and the sitemap line; `curl -sI https://myriarch.dev/no-such-page/` answers 404, and the body is the page of
   `404.html`.
5. **The GitHub repository,** before the announcement: the description "GitOps-governed fleet manager for OpenTelemetry Collectors, built
   on OpAMP", and the topics `opentelemetry`, `opamp`, `fleet-management`, `gitops` and `observability`.
6. **Lighthouse** on the production build, as in the [development guide](../docs/development.md#website).

## Overrides of Docsy files

Files in `layouts/` and `assets/` with the same path as a file of the theme replace it. They are few and each starts with a comment that
names the upstream file: `head.html` (self-hosted lunr, brand colors, the CSP first, the home title, `noindex` on the 404 page, and the canonical link and
structured data through the partial `seo/head.html`; see [SEO and GEO](#seo-and-geo)), `favicons.html`, `page-meta-links.html` (edit links
from the repository path), `scripts/plugins/mermaid.html` (self-hosted Mermaid), `footer/copyright.html`, `404.html` and
`assets/js/offline-search.js` (the search index loads on first use, see [Search](#search); the changed parts are marked "Changed from
upstream"). `overrides.txt` pins the SHA-256 of the upstream file of each. `REUSE.toml` (annotation n) adds "Copyright The Docsy Authors"
(Apache-2.0) to the project header of each of these files, so that the derivation is machine-readable; add a new override there as well
(`scripts/check-overrides.py` fails when the two lists differ, and when a file of `layouts/` or `assets/` shadows a theme file without a
pin). The three project style files (`assets/scss/_variables_project.scss`, `_variables_project_after_bs.scss` and `_styles_project.scss`)
are no overrides: Docsy ships them as empty placeholders and asks every project to replace them, so they need neither a pin nor the
attribution; the check fails only when a Docsy update puts content into one of them. To update Docsy:

1. Change the version in `go.mod`, run `cd website && hugo mod get github.com/docsy/docsy/theme@<version>` (updates `go.sum`) and
   `hugo mod verify`; the module path changed once already (0.18).
2. `python3 -I website/scripts/check-overrides.py` lists the upstream files that changed. Compare each with the override, port the
   change, then update the digest in `overrides.txt` (`shasum -a 256 <theme dir>/<path>`).
3. Update `Docsy` in `data/third_party.yaml`, the Mermaid version in `config/_default/params.yaml` if the theme moved it, and run
   `make website-check` and the browser check (`node website/scripts/check-requests.mjs`).

## Updating Mermaid

Mermaid is self-hosted: `config/_default/module.yaml` mounts `mermaid.esm.min.mjs` and `chunks/mermaid.esm.min/**` from
`node_modules/mermaid/dist` to `/vendor/mermaid/`, and the Docsy plugin imports the first file. A new version needs, besides
`package.json` and the lockfile:

1. The version in `config/_default/params.yaml` and in `data/third_party.yaml` (`check-notices.py` fails on a difference).
2. A check that the mounted files still exist and that the chunks import only each other (a relative `import` that points outside
   `chunks/mermaid.esm.min/` would be a 404 in the built site), and that no file of the bundle names another host
   (`check-third-party.py` scans `public/`).
3. A look at every diagram, not only at the build: `check-requests.mjs` stops at the first SVG of a page, and a diagram that fails to
   parse is also an SVG. The diagrams are in `docs/architecture.md` (four) and `docs/design/version-selection.md` (one); open both
   pages in the light and in the dark appearance and compare them with the previous version.
4. The release notes of the major versions in between. Mermaid 12 made the ELK layout, the redux-color theme and the neo look the defaults, so the
   `options` of the plugin in `params.yaml` pin `layout: dagre`, `look: classic` and `theme: default`, which render the diagrams as
   Mermaid 11 did. Drop the options only together with a review of every diagram; the ELK layout of the overview in
   `docs/architecture.md` is too small to read in the width of the content column. Keep the `version:` first in that line, because
   `check-notices.py` reads it with a pattern.
5. The browser floor. Mermaid 12 needs an ES2024 browser (Safari 17.4 or newer, for example). In an older browser the Docsy plugin
   only logs "Mermaid failed to render" and the page shows the diagram source instead of the diagram.

## Node dependency overrides

`package.json` pins its direct dependencies exactly and `pnpm-lock.yaml` pins everything below them. The Vercel CLI and
markdownlint-cli2 pin some of their own dependencies exactly, so `pnpm update` cannot reach a fixed release. Until the tool ships
one, `pnpm.overrides` in `package.json` replaces the vulnerable version with the first release that no advisory covers (checked with
`pnpm audit` and the GitHub Advisory Database). JSON allows no comments, so the reasons are here:

| Override | Replaces | Why |
|---|---|---|
| `@vercel/container>tar@<7.5.21` | tar 7.5.11 | pinned by `@vercel/container`; 7.5.21 is the first release without an advisory (7.5.18, the fix that the Dependabot alerts name, is still affected by GHSA-23hp-3jrh-7fpw and GHSA-r292-9mhp-454m). The key names the parent, so the tar 7.5.22 of `@mapbox/node-pre-gyp` and `@vercel/fun` stays |
| `@vercel/python-analysis>minimatch@<10.2.3` | minimatch 10.1.1 | pinned by `@vercel/python-analysis`. The key names the parent, so the minimatch 10.2.6 that `glob` resolves from its `^10.2.2` stays |
| `js-yaml@>=4.0.0 <4.3.2` | js-yaml 4.1.1 | pinned by `@vercel/python-analysis`. The range is the affected 4.x line: its upper bound keeps js-yaml 5 (markdownlint-cli2) out, its lower bound a js-yaml 3 |
| `smol-toml@<1.9.0` | smol-toml 1.5.2 and 1.8.0 | pinned by the Vercel CLI, its builders and markdownlint-cli2 |
| `undici@<6.0.0` | undici 5.28.4 and 5.29.0 | pinned by the Vercel CLI and `@vercel/node`; undici 5 has no fix. The CLI imports undici only for its proxy support. undici 6 no longer depends on `@fastify/busboy`, which leaves the lockfile |

pnpm applies a key when the version range that a package declares for the dependency intersects the range of the key. The keys
therefore name the vulnerable versions and not the version that a tool pins today, and a key with a parent names the parent without
a version. An entry keeps working across releases of the parent and stops by itself once the parent pins a fixed version. After an
update of the Vercel CLI or markdownlint-cli2, run `pnpm install` here and `pnpm audit`, and drop an entry when removing it changes
nothing in `pnpm-lock.yaml` but the `overrides:` block that records the entries in effect.

`pnpm audit` does not come back clean. These findings remain, each for a reason; a finding that is missing from this table needs a
decision, an override or a row here:

| Finding | Package and path | Why it stays |
|---|---|---|
| GHSA-j3q9-mxjg-w52f, GHSA-27v5-c462-wpq7 | path-to-regexp 8.3.0 under `vercel > @vercel/backends` | `@vercel/backends` is the builder for backend projects. It runs in `vercel build`, which this repository never calls: the deploy job runs `vercel deploy --prebuilt`. GitHub dismissed the alerts automatically (development scope) |
| GHSA-2g4f-4pwh-qvx6 | ajv 8.6.3 under `vercel > @vercel/backends > @vercel/static-config` | The same builder, the same reason |
| GHSA-vfj7-8cjw-p6xm | braces 3.0.3 under `markdownlint-cli2 > micromatch` | No fixed release exists. markdownlint-cli2 expands the glob patterns of its configuration and command line, which maintainers write. GitHub dismissed the alert automatically |
| GHSA-238p-pmpm-9mq7 | katex 0.16.47 under `mermaid` and under `markdownlint-cli2 > markdownlint > micromark-extension-math` | There is no override; see below |

KaTeX has no override because it would not change what the site serves. Mermaid and markdownlint accept only a KaTeX range below
0.18, which excludes the fix (0.18.2), and the Mermaid files that the site ships (`mermaid.esm.min.mjs` and
`chunks/mermaid.esm.min/**`) contain their own copy of KaTeX (0.16.47 in Mermaid 11.17.2 and in 12.1.0). An override would only change the
installed copy under `node_modules`, which nothing that the site ships uses; markdownlint loads it but parses Markdown without
rendering math. The lockfile would then report a version that the site does not serve, and a later advisory that affects the
shipped copy but not 0.18.2 would raise no alert. The advisory needs an `Object.prototype` that is already polluted and
attacker-controlled math in a page, and the site renders only diagrams that maintainers write. The shipped copy changes with a
Mermaid release that bundles KaTeX 0.18.2 or later; the Dependabot alert for the advisory is dismissed as a tolerable risk with this
paragraph as the reason.

## Checks

`make website-check` runs, after the build: `hugo mod verify`, lychee (offline, with fragments), markdownlint, the front matter of
`content/`, every `scripts/check-*.py` over `public/` and the unit tests (`python3 -I -m unittest discover -s website/scripts -p
'test_*.py'`). The checks:

| Check | Fails on |
|---|---|
| `check-third-party.py` | a script, style, font, image, frame, worker or import from another host in the built pages, styles and scripts |
| `check-local-paths.py` | the repository root, `/Users/...` or `/home/...` in the built site |
| `check-repo-links.py` | a link into the GitHub repository (blob, tree, edit) whose path does not exist in the working tree |
| `check-overrides.py` | a changed upstream file, a missing or an unlisted override (a file of `layouts/` or `assets/` that shadows a theme file) |
| `check-indexing.py` | a preview that can be indexed (no `Disallow: /`, a sitemap line, a page without `noindex`) or a production site that hides itself; the mode comes from `WEBSITE_ENV` |
| `check-placeholders.py` | a placeholder whose page now exists, or a malformed placeholder |
| `check-api-spec.py` | a missing API reference page, a download that is not exactly one fingerprinted copy of `api/openapi.yaml`, a page that loads a script of an API viewer, or a number of operation sections that differs from the contract |
| `check-landing.py` | a missing or malformed screenshot, a section or the mark of the landing page that is broken, an image without size or alt text, or a status chip that differs from `data/release.yaml` (or a malformed `release.yaml`) |
| `check-notices.py` | a shipped dependency without a notice, with another version in `data/third_party.yaml`, or without a published license text |
| `check-seo.py` | metadata for search engines and AI crawlers that differs from the configuration or breaks a rule of [SEO and GEO](#seo-and-geo): a page without exactly one canonical link to its own production URL, JSON-LD that is invalid, has a property its type does not define, names another version than `data/release.yaml` or a breadcrumb that differs from the visible one, a home title that is the bare site title, an image without alt text, an `llms.txt` that is missing, malformed, misspelled or links outside the site and the repository, a 404 page that can be indexed, a production `robots.txt` that blocks a crawler, a sitemap that differs from the canonical links; the mode comes from `WEBSITE_ENV` |

A new check is a file `scripts/check-<name>.py` that takes the built site as its argument; the runner finds it. Outside the gate:

- `node website/scripts/check-requests.mjs` (needs `make website-preview` running, Node.js 22 and Chrome; the pages are in
  `scripts/request-pages.txt`) loads every page in light and dark and fails on a request to another origin, a CSP violation or a console
  error, a page that requests the search index while it loads, a search that requests it other than once (focus, pointerdown, input, or
  a click and typing in a box: each must start exactly one request, and a query that is submitted at once must be rendered when the
  index arrives), a search that does not recover from an index that cannot be loaded (the check answers the request with a network error
  or with the 404 page: the next query must request the index again and find results, and a query that waited for the failed load must
  get a hint), a search that finds nothing for an operation or a heading, a diagram that does not render, an API page without its
  tables, and the mark on the landing page (about a minute, in light and dark): its rollout must play and end on identity transforms,
  the unchanged mark must be held for 6 to 8.5 s, the dots must retract and a second cycle must start; the mark is its own pause control
  (no visible button: `role="button"`, `tabindex="0"`, a name, `aria-pressed`, a pointer cursor): a click, or Enter or Space on the
  focused mark, must stop the motion and show the static mark, and the next one must resume at the retraction that ends a cycle (the
  dots shrink from the full mark, the ring does not grow again); `aria-pressed` must follow; the focus ring (contrast 3:1) must show
  for keyboard focus and not after a mouse click; nothing may run while the hero is out of view or the document is hidden; with reduced
  motion nothing may animate and the mark must have no role and no `tabindex`; without JavaScript the rollout must play once and the
  mark must be a plain image.
- `python3 -I website/scripts/lighthouse-scores.py --min 90 <report.json>` evaluates Lighthouse reports (see the development guide). The
  preview server (`scripts/preview-server.py`) compresses text files like a host does, so its scores match what visitors get.

## Privacy

No analytics, no cookies, no request to a third party. Fonts are the system font stack plus the self-hosted icon font. Production
pages carry this Content Security Policy as a meta tag (`layouts/_partials/csp.html`): `default-src 'self'`; scripts and styles
`'self' 'unsafe-inline'` (the theme has small inline scripts and styles); images and fonts `'self' data:`; `connect-src 'self'`;
workers `'self' blob:`; no frames, no objects. A page can widen a directive with `params.csp` (a map from directive to a list of
sources); the checks above still fail on a request to another host. `frame-ancestors`, HSTS and `X-Content-Type-Options` cannot be set
in a meta tag: they belong to the deployment (see [Deployment](#deployment), Follow-ups).

## Hosting

The output is `website/public`: plain files with an `index.html` per page, `/404.html`, `/sitemap.xml` and `/robots.txt`. There are
no functions and no host-specific configuration in the repository (no `vercel.json`, no rewrites), so the directory can be served by
Vercel, Netlify, GitHub Pages or any static server. The one host-specific item, the route to `404.html` that Vercel needs, is
generated by the deployment workflow and never committed (see [Deployment](#deployment)).

The site is built in the project's own CI with the pinned tools and deployed as prebuilt static output, so Vercel never executes
repository code and the same directory goes to another host unchanged. Not used: the built-in Hugo preset of Vercel. It has no Dart
Sass, takes the Hugo version from an environment variable and runs the build on the host.

## Previews

`WEBSITE_ENV=preview` builds the site with the production pipeline (the same minified and fingerprinted assets, no source maps) and
sets only the site parameter `params.preview`, which switches `robots.txt` to `Disallow: /` (without a sitemap line) and every page
to `<meta name="robots" content="noindex, nofollow">`. The Hugo environment `preview` is not used: Docsy treats every environment
except `production` as development and would ship unminified assets and a source map with the path of the build machine, so such a
preview would not be the bytes that production ships. A preview has no known host name at build time (the URL of a Vercel
deployment exists only after the upload), so it is built with `WEBSITE_BASEURL=/` and root-relative URLs. `check-indexing.py`
verifies the mode on the built site. The canonical links, the structured data and `llms.txt` of a preview name the production origin all the
same (`params.seo.canonicalBase`, [SEO and GEO](#seo-and-geo)), so a preview never names itself as the original; `check-seo.py` runs in both modes. A build for another host (`WEBSITE_BASEURL`) is
checked against that host for the sitemap and for the link to `llms.txt`; its canonical links stay on the production origin.

## Deployment

[`.github/workflows/website.yml`](../.github/workflows/website.yml) runs on pushes to `main` and on pull requests that change the
website or one of its inputs (`website/`, `docs/`, `api/openapi.yaml`, `assets/brand/`, the root Markdown files that the site
renders, the two mounted README files, the build scripts and the workflow itself), and on demand (`workflow_dispatch`, from
`main` only). A change elsewhere is deployed with the next website change, or by a manual run.

| Job | What it does | Secrets |
|---|---|---|
| `build` | installs the pinned tools, runs `make website-check` (with `WEBSITE_ENV=preview` and `WEBSITE_BASEURL=/` for a pull request) and uploads `website/public` | none |
| `deploy` | installs the pinned Vercel CLI from `pnpm-lock.yaml` without lifecycle scripts, assembles `.vercel/output` from the uploaded site and runs `vercel deploy --prebuilt` | the environments `website-preview` and `website-production` |

- **Production** on every push to `main` (and on a manual run from `main`): `vercel deploy --prebuilt --target=production`.
  Previews run `vercel deploy --prebuilt --target=preview`.
- **The first deployment is production.** Vercel makes the first deployment of a project a production deployment, whatever the
  CLI asks for, and assigns the production domains to it
  ([Vercel documentation](https://vercel.com/docs/deployments/environments)). The deploy job therefore refuses a preview while the
  project has no production deployment (`targets.production` of the project is empty); the first deployment of a new project comes
  from `main`, for example by a manual run.
- **Previews** for pull requests from branches of this repository, never for forks. The `deploy` job does not run for a fork, and
  not for a Dependabot pull request either (it has no Actions secrets); both get the build and the gate only. Previews are
  protected by Vercel Authentication, built with `WEBSITE_ENV=preview` and marked `noindex`.
- **Concurrency:** a newer push to a pull request cancels the running preview. A run on a branch is never cancelled; a newer pending
  run of the same ref replaces an older pending one, so the newest commit of `main` wins and a manual run from another branch cannot
  replace a pending production run.
- **Output:** the job summary lists the deployment URL, the commit and, for production, the site address. The workflow writes no pull
  request comment and needs no write permission (`contents: read`).
- **Guards:** the deploy job fails when `index.html`, `404.html` or `robots.txt` is missing, when a preview lacks `Disallow: /`, when
  production has it or lacks the absolute sitemap line, when a secret is empty, when the CLI does not print a
  `https://<name>.vercel.app` URL, when a preview would be the first deployment of the project, and when the target that Vercel
  recorded for the deployment (read back through the API) is not the requested one.
- **`404.html` and trailing slashes:** the Build Output API serves neither `404.html` nor the redirect from `/docs` to `/docs/` by
  itself. The deploy job writes `.vercel/output/config.json` with three routes: a `308` redirect for every path without a final slash
  and without a dot (the route that `vercel build` generates for `trailingSlash`), `{"handle":"filesystem"}`, and a catch-all that
  answers `404` with `404.html`. The job tests the redirect pattern against sample paths. The file lives only in the job
  (`/.vercel/` is git-ignored) and is not part of `website/public`.
- **Actions:** only GitHub-owned actions, each pinned to a commit SHA with the version in a comment. pnpm comes from corepack
  (`packageManager` in `package.json`), hugo, sass and lychee from `ci/install-tools.sh`, the Vercel CLI from the lockfile. Nothing
  is cached: the job handles a token, and the downloads are checksum-verified.

### Setup (once, by a maintainer)

1. **Vercel project without a Git integration.** Create a project in the team of the project (dashboard, or `vercel project add`),
   without connecting the repository. Framework preset **Other**, **Root Directory** empty (the CLI looks for `.vercel/output`
   below it), no build command, no output directory. Deployment Protection **Standard**: previews and generated URLs sit behind
   Vercel Authentication, the production domain is public. Turn the **Vercel Toolbar off** for previews (it injects a script from
   `vercel.live`, a request to a third party that the privacy rules forbid), keep Web Analytics and Speed Insights off, and set a
   retention policy for preview deployments.
2. **Token.** Create a Vercel access token scoped to the team, with an expiration date, named for the workflow. It is team-scoped,
   not project-scoped. Rotate it before it expires; an expired token fails the deploy job loudly.
3. **Two GitHub environments** (repository settings, Environments), each with the three secrets `VERCEL_TOKEN`, `VERCEL_ORG_ID` (the
   team ID, `team_...`) and `VERCEL_PROJECT_ID` (`prj_...`; both appear in the project settings):
   - `website-production`: deployment branches *Selected branches*, only `main`. Required reviewers can be added here later without
     touching previews.
   - `website-preview`: deployment branches *No restriction*. A pull request job is evaluated against `refs/pull/<n>/merge`, so a
     branch policy would block previews; the `if:` of the `deploy` job keeps forks and Dependabot out. No required reviewers, since
     they would gate every preview.

   The two environments keep the deployment link of the repository for production from flipping to a protected preview URL.
4. **Domains.** Add `myriarch.dev` (primary) and `www.myriarch.dev` (redirect `308` to the apex) to the project. At the registrar set
   an apex `A` record and a `www` `CNAME` to exactly the values that the Vercel dashboard shows for the project. Remove the parking
   `AAAA` record and every other parking or forwarding record: Vercel serves no IPv6 for the custom domain, and the certificate
   validation prefers IPv6, so a stale `AAAA` record blocks the certificate and sends IPv6 visitors to the parking page. A `CAA`
   record, if present, must allow `letsencrypt.org`.
5. **Required checks.** Do not make `website` a required status check: its path filters would block pull requests that do not touch
   the website. If the gate should be required, run it from the general CI workflow instead of this one.
6. **Firewall.** Bot Protection and the managed rule for AI bots run in **Log** mode, Attack Mode is off, and there are no custom
   rules and no IP blocks, so the three custom rules and the IP blocks of the Hobby plan stay free for an incident. Log mode only
   records: visitors see no challenge and get no cookie. With the pinned CLI (`pnpm install --frozen-lockfile --ignore-scripts` in
   `website/`, then `node_modules/.bin/vercel`):

   ```sh
   vercel firewall rules edit bot-protection --action log --yes --project myriarch --scope <team>
   vercel firewall rules edit ai-bots --action log --yes --project myriarch --scope <team>
   vercel firewall publish --yes --project myriarch --scope <team>
   vercel firewall status --project myriarch --scope <team>
   ```

   Challenge and Deny stay off on purpose. A challenge runs a script and sets a session cookie, which the privacy rules exclude, and
   it breaks the link previews of chat services and the `curl` checks below. Deny for AI bots would also stop assistants from reading
   the documentation that people ask them about, while the same files stay readable on GitHub. `robots.txt` stays open for every
   crawler.

Residual risk: the token is team-scoped, and `website-preview` holds the same token as `website-production`. Every workflow run on a
branch of this repository (that is, everyone with write access) can use the preview environment and so the token; the branch policy of
`website-production` protects only the production secrets, not the token itself. Fork pull requests never receive it. Required
reviewers for production, or a separate Vercel team for previews, are the next step if the number of writers grows. The security
model lists the control ([Security model](../docs/security/model.md#build-and-release)).

### Acceptance after the first deployments

- Preview (needs a Vercel login): `/` and `/docs/` return 200, `/docs` redirects to `/docs/` (the generated `308` route; check this
  first, a link without the slash must not end in the 404 page), `/no-such-page/` returns 404 with the
  body of `404.html`, `robots.txt` says `Disallow: /`, the pages carry `noindex`, and no request goes to `vercel.live`.
- Production (`curl -sI https://myriarch.dev/`): `strict-transport-security` is present, `www` redirects with 308 to the apex,
  `robots.txt` names `https://myriarch.dev/sitemap.xml`, `/sitemap.xml` returns 200, a Mermaid `.mjs` file is served as
  `text/javascript`, and `site.webmanifest` has a JSON or manifest media type.
- If `vercel deploy --prebuilt` refuses to run without a linked project, add `vercel pull --yes --environment=<preview|production>`
  before the deploy step (it writes the git-ignored `.vercel/project.json`).

### Firewall incidents

The commands take the same `--project myriarch --scope <team>` as in the setup; a rule change is a draft until
`vercel firewall publish --yes`.

- **Find the cause first:** `vercel firewall overview` (the last day: traffic by action, busiest rules, alerts) and
  `vercel firewall traffic list`, then `vercel firewall traffic inspect ip <address>` for one client.
- **One abusive client or network:** `vercel firewall ip-blocks block <address or CIDR> --yes`, or one custom rule on its user agent
  or network (ASN), added with `--action log` first and switched to `deny` once the log shows that it matches only that client, for
  example `vercel firewall rules add "Block <client>" --condition '{"type":"user_agent","op":"sub","value":"<name>"}' --action log --yes`.
- **Too many requests:** the one rate-limit rule of the Hobby plan, keyed on IP and JA4, also in log mode first.
- **An attack:** `vercel firewall attack-mode enable --duration 1h --yes` challenges every request for an hour. It sets a short-lived
  security cookie and blocks link previews and scripts, so switch it off with `vercel firewall attack-mode disable --yes` as soon as
  the attack is over.
- Revert every incident measure when the incident is over, and keep the setup of step 6 as the normal state.

### Follow-ups

- Headers that a meta tag cannot carry: `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff` and `Referrer-Policy`. They
  need host configuration and will be added in a host-neutral way, for example as `headers` in the generated `config.json`.
- HSTS: Vercel sends `Strict-Transport-Security: max-age=63072000` on custom domains by default. `includeSubDomains` and `preload`
  would need a header of the project.
- Immutable cache headers for the fingerprinted assets, and versioned documentation.
