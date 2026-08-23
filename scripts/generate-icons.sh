#!/usr/bin/env bash
#
# Generate the frontend's full favicon / app-icon set into frontend/public/.
#
# Run via `make frontend-icons`. Outputs are committed, so this only needs
# rerunning when the mark itself changes.
#
# Why a whole set rather than one favicon.svg: an SVG favicon covers modern
# browser tabs and nothing else. iOS Safari (home screen) and macOS Safari
# (Start Page favourites) read `apple-touch-icon` only, and ignore both the
# SVG and the .ico. Android reads the web app manifest. Legacy Windows and
# older browsers read /favicon.ico. Each target below exists for exactly one
# of those consumers -- see docs/favicons.md.
#
# Requires librsvg (rsvg-convert) and ImageMagick 7 (magick):
#     brew install librsvg imagemagick

set -euo pipefail

OUT="frontend/public"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

for bin in rsvg-convert magick; do
    command -v "$bin" >/dev/null || {
        echo "$bin not found; brew install librsvg imagemagick" >&2
        exit 1
    }
done

# The mark: lucide's cloud-rain, on lucide's 24-unit grid.
GLYPH='<path d="M4 14.899A7 7 0 1 1 15.71 8h1.79a4.5 4.5 0 0 1 2.5 8.242"/><path d="M16 14v6"/><path d="M8 14v6"/><path d="M12 16v6"/>'
# Stroke weight on lucide's grid. The heavier weight is an optical correction
# for the .ico: at 16px the nominal stroke closes up the gaps between the rain
# strokes and the mark turns to mush.
STROKE_W=2.25
STROKE_W_SMALL=2.5

# The drawn glyph is not centred on its own grid: with the stroke included it
# spans 22.26 units wide and 21.26 tall, centred on (12, 12.5) rather than
# (12, 12). Every placement below therefore centres on (12, 12.5), and sizes
# against the 22.26-unit long edge.
GLYPH_CX=12
GLYPH_CY=12.5
GLYPH_EXTENT=22.26

# Palette: the app's own neutral tokens (src/index.css), resolved out of oklch.
# oklch(1 0 0) -> #ffffff, oklch(0.145 0 0) -> #0a0a0a.
#
# White tile, black mark, in every scheme. Not a stylistic choice -- Safari
# composites tab and favourite icons onto its own light chip, so a dark tile
# reads as a dark square with a white surround bleeding around it. A white
# tile disappears into that chip, and stays legible on the dark tab strips
# (Chrome, Firefox) that composite onto nothing.
PAPER="#ffffff"
INK="#0a0a0a"

# tile <fraction> <corner-radius> <stroke-width> <bg> <fg>  -- a 100x100 icon
# tile SVG, glyph centred and scaled so its long edge is <fraction> of the
# tile. Radius is in tile units; 0 gives the full-bleed square that iOS and
# Android mask themselves. Stroke width is on lucide's grid, so it scales with
# the glyph.
tile() {
    local frac=$1 radius=$2 stroke_w=$3 bg=$4 fg=$5 scale
    scale=$(awk -v f="$frac" -v e="$GLYPH_EXTENT" 'BEGIN { printf "%.5f", f * 100 / e }')
    cat <<EOF
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="100" height="100">
  <rect width="100" height="100" rx="$radius" ry="$radius" fill="$bg"/>
  <g fill="none" stroke="$fg" stroke-width="$stroke_w" stroke-linecap="round" stroke-linejoin="round"
     transform="translate(50 50) scale($scale) translate(-$GLYPH_CX -$GLYPH_CY)">$GLYPH</g>
</svg>
EOF
}

# render <svg> <px> <png> [opaque]  -- rasterise, then normalise and strip
# metadata so a rerun that changes nothing leaves a clean git tree.
#
# Colour type is pinned rather than left to the encoder: both rsvg and
# ImageMagick spot that an all-neutral tile is greyscale and emit it as such.
# `opaque` additionally flattens the alpha channel, for the full-bleed tiles
# that must not be transparent -- it would wreck a rounded tile's corners.
render() {
    local svg=$1 px=$2 png=$3 mode=${4:-alpha}
    rsvg-convert -w "$px" -h "$px" "$svg" -o "$TMP/raw.png"
    if [ "$mode" = opaque ]; then
        magick "$TMP/raw.png" -background "$PAPER" -alpha remove -alpha off \
            -colorspace sRGB -define png:color-type=2 -strip "$png"
    else
        magick "$TMP/raw.png" \
            -colorspace sRGB -define png:color-type=6 -strip "$png"
    fi
}

mkdir -p "$OUT"

# ---------------------------------------------------------------------------
# favicon.svg -- browser tabs. Not generated: it is the source mark, checked in
# as lucide exports it, and left alone. Its `currentColor` resolves to black,
# which is what Safari's light icon chip wants.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# apple-touch-icon.png -- iOS home screen, macOS Safari Start Page favourites.
# ---------------------------------------------------------------------------
# 180x180 is the largest size iOS asks for and it downsamples the rest. Full
# bleed and opaque: iOS applies its own superellipse mask, and composites any
# transparency onto black. 60% keeps the mark clear of that mask.
tile 0.60 0 "$STROKE_W" "$PAPER" "$INK" > "$TMP/apple.svg"
render "$TMP/apple.svg" 180 "$OUT/apple-touch-icon.png" opaque

# ---------------------------------------------------------------------------
# icon-192.png, icon-512.png -- Android / Chrome, manifest purpose "any".
# ---------------------------------------------------------------------------
# Shown unmasked, so the tile rounds its own corners.
tile 0.62 18 "$STROKE_W" "$PAPER" "$INK" > "$TMP/rounded.svg"
render "$TMP/rounded.svg" 192 "$OUT/icon-192.png"
render "$TMP/rounded.svg" 512 "$OUT/icon-512.png"

# ---------------------------------------------------------------------------
# icon-maskable-512.png -- Android adaptive icons, manifest purpose "maskable".
# ---------------------------------------------------------------------------
# The launcher may crop to any shape inside a circle of 80% diameter, so the
# mark has to fit that circle: a square inscribed in it is 56% of the tile, and
# 52% leaves a little air.
tile 0.52 0 "$STROKE_W" "$PAPER" "$INK" > "$TMP/maskable.svg"
render "$TMP/maskable.svg" 512 "$OUT/icon-maskable-512.png" opaque

# ---------------------------------------------------------------------------
# favicon.ico -- /favicon.ico probes, older browsers, Windows pinned sites.
# ---------------------------------------------------------------------------
# Its own tile, not the Android one: an .ico is mostly consumed at 16px, and
# that size needs a much larger glyph (84% vs 62%) and a heavier stroke before
# the rain reads as rain at all. Square, because the tile is flattened opaque
# and a rounded corner would just be filled back in with the tile colour.
# Rasterise
# once at 256 and downsample -- that holds the shape together far better than
# rendering straight to 16px.
#
# It is an opaque white tile rather than a transparent one: an .ico cannot
# follow the platform theme, and a bare black glyph vanishes into a dark
# browser chrome.
tile 0.84 0 "$STROKE_W_SMALL" "$PAPER" "$INK" > "$TMP/ico.svg"
render "$TMP/ico.svg" 256 "$TMP/ico-256.png" opaque
magick "$TMP/ico-256.png" \
    -define icon:auto-resize=48,32,16 -strip "$OUT/favicon.ico"

echo "wrote:"
ls -1 "$OUT"
