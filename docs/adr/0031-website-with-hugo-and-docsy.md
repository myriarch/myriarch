# 0031. Project website myriarch.dev with Hugo and Docsy, built from the repository and served as static files

- Status: Accepted
- Date: 2026-10-09
- Related: [ADR-0011](0011-no-containers-for-local-development-and-tests.md), [ADR-0016](0016-trademark-usage-and-name-protection.md), [ADR-0018](0018-license-and-copyright.md), [ADR-0020](0020-english-as-project-language.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0030](0030-reuse-compliance-and-third-party-notices.md)

## Context

The project needs a public face at myriarch.dev: a landing page that explains what myriarch is, and the documentation. The documentation already exists. It lives in `docs/` as Markdown with Hugo front matter (`title`, `weight`, `description`, `_index.md` section files), the decision records follow the MADR layout, and the generated references (command line, GitOps schema) are already checked for drift by the gates. A second copy of this text on a website would drift; a website that cannot show the repository documentation would be a second documentation project.

The criteria for the generator and the theme:

- **Search** in the browser, without a search service.
- **Versioned documentation later** (a version menu), not now.
- **Dark mode** that follows the system and can be switched, with the brand colors (`assets/brand`).
- **Maintenance effort**: few moving parts, pinned versions, a build that fails loudly.
- **Build without a container runtime** ([ADR-0011](0011-no-containers-for-local-development-and-tests.md)): one downloadable binary plus a few pinned packages, on a developer machine and in CI.
- **Privacy**: no trackers, no analytics, no requests to third parties when a page loads (no CDN scripts, no web fonts from a provider, no embedded frames).
- **License and REUSE compliance** ([ADR-0018](0018-license-and-copyright.md), [ADR-0030](0030-reuse-compliance-and-third-party-notices.md)): every file licensed, every third-party component listed.
- **A host that can be exchanged**: the first host is Vercel, but the site must move to Netlify or GitHub Pages without changes.
- **Conventions of the field**: the OpenTelemetry and many CNCF projects use Hugo with the Docsy theme, so contributors know it.

The website is developed now and published only after the first public push of the repository. Nothing is deployed by this decision.

## Decision

1. **Hugo (extended) with Docsy as a Hugo module.** The versions are pinned: Hugo, Dart Sass (Docsy compiles its styles with it) and lychee in `ci/versions.env` with SHA-256 values of the release downloads (`ci/install-tools.sh hugo sass lychee`), the theme in `website/go.mod` and `website/go.sum` (checked by `hugo mod verify` and the Go checksum database), the npm packages that Docsy does not install for module consumers (Bootstrap, Font Awesome Free) and the other shipped assets (lunr, Mermaid) in `website/package.json` and `website/pnpm-lock.yaml`.
2. **The sources live under `website/`. `docs/`, the decision records and selected root files are mounted, never copied.** Module mounts put `docs/` at `/docs/`; content adapters render the decision records (the H1 line is the title, the status line a parameter) and the root files (`ROADMAP.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, `GOVERNANCE.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `MAINTAINERS.md`, `ADOPTERS.md`, `CONTRIBUTOR_LADDER.md`) below `/project/`. `api/openapi.yaml` is mounted as an asset and offered for download, and the REST API reference (`docs/reference/api/`) is generated from it by `make docs-gen` (a Go generator, `internal/apidoc`) and checked for drift, so the reference always shows the committed contract. The URLs mirror the repository paths, so "edit this page" links and link resolution stay one to one. Content that exists only for the website (landing page, legal pages, placeholders for pages that a later change will write) lives in `website/content`.
3. **A link render hook maps the links of the repository files.** The Markdown files are written for GitHub (`../tasks/_index.md`, `gitops-schema.md#fields`). The hook resolves a relative link against the repository path of the page, maps it to the page that renders the target (including section index files and the files a page represents), sends other repository files and directories to the GitHub URL of the repository, and fails the build for an unknown site-absolute link or a link that leaves the repository. An image hook resolves only local resources: a GitHub raw URL would be a third-party request. Heading anchors follow the GitHub slugs, so existing `#fragment` links work.
4. **Static output only.** The result is a directory of files (`website/public`) with `index.html` per page, `/404.html`, `/sitemap.xml` and `/robots.txt`; no functions, no host-specific configuration. Vercel is the first host, but nothing depends on it. The site is built by the project's own pinned tooling and deployed as a directory of files; the host never runs repository code (its built-in Hugo image has no Dart Sass, takes the Hugo version from an environment variable and would run the build on the host). The environment `preview` marks pages and `robots.txt` `noindex` for review deployments.
5. **Privacy by construction.** Everything is self-hosted: scripts, styles, the icon font, the search index and Mermaid are files of the site, and the system font stack replaces web fonts. The build blocks every fetch of a remote resource. Production pages carry a Content Security Policy as a meta tag that allows only the site itself. The theme parts that load from a CDN (the lunr script, the Mermaid import, the favicon lookup, the "edit this page" paths) are replaced by overrides of six Docsy files (five partials and the 404 page), pinned by SHA-256 in `website/overrides.txt` so that a Docsy update shows what changed. There are no cookies and no analytics; the only stored value is the visitor's light or dark choice in `localStorage`, which is a browser preference and is disclosed on the privacy page. If statistics are ever wanted, only a cookieless solution that does not identify visitors qualifies, and only after a new decision record.
6. **Search is Docsy's offline search** (lunr, in the browser, from an index file of the site).
7. **The gate `website`** of `make check-local` (`make website-check`) builds with `--panicOnWarning` and runs: `hugo mod verify`; lychee over the built pages (offline, with fragments); markdownlint over the Markdown sources; the front matter rules of the site content; a scan of the built pages for resources from other hosts and for local file system paths; the check of the override pins; the check that placeholders disappear when the real page exists; the check that every shipped dependency has a notice; and the unit tests of these checks. The existing drift checks (generated CLI and REST API references, GitOps schema, `web/src/api/schema.d.ts`) stay where they are, and `check-api-spec.py` compares the built API pages with the contract. A browser check against the production build (`make website-preview`) confirms that a page load makes only same-origin requests; Lighthouse scores are an acceptance check, not a gate.
8. **Versioned documentation is deferred** to a later change. The theme has a version menu; the content layout does not need to change for it.

