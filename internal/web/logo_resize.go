// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package web

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // decoders for uploaded logos
	_ "image/jpeg"
	"image/png"
)

// A stored raster logo fits in this box (about three times the size it is
// shown at, so it stays sharp on high-density screens).
const (
	logoMaxW = 600
	logoMaxH = 150
)

// fitLogo shrinks a PNG, JPEG or GIF logo that is larger than the logo box,
// keeping its proportions, and returns it as PNG. Smaller images, SVG and
// WebP (no decoder in the standard library) are returned unchanged.
func fitLogo(data []byte, ct string) ([]byte, string, error) {
	switch ct {
	case "image/png", "image/jpeg", "image/gif":
	default:
		return data, ct, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("the logo image could not be read")
	}
	if cfg.Width > 8000 || cfg.Height > 8000 {
		return nil, "", errors.New("the logo is too large: at most 8000 × 8000 pixels")
	}
	w, h := fitBox(cfg.Width, cfg.Height, logoMaxW, logoMaxH)
	if w == cfg.Width && h == cfg.Height {
		return data, ct, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("the logo image could not be read")
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, shrink(src, w, h)); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/png", nil
}

// fitBox scales w×h down (never up) to fit in maxW×maxH.
func fitBox(w, h, maxW, maxH int) (int, int) {
	if w <= maxW && h <= maxH || w <= 0 || h <= 0 {
		return w, h
	}
	nw, nh := maxW, h*maxW/w
	if nh > maxH {
		nw, nh = w*maxH/h, maxH
	}
	return max(nw, 1), max(nh, 1)
}

// shrink downscales src to w×h by averaging the source pixels that fall in
// each target pixel (box filter, with premultiplied alpha), which keeps thin
// lines and text of a logo readable.
func shrink(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0 := b.Min.Y + y*sh/h
		y1 := max(b.Min.Y+(y+1)*sh/h, y0+1)
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*sw/w
			x1 := max(b.Min.X+(x+1)*sw/w, x0+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA() // premultiplied, 16 bit
					r, g, bl, a = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca)
					n++
				}
			}
			if a == 0 {
				continue // fully transparent
			}
			// Un-premultiply: colour = sum(c) / sum(alpha).
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r * 0xff / a), G: uint8(g * 0xff / a), B: uint8(bl * 0xff / a),
				A: uint8(a / n >> 8),
			})
		}
	}
	return dst
}
