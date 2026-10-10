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
| `check-indexing.py` | a preview that can be indexed (no `Disallow: /`, a sitemap line, a page without `noindex`) or a production site that hides itself; the mode comes from `WEBSITE_ENV` |
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
verifies the mode on the built site.

## Deployment

[`.github/workflows/website.yml`](../.github/workflows/website.yml) runs on pushes to `main` and on pull requests that change the
website or one of its inputs (`website/`, `docs/`, `api/openapi.yaml`, `assets/brand/`, the root Markdown files that the site
renders, the two mounted README files, the build scripts and the workflow itself), and on demand (`workflow_dispatch`, from
`main` only). A change elsewhere is deployed with the next website change, or by a manual run.

| Job | What it does | Secrets |
|---|---|---|
| `build` | installs the pinned tools, runs `make website-check` (with `WEBSITE_ENV=preview` and `WEBSITE_BASEURL=/` for a pull request) and uploads `website/public` | none |
| `deploy` | installs the pinned Vercel CLI from `pnpm-lock.yaml` without lifecycle scripts, assembles `.vercel/output` from the uploaded site and runs `vercel deploy --prebuilt` | the environments `website-preview` and `website-production` |

- **Production** on every push to `main` (and on a manual run from `main`): `vercel deploy --prebuilt --prod`.
- **Previews** for pull requests from branches of this repository, never for forks. The `deploy` job does not run for a fork, and
  not for a Dependabot pull request either (it has no Actions secrets); both get the build and the gate only. Previews are
  protected by Vercel Authentication, built with `WEBSITE_ENV=preview` and marked `noindex`.
- **Concurrency:** a newer push to a pull request cancels the running preview. A run on a branch is never cancelled; a newer pending
  run of the same ref replaces an older pending one, so the newest commit of `main` wins and a manual run from another branch cannot
  replace a pending production run.
- **Output:** the job summary lists the deployment URL, the commit and, for production, the site address. The workflow writes no pull
  request comment and needs no write permission (`contents: read`).
- **Guards:** the deploy job fails when `index.html`, `404.html` or `robots.txt` is missing, when a preview lacks `Disallow: /`, when
  production has it or lacks the absolute sitemap line, when a secret is empty, and when the CLI does not print a
  `https://<name>.vercel.app` URL.
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

### Follow-ups

- Headers that a meta tag cannot carry: `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff` and `Referrer-Policy`. They
  need host configuration and will be added in a host-neutral way, for example as `headers` in the generated `config.json`.
- HSTS: Vercel sends `Strict-Transport-Security: max-age=63072000` on custom domains by default. `includeSubDomains` and `preload`
  would need a header of the project.
- Immutable cache headers for the fingerprinted assets, and versioned documentation.