## Consequences

### Positive

- One source for GitHub and the website. Mounted content cannot drift, and the link and anchor checks of both worlds run on the same files.
- Familiar to contributors from OpenTelemetry and other CNCF projects; Docsy brings navigation, search, dark mode and the version menu.
- Fast builds (seconds), no Node runtime at serve time, a static directory that any host can serve.
- The privacy properties are enforced by checks (static scan of the output, runtime request check), not by review alone.

### Negative

- Two more pinned binaries (Hugo, Dart Sass) plus lychee, and a Node dependency tree for the website assets (Bootstrap, Font Awesome, lunr, Mermaid) next to the one of the web UI.
- Six overrides of theme files have to be re-checked on every Docsy update (guarded by `website/overrides.txt`). Docsy makes breaking changes between minor versions, for example the module path change in 0.18 from `github.com/google/docsy` to `github.com/docsy/docsy/theme`.
- A first build needs network access (Go proxy, npm registry, release downloads); later builds use the caches.
- The REST API reference comes from a generator that renders a deliberately small subset of OpenAPI (no `allOf`, `oneOf`, `anyOf`, type arrays, inline nested objects or external references). `internal/apidoc` refuses everything else and names the JSON pointer, so a contract change that leaves the subset extends the generator explicitly. The reference has no request and response samples from a viewer; the generator derives one example per request body from the examples and defaults of the contract.
- A visitor cannot try an operation from the page. That is intended: the reference describes the API and sends no request to it.

## Alternatives considered

- **Astro Starlight**: Pagefind search and a good developer experience, but content must live in `src/content` or be loaded by a custom loader, documents outside the project need symlinks or a loader, the build is a Node build, and it is less common in CNCF projects. It stays the alternative if Docsy becomes a burden.
- **MkDocs Material**: a Python toolchain, navigation in `mkdocs.yml`, and front matter conventions that differ from the files that already exist.
- **Docusaurus**: its MDX parsing breaks plain Markdown that contains `<` and `{`, and the React build is heavy for static documentation.
- **Hugo with Hextra or Book**: smaller ecosystems and no version menu.
- **Plain GitHub rendering**: no landing page, no search, no control over privacy or presentation.
- **API renderers in the browser**: Redoc, Scalar and Swagger UI. All three render the whole contract on one page from a script of 1 MB or more: the page has one very long scroll (about 30,000 DOM nodes for this contract) with a second navigation next to the one of the site, the dark mode needs a hand-made theme that has to be switched when the appearance changes (Redoc has none of its own), the operations are not in the search of the site, and the examples are invented by the viewer instead of taken from the contract. Redoc also requests a logo from a vendor host at startup, which a guard that patches DOM prototypes had to stop; Scalar can send requests through a proxy of the vendor and loads fonts from a vendor host unless five settings switch that off; the "try it out" feature of Swagger UI sends requests from the page. A viewer was used first (Redoc, self-hosted) and replaced by the generated reference once the measurements showed these costs.
- **A Hugo content adapter instead of committed pages**: it would build the pages from the mounted contract at build time and needs no generated files in the repository, but logic in Go templates is hard to unit-test, the pages would not exist on GitHub, and errors are less precise than in a tested Go package. The generated files follow the pattern of the command line reference.
- **Hosting with Vercel's own Hugo build image**: no Dart Sass, the Hugo version comes from an environment variable, and it would run repository code on the host. The project builds the site itself and deploys the result.

## Notes

- 2026-10-10: Decision 4 names an environment `preview` for review deployments. It is implemented differently: a preview is built with the production Hugo environment, so that it ships the same bytes as production, and only the site parameter `params.preview` switches `noindex` and `robots.txt`. The Hugo environment `preview` does not exist. The deployment is described in `website/README.md` (Previews, Deployment).
- 2026-10-13: A seventh override, `website/assets/js/offline-search.js`, changes when the search index loads (on the first use of a search box, not on every page load, for Total Blocking Time), not where it comes from. It is pinned in `website/overrides.txt` and gets the Docsy attribution in `REUSE.toml`, like the six of decision 5. The pin check of decision 7 (`check-overrides.py`) covers `website/layouts` and `website/assets`; the three project style files that the theme ships as empty placeholders are exempt. The six overrides named in the consequences are now seven.
