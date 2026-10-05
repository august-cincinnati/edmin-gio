package main

import (
	"image"
	"image/color"
	"math"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"golang.org/x/image/math/fixed"
)

// monoMetrics is the character cell of a monospace font, in pixels. As with
// GTK's hinted text, the advance and the line's ascent and descent are
// rounded to whole logical pixels, so text sits on a regular grid.
type monoMetrics struct {
	cw     float32 // advance of one character
	lineH  int
	ascent int
}

type metricsKey struct {
	f     font.Font
	size  unit.Sp
	scale float32
}

var metricsCache = map[metricsKey]monoMetrics{}

func monoMetricsFor(gtx layout.Context, f font.Font, size unit.Sp) monoMetrics {
	k := metricsKey{f, size, gtx.Metric.PxPerDp}
	if m, ok := metricsCache[k]; ok {
		return m
	}
	shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(gtx.Sp(size)), MaxWidth: 1 << 24}, "M")
	var g text.Glyph
	for {
		x, ok := shaper.NextGlyph()
		if !ok {
			break
		}
		g = x
	}
	s := gtx.Metric.PxPerDp
	px := func(v fixed.Int26_6) float32 { return float32(v) / 64 }
	round := func(v float32) float32 { return float32(math.Round(float64(v/s))) * s }
	ceil := func(v float32) float32 { return float32(math.Ceil(float64(v/s-0.01))) * s }
	asc, desc := ceil(px(g.Ascent)), ceil(px(g.Descent))
	m := monoMetrics{cw: max(round(px(g.Advance)), 1), lineH: int(asc + desc), ascent: int(asc)}
	if m.lineH <= 0 {
		m.lineH = gtx.Sp(size)
	}
	metricsCache[k] = m
	return m
}

// drawMono draws s with its characters on the cell grid m, its top-left
// corner at (x, y).
func drawMono(gtx layout.Context, s string, f font.Font, size unit.Sp, col color.NRGBA, x float32, y int, m monoMetrics) {
	if s == "" {
		return
	}
	shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(gtx.Sp(size)), MaxWidth: 1 << 24, DisableSpaceTrim: true}, s)
	var gs []text.Glyph
	runes := 0
	clusterStart := -1
	var clusterX fixed.Int26_6
	for {
		g, ok := shaper.NextGlyph()
		if !ok {
			break
		}
		if clusterStart < 0 {
			clusterStart = len(gs)
			clusterX = g.X
		}
		// Keep the glyph's offset within its cluster; move the cluster to its cell.
		g.X = fixed.Int26_6(float32(runes)*m.cw*64) + (g.X - clusterX)
		g.Y = 0
		gs = append(gs, g)
		if g.Flags&text.FlagClusterBreak != 0 {
			runes += int(g.Runes)
			clusterStart = -1
		}
	}
	if len(gs) == 0 {
		return
	}
	// Whole pixels keep the glyphs crisp.
	defer op.Offset(image.Pt(int(math.Round(float64(x))), y+m.ascent)).Push(gtx.Ops).Pop()
	path := shaper.Shape(gs)
	outline := clip.Outline{Path: path}.Op().Push(gtx.Ops)
	paint.ColorOp{Color: col}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	outline.Pop()
	shaper.Bitmaps(gs).Add(gtx.Ops)
}
