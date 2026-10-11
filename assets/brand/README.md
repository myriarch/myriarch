# myriarch brand assets

## The mark

*myriarch* comes from the Ancient Greek *myriarchos*, "commander of ten thousand". The mark is a tetractys,
the Pythagorean triangle of 1 + 2 + 3 + 4 = 10 points: ten points in four rows read as 10⁴ = 10,000, a myriad.
The apex is an open ring. It stands for the control plane, which leads the fleet and runs no workload itself.
The three rows below it are the fleet as a rollout grows: a canary first, then waves that widen until they
reach every collector. The colour is Tyrian purple, the costly dye that marked rulers in antiquity.

## Files

| File | Use it for |
|---|---|
| `myriarch-horizontal.svg` | The default logo on light backgrounds: README header, website header, documentation, slides. |
| `myriarch-horizontal-dark.svg` | The default logo on dark backgrounds (designed against `#0f172a`). |
| `myriarch-stacked.svg`, `myriarch-stacked-dark.svg` | Square or narrow spaces: title slides, splash screens, stickers, posters. |
| `myriarch-icon.svg`, `myriarch-icon-dark.svg` | The mark alone, for places where the name is already visible or there is no room for it: app and UI icons, avatars, bullets in diagrams. |
| `myriarch-icon-parts.svg` | The mark as ten separate shapes (one ring, nine dots) in `currentColor`, for animation (see [Motion](#motion)). Not for static use: use `myriarch-icon.svg` there. |
| `*-black.svg`, `*-white.svg` | One-colour reproduction: print without colour, embossing, engraving, single-colour merchandise, or photos and coloured backgrounds where neither palette variant has enough contrast. |
| `favicon.svg` | Browser favicon. Drawn on a 16 px grid; switches from Tyrian to Orchid under `prefers-color-scheme: dark`. |
| `favicon.ico` | Favicon fallback with pixel-tuned 16, 32 and 48 px bitmaps (each drawn on its own pixel grid, not downscaled). |
| `favicon-16.png`, `favicon-32.png` | PNG favicon fallbacks for tools that do not read SVG or ICO. |
| `apple-touch-icon.png` | iOS and iPadOS home screen icon (180 × 180, white background, mark with padding). |
| `icon-192.png`, `icon-512.png` | Web app manifest icons, `"purpose": "any"` (transparent background). They are not maskable icons. |
| `social-preview.png` | GitHub repository social preview (1280 × 640): *Settings → General → Social preview*. GitHub needs the PNG. |
| `og-image.png` | Open Graph and X/Twitter card image for the website (1200 × 630). |
| `social-preview.svg`, `og-image.svg` | Vector sources of the two social images. |
| `source/` | The generator that produces every file above (see [Production](#production)). |

All SVGs are plain filled paths: no `<text>`, no embedded raster images, no scripts and no external references.
Each has a tight `viewBox` and explicit `width` and `height`, plus `role="img"`, `aria-label` and `<title>`.

### Embedding

Website favicons:

```html
<link rel="icon" href="/favicon.ico" sizes="32x32">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<link rel="manifest" href="/site.webmanifest">
```

```json
"icons": [
  { "src": "/icon-192.png", "type": "image/png", "sizes": "192x192" },
  { "src": "/icon-512.png", "type": "image/png", "sizes": "512x512" }
]
```

`favicon.svg` switches colour with a `<style>` media query. If a Content Security Policy blocks inline styles
in the served SVG, the favicon stays Tyrian, which still reaches 3.19:1 on `#0f172a`.

Light and dark logos in a Markdown README (adjust the paths):

```html
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/myriarch-horizontal-dark.svg">
  <img alt="myriarch" src="brand/myriarch-horizontal.svg" width="320">
</picture>
```

## Colours

| Name | Hex | Role | WCAG contrast |
|---|---|---|---|
| Tyrian | `#B3338A` | The mark on light backgrounds | 5.59:1 on `#FFFFFF` (AA for text, passes 3:1 for graphics) |
| Orchid | `#F28CCB` | The mark on dark backgrounds | 7.98:1 on `#0f172a` (AAA) |
| Plum ink | `#2B0A1F` | The wordmark on light backgrounds | 18.01:1 on `#FFFFFF` (AAA) |
| Mist | `#F1F5F9` | The wordmark on dark backgrounds | 16.30:1 on `#0f172a` (AAA) |
| Night | `#0f172a` | Reference dark background | – |
| Blush | `#FBF6F9` | Background of the social images | Tyrian 5.23:1, Plum ink 16.86:1 |
| Mauve | `#5C4656` | Tagline in the social images | 7.97:1 on `#FBF6F9` (AAA) |

Limits: Tyrian reaches only 3.19:1 on `#0f172a` (enough for graphics, not for text), so use the `-dark` files on dark
backgrounds. Orchid reaches only 2.24:1 on white: never use it on light backgrounds.

## Clear space

The unit **u** is the outer diameter of the open ring, which is one quarter of the mark's height. Keep at least
**1u** free on every side of every logo: no text, other logos or edges inside it. The lockups use the same unit:
the gap between mark and wordmark is 1u in both the horizontal and the stacked version.

## Minimum sizes

| Asset | Minimum |
|---|---|
| Icon (`myriarch-icon*.svg`) | 24 px tall. Below that the open ring closes up. |
| Icon at 16 to 23 px | Use `favicon.svg`, `favicon.ico` or the favicon PNGs, which are redrawn on whole pixels for 16, 32 and 48 px. |
| Horizontal lockup | 120 px wide |
| Stacked lockup | 80 px wide |

Below the lockup minimums, use the icon on its own.

## Construction

Design units (the SVG files use twice these values):

- **Lattice:** dots Ø 12 on a pitch of 15 (gap 3) with a row step of 13 (15 · √3 / 2 = 12.99), so every dot sits
  on an equilateral lattice and every gap is 3.
- **Open ring:** outer Ø 13 (1u), inner Ø 5.5. An open ring with the same diameter as a dot looks lighter and
  smaller, so the ring is 8 % larger than a dot and carries 96 % of a dot's ink. Its centre sits 13.5 above the
  2-dot row, which keeps its gap to the two dots below at 2.94, optically equal to the dot gap.
- **Mark:** 57 × 52, which is 4u tall.
- **Horizontal lockup:** the wordmark *myriarch* is set in Inter SemiBold with −0.02 em tracking and the font's
  own kerning. The round letters span exactly the two lower rows: their tops meet the tops of the 3-dot row and
  their bottoms meet the bottoms of the 4-dot row. The wordmark therefore shares its baseline with the bottom row,
  and the dots overshoot it just as round letters do. The ascender of the *h* ends at the centre line of the
  2-dot row. The gap between the mark and the stem of the *m* is 1u.
- **Stacked lockup:** the wordmark is exactly twice as wide as the mark and its cap line sits 1u below the mark.
- **Favicons:** `favicon.svg` and the 16 px bitmap use 3 px dots, 1 px gaps, a 4 px row step and a 3 px ring
  with a fully open centre pixel. The 32 px bitmap uses 6/2/7 px and the 48 px bitmap 10/2/11 px. Every edge
  falls on a whole pixel.

## Do's and don'ts

### Do

- Use the supplied files and scale them proportionally.
- Use Tyrian with Plum ink on light backgrounds, and Orchid with Mist on dark backgrounds.
- Use the black or white version when colour is not available or neither palette version has enough contrast.
- Keep the clear space and the minimum sizes.
- Animate the mark only in ways that end on the unchanged mark, for example rows appearing one after another
  as a loading or rollout indicator. The rules and the reference animation are in [Motion](#motion).

### Don't

- Recolour the logo outside the palette, use gradients, or give the mark two colours.
- Add effects: shadows, glows, bevels, 3D, blur or transparency.
- Rotate or mirror the logo. The open ring always points up.
- Stretch, squash or otherwise distort the proportions.
- Draw an outline version, add strokes, or fill the open ring.
- Add, remove or rearrange dots.
- Retype the wordmark in live text or in another typeface, or change its spacing.
- Put Tyrian on dark backgrounds or Orchid on light ones (see the contrast limits above).
- Combine the logo with third-party marks such as OpenTelemetry, Kubernetes or CNCF into a joint logo.

## Motion

The mark may play one short animation, the rollout. It tells the story of the mark (see [The mark](#the-mark)): the open ring
comes up first, the control plane. Then the fleet connects: nine small seed dots appear, one per collector, because every
collector is there but none is on the new revision yet. Then the rollout updates the fleet row by row: the canary (2 dots) grows to
full size, then the first wave (3 dots), then the second wave (4 dots), which is the full fleet. The animation ends on the
unchanged mark. The website plays it on the mark of the landing page and repeats it (see [Looping](#looping)).

The animation needs the parts of the mark as separate shapes. `myriarch-icon-parts.svg` has them: one path `ring` and nine paths
`dot` with the classes `r1` to `r3` (the row, from the top) and `c0` to `c3` (the column, from the left). It is generated from the same
construction as every other file (`source/build.py`), and `source/validate.py` checks that its circles are those of
`myriarch-icon.svg`. The paths use `currentColor`, so the page decides the colour, and an inline SVG follows the light and dark
appearance of the page.

Reference timing, with the scale of the parts as the only thing that moves. The rows share one clock of 2.2 s; the dots of a row start
0, 60, 120 and 180 ms apart from left to right (columns `c0` to `c3`).

| Part | Starts at scale 0 | Seed (scale 0.3) | Full size (scale 1) |
|---|---|---|---|
| Ring (`ring`) | at 0 s | not used | at 0.45 s (one segment from 0 to 1) |
| Canary, row 1 (`r1`) | until 0.35 s (16 %) | from 0.55 s (25 %) to 0.75 s (34 %) | from 1.1 s (50 %) |
| First wave, row 2 (`r2`) | until 0.35 s | from 0.55 s to 1.3 s (59 %) | from 1.65 s (75 %) |
| Full fleet, row 3 (`r3`) | until 0.35 s | from 0.55 s to 1.85 s (84 %) | at 2.2 s (100 %) |

Every segment eases with `cubic-bezier(0.2, 0.7, 0.2, 1)`. The whole animation takes about 2.4 s.

Rules for every animation of the mark:

- Move only the size of the parts: `transform: scale(...)` around the centre of each part (`transform-box: fill-box`,
  `transform-origin: 50% 50%`). No opacity, no second colour, no stroke, no blur, no motion of position.
- No overshoot: a size above 1 would close the gaps of 3 units between the dots. The brand rules for the still mark (one colour, no
  transparency, no distortion) hold in every frame, not only in the last.
- The ring never rotates and always points up.
- Always end on the unchanged mark: every part at scale 1, in the colour of the static mark.
- Play the rollout once, or loop it only as described under [Looping](#looping). Do not replay it on hover or focus. The mark is
  decoration, except where it is the pause control of a loop (see [Looping](#looping)).
- Respect `prefers-reduced-motion`: the static mark is the base style, and the animation is added only inside
  `@media (prefers-reduced-motion: no-preference)`. With reduced motion, in print and where CSS animations are unsupported the unchanged
  mark shows.
- Show the animated mark at 96 px tall or more. Below that the seed dots are too small to read, and the static mark is used.

The web UI may reuse the same timeline later, for example as a loading indicator.

### Looping

The landing page keeps the mark alive by repeating the rollout. A loop is allowed under these conditions, which the website meets:

- **Calm timing.** One cycle takes 10 s: the rollout (about 2.4 s), then the unchanged mark held until 9.4 s (7 s after the last dot is
  full size), then the nine dots retract to scale 0 in the last 0.6 s (`ease-in-out`, all together), so that the next rollout starts from
  the state in which the first one started and nothing jumps. The ring grows once, on load, and stays in place afterwards. A cycle
  never ends on a half-grown mark: the unchanged mark is what the visitor sees for most of the time.
- **A way to pause it** (WCAG 2.2.2, Pause, Stop, Hide: motion that starts by itself and lasts longer than 5 s). The mark itself is the
  control, with no visible button: a click or tap on the mark pauses the loop and shows the unchanged mark, and another click resumes
  it. The script makes the mark a control only when it can animate (`role="button"`, `tabindex="0"`, the name "Pause logo animation",
  `aria-pressed` for the paused state, Enter and Space toggle it like a native button, a pointer cursor, and a focus ring for keyboard
  focus only, in the accent colour of the appearance). With `prefers-reduced-motion: reduce` nothing animates and the mark is a plain
  image: no role, not focusable.
- **Only while visible.** The loop stops (unchanged mark, no running animation) when the hero is out of view or the document is hidden,
  and starts again at the retraction that ends a cycle (the dots shrink from the full mark and the rollout follows at once, so nothing
  jumps). Nothing runs on a timer: the browser runs the animation, and a stopped mark has none.
- **Without JavaScript** the rollout plays once and the page keeps the unchanged mark (a plain image: no role, not focusable, since
  nothing needs to be paused).

The stylesheet holds the timeline (`website/assets/scss/_landing.scss`; the loop has one keyframes rule per dot because the stagger of
the columns has to repeat in every cycle) and `website/assets/js/landing-mark.js` decides when it runs.

## License

- **Artwork:** the logo artwork in this directory is licensed under
  [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Copyright The Myriarch Authors.
  Suggested attribution: "myriarch logo by The Myriarch Authors, CC BY 4.0".
- **Marks:** the name *myriarch* and the logo are project marks. CC BY 4.0 covers copyright only and grants no
  trademark rights (section 2(b)(2) of the license). Use the name and logo according to the project's trademark
  guidelines ([ADR-0016](../../docs/adr/0016-trademark-usage-and-name-protection.md)): to refer to the project, without
  suggesting endorsement or affiliation, and not as part of another product's name or logo.
- **Generator:** the scripts in `source/` are code and use the license of the rest of the repository, Apache-2.0
  ([ADR-0018](../../docs/adr/0018-license-and-copyright.md)).
- **Typeface:** the wordmark is set in Inter SemiBold and the tagline of the social images in Inter Regular
  (Inter 4.1, © The Inter Project Authors), both converted to outlines. Inter is licensed under the
  [SIL Open Font License 1.1](https://openfontlicense.org), which allows this use in artwork. No font files are
  distributed here.
- **Third-party marks:** OpenTelemetry is a trademark of The Linux Foundation. It appears in the tagline of the
  social images only to describe what myriarch works with. Myriarch is not affiliated with or endorsed by The
  Linux Foundation.

## Production

The files are generated from the construction above, not drawn by hand: outlines come from Inter via fontTools
and HarfBuzz, the SVGs are optimized with svgo (keeping `<title>`, `viewBox`, size and ARIA attributes), and the
PNGs are rendered with resvg. To change the logo, change the construction in `source/build.py` and
`source/brandlib.py` and regenerate. Do not edit the paths by hand.

```sh
assets/brand/source/make_assets.sh
```

The script needs Python 3.10 or newer (`python3`, or the interpreter that `BRAND_PYTHON` names, for example
`BRAND_PYTHON=python3.12`), Node.js with `npm`, `curl` and `unzip`. On its first run it sets up its tools in
`.cache/brand/`: a Python venv with the packages pinned in `source/requirements.txt` (recreated whenever the pins
change), the Node packages locked in `source/package-lock.json`, and the Inter 4.1 fonts from the release archive,
checked against a pinned SHA-256. With these versions a rerun reproduces the committed files byte for byte (checked
on macOS arm64), except `favicon.ico`: Pillow compresses its three PNG entries, and Pillow 12.3.0 writes one byte
more than the 11.3.0 that wrote the committed file. The pixels are identical, and `finish_rasters.py` checks every
entry against its source bitmap after writing. The script also writes `contact-sheet.png`, an overview of every
variant on white and on `#0f172a`, to `.cache/brand/work/` for review; it is not committed.

No gate runs the script. Dependabot proposes updates of the pinned packages (`source/requirements.txt`,
`source/package.json`), and such an update can change the output, so run the script into an empty directory
(`assets/brand/source/make_assets.sh <empty-dir>`) and compare the files with the committed ones before you merge it.
