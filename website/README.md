# myriarch website

The sources of the project website (myriarch.dev): Hugo extended with the Docsy theme. The site is built from the files in
this directory and from the Markdown files of the repository that it mounts (`docs/`, the decision records, selected root
files). It is a purely static site. The decision is [ADR-0031](../docs/adr/0031-website-with-hugo-and-docsy.md); the
development guide has the contributor view ([Website](../docs/development.md#website)).

The website is developed now and published after the first public push. Nothing in this directory deploys anything.

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
(default 1313; 8080 and the ports of the end-to-end tests are refused), `WEBSITE_BASEURL` (a preview on another host name),
`WEBSITE_ENV=preview` (`noindex`, `Disallow: /`), `TOOLS_BIN` (the tool directory; a git worktree points it at the main working
copy). Requirements: Go (Hugo fetches the Docsy theme as a Go module), Node.js, pnpm and Python 3; `make website-check` needs
Node.js 22 or newer because of markdownlint-cli2 (`engines` in `package.json`; the script says so before it fails).

## Layout

```text
go.mod, go.sum              the pin of the Docsy module (github.com/docsy/docsy/theme)
package.json, pnpm-lock.yaml  Bootstrap, Font Awesome Free, lunr, Mermaid (shipped) and markdownlint-cli2; exact versions
config/_default/            hugo.yaml (site), module.yaml (mounts), params.yaml, menus.yaml
config/preview/hugo.yaml    the environment "preview"
content/                    pages that exist only on the website, content adapters (_content.gotmpl)
data/repo_pages.yaml        repository files that adapters render (the root files)
data/third_party.yaml       the notices of /legal/third-party/
layouts/                    render hooks (_markup), partials (repo/*, csp.html), shortcodes, overrides of Docsy files, 404.html, robots.txt
assets/scss/                brand tokens (_variables_project.scss) and project styles
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
  (`docs/operations/install-helm.md` is `content/docs/operations/install-helm.md`) with

  ```yaml
  params:
    placeholder:
      tracking: "operations-manual"
      replaces: "docs/operations/install-helm.md"
  ```

  and the `placeholder` shortcode as its body. The tracking value is never rendered; it is a lowercase topic slug (`operations-manual`) or a GitHub issue reference, which must be quoted (`"#123"`; an unquoted `#` starts a YAML comment). An optional `planned: "v0.2.0"` under `placeholder` makes the notice say
  that the page is planned for that release. When `docs/operations/install-helm.md`
  exists, the placeholder has to go in the same change (`scripts/check-placeholders.py`).

## Overrides of Docsy files

Files in `layouts/` with the same path as a file of the theme replace it. They are few and each starts with a comment that names the
upstream file: `head.html` (self-hosted lunr, brand colors, the CSP first), `favicons.html`, `page-meta-links.html` (edit links from
the repository path), `scripts/plugins/mermaid.html` (self-hosted Mermaid), `footer/copyright.html` and `404.html`.
`overrides.txt` pins the SHA-256 of the upstream file of each. `REUSE.toml` (annotation n) adds "Copyright The Docsy Authors" (Apache-2.0)
to the project header of each of these files, so that the derivation is machine-readable; add a new override there as well (`scripts/check-overrides.py` fails when the two lists differ). To update Docsy:

1. Change the version in `go.mod`, run `cd website && hugo mod get github.com/docsy/docsy/theme@<version>` (updates `go.sum`) and
   `hugo mod verify`; the module path changed once already (0.18).
2. `python3 -I website/scripts/check-overrides.py` lists the upstream files that changed. Compare each with the override, port the
   change, then update the digest in `overrides.txt` (`shasum -a 256 <theme dir>/<path>`).
3. Update `Docsy` in `data/third_party.yaml`, the Mermaid version in `config/_default/params.yaml` if the theme moved it, and run
   `make website-check` and the browser check (`node website/scripts/check-requests.mjs`).

## Checks

`make website-check` runs, after the build: `hugo mod verify`, lychee (offline, with fragments), markdownlint, the front matter of
`content/`, every `scripts/check-*.py` over `public/` and the unit tests (`python3 -I -m unittest discover -s website/scripts -p
'test_*.py'`). The checks:

| Check | Fails on |
|---|---|
| `check-third-party.py` | a script, style, font, image, frame, worker or import from another host in the built pages, styles and scripts |
| `check-local-paths.py` | the repository root, `/Users/...` or `/home/...` in the built site |
| `check-repo-links.py` | a link into the GitHub repository (blob, tree, edit) whose path does not exist in the working tree |
| `check-overrides.py` | a changed upstream file, a missing or an unlisted override |
| `check-placeholders.py` | a placeholder whose page now exists, or a malformed placeholder |
| `check-api-spec.py` | a missing API reference page, a download that is not exactly one fingerprinted copy of `api/openapi.yaml`, a page that loads a script of an API viewer, or a number of operation sections that differs from the contract |
| `check-notices.py` | a shipped dependency without a notice, with another version in `data/third_party.yaml`, or without a published license text |

A new check is a file `scripts/check-<name>.py` that takes the built site as its argument; the runner finds it. Outside the gate:

- `node website/scripts/check-requests.mjs` (needs `make website-preview` running, Node.js 22 and Chrome; the pages are in
  `scripts/request-pages.txt`) loads every page in light and dark and fails on a request to another origin, a CSP violation or a
  console error, a search that finds nothing for an operation or a heading, a diagram that does not render, an API page without its tables, and a mark on the landing page that does not play its rollout animation, does not end on identity transforms or animates with reduced motion.
- `python3 -I website/scripts/lighthouse-scores.py --min 90 <report.json>` evaluates Lighthouse reports (see the development guide). The
  preview server (`scripts/preview-server.py`) compresses text files like a host does, so its scores match what visitors get.

## Privacy

No analytics, no cookies, no request to a third party. Fonts are the system font stack plus the self-hosted icon font. Production
pages carry this Content Security Policy as a meta tag (`layouts/_partials/csp.html`): `default-src 'self'`; scripts and styles
`'self' 'unsafe-inline'` (the theme has small inline scripts and styles); images and fonts `'self' data:`; `connect-src 'self'`;
workers `'self' blob:`; no frames, no objects. A page can widen a directive with `params.csp` (a map from directive to a list of
sources); the checks above still fail on a request to another host. `frame-ancestors`, HSTS and `X-Content-Type-Options` cannot be set
in a meta tag: they belong to the deployment (see below).

## Hosting

The output is `website/public`: plain files with an `index.html` per page, `/404.html`, `/sitemap.xml` and `/robots.txt`. There are
no functions and no host-specific configuration (no `vercel.json`, no rewrites), so the directory can be served by Vercel,
Netlify, GitHub Pages or any static server.

Recommended for Vercel: build in the project's own CI with the pinned tools (`make tools TOOLS="hugo sass lychee"` and `make
website-check`) and deploy the finished directory as prebuilt static output (`vercel deploy --prebuilt` or `vercel deploy
website/public`, with the token as a CI secret). Vercel then never executes repository code, and the same directory goes to
another host unchanged. Not recommended: the built-in Hugo preset of Vercel. It has no Dart Sass, takes the Hugo version from an
environment variable and runs the build on the host.

Previews per pull request: protected deployments built with `WEBSITE_ENV=preview` and `WEBSITE_BASEURL=https://$VERCEL_URL/`
(`noindex`, `Disallow: /`).

Follow-ups for the deployment (publication, versioned documentation): security headers that a meta tag cannot carry (HSTS, `frame-ancestors 'none'`,
`X-Content-Type-Options: nosniff`, `Referrer-Policy`), added in a host-neutral way; versioned documentation.
