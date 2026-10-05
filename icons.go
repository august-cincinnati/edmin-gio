package main

import (
	"encoding/xml"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// Icons come from the desktop's icon theme, as in GTK: symbolic icons are
// SVGs drawn in the text colour, and the explorer's file and folder icons
// are the theme's coloured PNGs.

var iconDirs = []string{"/usr/share/icons", "/usr/local/share/icons"}

type iconTheme struct {
	once  sync.Once
	dir   string
	files map[string][]string // icon name → files
}

var (
	themeMu    sync.Mutex
	iconThemes = map[string]*iconTheme{}
	themeOrder []string
)

func init() {
	themeOrder = []string{"Yaru", "Adwaita", "hicolor"}
	if out, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", "icon-theme").Output(); err == nil {
		if t := strings.Trim(strings.TrimSpace(string(out)), "'"); t != "" {
			themeOrder = append([]string{t}, themeOrder...)
		}
	}
}

func (t *iconTheme) index() {
	t.once.Do(func() {
		t.files = map[string][]string{}
		filepath.WalkDir(t.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			ext := filepath.Ext(name)
			if ext != ".svg" && ext != ".png" {
				return nil
			}
			base := strings.TrimSuffix(name, ext)
			t.files[base] = append(t.files[base], p)
			return nil
		})
	})
}

func themeNamed(name string) *iconTheme {
	themeMu.Lock()
	defer themeMu.Unlock()
	if t, ok := iconThemes[name]; ok {
		return t
	}
	var t *iconTheme
	for _, d := range iconDirs {
		if st, err := os.Stat(filepath.Join(d, name)); err == nil && st.IsDir() {
			t = &iconTheme{dir: filepath.Join(d, name)}
			break
		}
	}
	iconThemes[name] = t
	return t
}

// findIcon returns the theme files for name, from the first theme having it.
func findIcon(name string) []string {
	seen := map[string]bool{}
	for _, tn := range themeOrder {
		if seen[tn] {
			continue
		}
		seen[tn] = true
		if t := themeNamed(tn); t != nil {
			t.index()
			if fs := t.files[name]; len(fs) > 0 {
				return fs
			}
		}
	}
	return nil
}

// ---- Cache ----

type iconKey struct {
	name string
	px   int
	col  color.NRGBA
}

var (
	iconMu    sync.Mutex
	iconCache = map[iconKey]*image.NRGBA{}
)

// iconImage returns name rendered at px pixels, or nil if it isn't found.
// Symbolic icons take the colour col.
func iconImage(name string, px int, col color.NRGBA) *image.NRGBA {
	k := iconKey{name, px, col}
	if !strings.HasSuffix(name, "-symbolic") {
		k.col = color.NRGBA{}
	}
	iconMu.Lock()
	img, ok := iconCache[k]
	iconMu.Unlock()
	if ok {
		return img
	}
	files := findIcon(name)
	if strings.HasSuffix(name, "-symbolic") {
		for _, f := range files {
			if strings.HasSuffix(f, ".svg") {
				img = renderSymbolic(f, px, col)
				break
			}
		}
	} else {
		img = loadPNGIcon(files, px)
	}
	iconMu.Lock()
	iconCache[k] = img
	iconMu.Unlock()
	return img
}

// drawIcon draws icon name, size dp, at the current offset.
func drawIcon(gtx layout.Context, name string, size unit.Dp, col color.NRGBA) layout.Dimensions {
	px := gtx.Dp(size)
	img := iconImage(name, px, col)
	if img != nil {
		paint.NewImageOp(img).Add(gtx.Ops)
		// Images are drawn at one pixel per texel.
		defer op.Affine(scaleAffine(float32(px) / float32(img.Bounds().Dx()))).Push(gtx.Ops).Pop()
		paint.PaintOp{}.Add(gtx.Ops)
	}
	return layout.Dimensions{Size: image.Pt(px, px)}
}

// loadPNGIcon picks the smallest PNG at least px pixels wide, else the
// largest one, and scales it to px.
func loadPNGIcon(files []string, px int) *image.NRGBA {
	type cand struct {
		path string
		size int
	}
	var cs []cand
	for _, f := range files {
		if !strings.HasSuffix(f, ".png") {
			continue
		}
		// Directories look like 16x16, 16x16@2x or 16x16/places.
		size := 0
		for _, part := range strings.Split(f, string(filepath.Separator)) {
			w, rest, ok := strings.Cut(part, "x")
			n, err := strconv.Atoi(w)
			if !ok || err != nil {
				continue
			}
			if h, sc, ok := strings.Cut(rest, "@"); ok {
				if _, err := strconv.Atoi(h); err == nil {
					m, _ := strconv.Atoi(strings.TrimSuffix(sc, "x"))
					n *= max(m, 1)
				}
			}
			size = n
		}
		if size > 0 {
			cs = append(cs, cand{f, size})
		}
	}
	if len(cs) == 0 {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].size < cs[j].size })
	pick := cs[len(cs)-1]
	for _, c := range cs {
		if c.size >= px {
			pick = c
			break
		}
	}
	fh, err := os.Open(pick.path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	src, err := png.Decode(fh)
	if err != nil {
		return nil
	}
	return resample(src, px)
}

// resample scales src to a px×px image with a box filter.
func resample(src image.Image, px int) *image.NRGBA {
	b := src.Bounds()
	in := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	if b.Dx() == px && b.Dy() == px {
		return in
	}
	out := image.NewNRGBA(image.Rect(0, 0, px, px))
	sx, sy := float64(b.Dx())/float64(px), float64(b.Dy())/float64(px)
	for y := 0; y < px; y++ {
		for x := 0; x < px; x++ {
			x0, x1 := int(float64(x)*sx), max(int(float64(x+1)*sx), int(float64(x)*sx)+1)
			y0, y1 := int(float64(y)*sy), max(int(float64(y+1)*sy), int(float64(y)*sy)+1)
			var r, g, bb, a, n float64
			for yy := y0; yy < y1 && yy < b.Dy(); yy++ {
				for xx := x0; xx < x1 && xx < b.Dx(); xx++ {
					c := in.NRGBAAt(xx, yy)
					al := float64(c.A)
					r += float64(c.R) * al
					g += float64(c.G) * al
					bb += float64(c.B) * al
					a += al
					n++
				}
			}
			if a > 0 {
				out.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(bb / a), uint8(a / n)})
			}
		}
	}
	return out
}

// ---- Symbolic SVG rendering ----

type mat struct{ a, b, c, d, e, f float64 } // x' = a*x + c*y + e, y' = b*x + d*y + f

var identity = mat{1, 0, 0, 1, 0, 0}

func (m mat) mul(n mat) mat { // m applied after n
	return mat{
		m.a*n.a + m.c*n.b, m.b*n.a + m.d*n.b,
		m.a*n.c + m.c*n.d, m.b*n.c + m.d*n.d,
		m.a*n.e + m.c*n.f + m.e, m.b*n.e + m.d*n.f + m.f,
	}
}

func (m mat) apply(x, y float64) (float64, float64) {
	return m.a*x + m.c*y + m.e, m.b*x + m.d*y + m.f
}

type pt struct{ x, y float64 }

// shape is a filled set of polygons.
type shape struct {
	polys   [][]pt
	evenOdd bool
	alpha   float64
}

func parseTransform(s string) mat {
	m := identity
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		open := strings.IndexByte(s, '(')
		end := strings.IndexByte(s, ')')
		if open < 0 || end < open {
			break
		}
		name := strings.TrimSpace(strings.Trim(s[:open], ", "))
		args := parseNums(s[open+1 : end])
		s = s[end+1:]
		var t mat
		switch name {
		case "translate":
			t = identity
			if len(args) > 0 {
				t.e = args[0]
			}
			if len(args) > 1 {
				t.f = args[1]
			}
		case "scale":
			t = identity
			if len(args) > 0 {
				t.a, t.d = args[0], args[0]
			}
			if len(args) > 1 {
				t.d = args[1]
			}
		case "matrix":
			if len(args) == 6 {
				t = mat{args[0], args[1], args[2], args[3], args[4], args[5]}
			} else {
				t = identity
			}
		case "rotate":
			t = identity
			if len(args) > 0 {
				a := args[0] * math.Pi / 180
				r := mat{math.Cos(a), math.Sin(a), -math.Sin(a), math.Cos(a), 0, 0}
				if len(args) == 3 {
					r = mat{1, 0, 0, 1, args[1], args[2]}.mul(r).mul(mat{1, 0, 0, 1, -args[1], -args[2]})
				}
				t = r
			}
		default:
			t = identity
		}
		m = m.mul(t)
	}
	return m
}

func parseNums(s string) []float64 {
	var out []float64
	sc := numScanner{s: s}
	for {
		v, ok := sc.next()
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

// numScanner reads SVG numbers, which may run together ("1.5.5", "1-2").
type numScanner struct {
	s string
	i int
}

func (n *numScanner) skip() {
	for n.i < len(n.s) && (n.s[n.i] == ' ' || n.s[n.i] == ',' || n.s[n.i] == '\n' || n.s[n.i] == '\t' || n.s[n.i] == '\r') {
		n.i++
	}
}

func (n *numScanner) next() (float64, bool) {
	n.skip()
	start := n.i
	if n.i < len(n.s) && (n.s[n.i] == '-' || n.s[n.i] == '+') {
		n.i++
	}
	dot, digits := false, false
	for n.i < len(n.s) {
		c := n.s[n.i]
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c == '.' && !dot:
			dot = true
		case (c == 'e' || c == 'E') && digits:
			n.i++
			if n.i < len(n.s) && (n.s[n.i] == '-' || n.s[n.i] == '+') {
				n.i++
			}
			continue
		default:
			goto done
		}
		n.i++
	}
done:
	if !digits {
		n.i = start
		return 0, false
	}
	v, err := strconv.ParseFloat(n.s[start:n.i], 64)
	return v, err == nil
}

// flag reads an arc flag, which may be written without separators.
func (n *numScanner) flag() (bool, bool) {
	n.skip()
	if n.i < len(n.s) && (n.s[n.i] == '0' || n.s[n.i] == '1') {
		n.i++
		return n.s[n.i-1] == '1', true
	}
	return false, false
}

// parsePath flattens SVG path data into polygons.
func parsePath(d string, m mat) [][]pt {
	var polys [][]pt
	var cur []pt
	var x, y, sx, sy, lcx, lcy float64 // pen, subpath start, last control
	var lastCmd byte
	add := func(px, py float64) {
		tx, ty := m.apply(px, py)
		cur = append(cur, pt{tx, ty})
	}
	flush := func() {
		if len(cur) > 2 {
			polys = append(polys, cur)
		}
		cur = nil
	}
	cubic := func(x1, y1, x2, y2, x3, y3 float64) {
		for i := 1; i <= 12; i++ {
			t := float64(i) / 12
			u := 1 - t
			add(u*u*u*x+3*u*u*t*x1+3*u*t*t*x2+t*t*t*x3, u*u*u*y+3*u*u*t*y1+3*u*t*t*y2+t*t*t*y3)
		}
		x, y = x3, y3
	}
	quad := func(x1, y1, x2, y2 float64) {
		for i := 1; i <= 10; i++ {
			t := float64(i) / 10
			u := 1 - t
			add(u*u*x+2*u*t*x1+t*t*x2, u*u*y+2*u*t*y1+t*t*y2)
		}
		x, y = x2, y2
	}
	sc := numScanner{s: d}
	var cmd byte
	for {
		sc.skip()
		if sc.i >= len(sc.s) {
			break
		}
		c := sc.s[sc.i]
		if strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", c) >= 0 {
			cmd = c
			sc.i++
		} else if cmd == 0 {
			break
		}
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = x, y
		}
		num := func() float64 { v, _ := sc.next(); return v }
		switch cmd | 0x20 {
		case 'z':
			flush()
			x, y = sx, sy
			lastCmd = 'z'
			continue
		case 'm':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			b := num()
			flush()
			x, y = ox+a, oy+b
			sx, sy = x, y
			add(x, y)
			// Further pairs are line-tos.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			x, y = ox+a, oy+num()
			add(x, y)
		case 'h':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			x = ox + a
			add(x, y)
		case 'v':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			y = oy + a
			add(x, y)
		case 'c':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			x1, y1 := ox+a, oy+num()
			x2, y2 := ox+num(), oy+num()
			x3, y3 := ox+num(), oy+num()
			lcx, lcy = x2, y2
			cubic(x1, y1, x2, y2, x3, y3)
		case 's':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			x1, y1 := x, y
			if lastCmd == 'c' || lastCmd == 's' {
				x1, y1 = 2*x-lcx, 2*y-lcy
			}
			x2, y2 := ox+a, oy+num()
			x3, y3 := ox+num(), oy+num()
			lcx, lcy = x2, y2
			cubic(x1, y1, x2, y2, x3, y3)
		case 'q':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			x1, y1 := ox+a, oy+num()
			x2, y2 := ox+num(), oy+num()
			lcx, lcy = x1, y1
			quad(x1, y1, x2, y2)
		case 't':
			a, ok := sc.next()
			if !ok {
				return polys
			}
			x1, y1 := x, y
			if lastCmd == 'q' || lastCmd == 't' {
				x1, y1 = 2*x-lcx, 2*y-lcy
			}
			x2, y2 := ox+a, oy+num()
			lcx, lcy = x1, y1
			quad(x1, y1, x2, y2)
		case 'a':
			rx, ok := sc.next()
			if !ok {
				return polys
			}
			ry := num()
			rot := num()
			large, _ := sc.flag()
			sweep, _ := sc.flag()
			x2, y2 := ox+num(), oy+num()
			for _, p := range arcPoints(x, y, rx, ry, rot, large, sweep, x2, y2) {
				add(p.x, p.y)
			}
			x, y = x2, y2
		}
		lastCmd = cmd | 0x20
	}
	flush()
	return polys
}

// arcPoints flattens an SVG elliptical arc (endpoint parameterisation).
func arcPoints(x1, y1, rx, ry, rotDeg float64, large, sweep bool, x2, y2 float64) []pt {
	if rx == 0 || ry == 0 || x1 == x2 && y1 == y2 {
		return []pt{{x2, y2}}
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := rotDeg * math.Pi / 180
	cp, sp := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cp*dx+sp*dy, -sp*dx+cp*dy
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		rx, ry = rx*math.Sqrt(l), ry*math.Sqrt(l)
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := math.Sqrt(math.Max(num/den, 0))
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx, cy := cp*cxp-sp*cyp+(x1+x2)/2, sp*cxp+cp*cyp+(y1+y2)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	n := max(int(math.Abs(dt)/(math.Pi/16))+1, 2)
	out := make([]pt, 0, n)
	for i := 1; i <= n; i++ {
		t := t1 + dt*float64(i)/float64(n)
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		out = append(out, pt{cp*ex - sp*ey + cx, sp*ex + cp*ey + cy})
	}
	return out
}

func ellipsePoly(cx, cy, rx, ry float64, m mat) []pt {
	var out []pt
	for i := 0; i < 48; i++ {
		t := float64(i) / 48 * 2 * math.Pi
		x, y := m.apply(cx+rx*math.Cos(t), cy+ry*math.Sin(t))
		out = append(out, pt{x, y})
	}
	return out
}

func attrF(attrs map[string]string, k string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(attrs[k], "px"), 64)
	return v
}

// styleProp reads a presentation property from an attribute or the style.
func styleProp(attrs map[string]string, k string) string {
	for _, decl := range strings.Split(attrs["style"], ";") {
		if n, v, ok := strings.Cut(decl, ":"); ok && strings.TrimSpace(n) == k {
			return strings.TrimSpace(v)
		}
	}
	return attrs[k]
}

// parseSymbolic reads the shapes of a symbolic SVG, in viewBox units.
func parseSymbolic(path string) (shapes []shape, vbW, vbH float64, vbX, vbY float64) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, 0, 0
	}
	defer fh.Close()
	dec := xml.NewDecoder(fh)
	type frame struct {
		m       mat
		hidden  bool
		opacity float64
		evenOdd bool
	}
	stack := []frame{{identity, false, 1, false}}
	vbW, vbH = 16, 16
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			top := stack[len(stack)-1]
			f := top
			f.m = top.m.mul(parseTransform(attrs["transform"]))
			if styleProp(attrs, "display") == "none" || styleProp(attrs, "visibility") == "hidden" {
				f.hidden = true
			}
			if o := styleProp(attrs, "opacity"); o != "" {
				v, _ := strconv.ParseFloat(o, 64)
				f.opacity *= v
			}
			if r := styleProp(attrs, "fill-rule"); r != "" {
				f.evenOdd = r == "evenodd"
			}
			stack = append(stack, f)
			name := t.Name.Local
			if name == "svg" {
				if vb := parseNums(attrs["viewBox"]); len(vb) == 4 {
					vbX, vbY, vbW, vbH = vb[0], vb[1], vb[2], vb[3]
				} else {
					if w := attrF(attrs, "width"); w > 0 {
						vbW = w
					}
					if h := attrF(attrs, "height"); h > 0 {
						vbH = h
					}
				}
				continue
			}
			switch name {
			case "defs", "clipPath", "mask", "metadata", "title", "style", "linearGradient", "radialGradient", "filter":
				stack[len(stack)-1].hidden = true
				continue
			}
			if f.hidden || styleProp(attrs, "fill") == "none" {
				continue
			}
			alpha := f.opacity
			if o := styleProp(attrs, "fill-opacity"); o != "" {
				v, _ := strconv.ParseFloat(o, 64)
				alpha *= v
			}
			var polys [][]pt
			switch name {
			case "path":
				polys = parsePath(attrs["d"], f.m)
			case "rect":
				x, y, w, h := attrF(attrs, "x"), attrF(attrs, "y"), attrF(attrs, "width"), attrF(attrs, "height")
				rx, ry := attrF(attrs, "rx"), attrF(attrs, "ry")
				if rx == 0 {
					rx = ry
				}
				if ry == 0 {
					ry = rx
				}
				rx, ry = math.Min(rx, w/2), math.Min(ry, h/2)
				var d string
				if rx > 0 {
					d = "M" + ff(x+rx) + "," + ff(y) + "H" + ff(x+w-rx) + "A" + ff(rx) + "," + ff(ry) + " 0 0 1 " + ff(x+w) + "," + ff(y+ry) +
						"V" + ff(y+h-ry) + "A" + ff(rx) + "," + ff(ry) + " 0 0 1 " + ff(x+w-rx) + "," + ff(y+h) +
						"H" + ff(x+rx) + "A" + ff(rx) + "," + ff(ry) + " 0 0 1 " + ff(x) + "," + ff(y+h-ry) +
						"V" + ff(y+ry) + "A" + ff(rx) + "," + ff(ry) + " 0 0 1 " + ff(x+rx) + "," + ff(y) + "Z"
				} else {
					d = "M" + ff(x) + "," + ff(y) + "h" + ff(w) + "v" + ff(h) + "h" + ff(-w) + "Z"
				}
				polys = parsePath(d, f.m)
			case "circle":
				r := attrF(attrs, "r")
				polys = [][]pt{ellipsePoly(attrF(attrs, "cx"), attrF(attrs, "cy"), r, r, f.m)}
			case "ellipse":
				polys = [][]pt{ellipsePoly(attrF(attrs, "cx"), attrF(attrs, "cy"), attrF(attrs, "rx"), attrF(attrs, "ry"), f.m)}
			case "polygon":
				ns := parseNums(attrs["points"])
				var poly []pt
				for i := 0; i+1 < len(ns); i += 2 {
					x, y := f.m.apply(ns[i], ns[i+1])
					poly = append(poly, pt{x, y})
				}
				polys = [][]pt{poly}
			}
			if len(polys) > 0 {
				shapes = append(shapes, shape{polys: polys, evenOdd: f.evenOdd, alpha: alpha})
			}
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return shapes, vbW, vbH, vbX, vbY
}

func ff(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// renderSymbolic rasterises a symbolic icon at px×px in colour col.
func renderSymbolic(path string, px int, col color.NRGBA) *image.NRGBA {
	shapes, vbW, vbH, vbX, vbY := parseSymbolic(path)
	if len(shapes) == 0 {
		return nil
	}
	const ss = 4 // supersampling per axis
	n := px * ss
	cov := make([]float64, px*px)
	sx, sy := float64(n)/vbW, float64(n)/vbH
	inside := make([]uint8, n)
	for _, s := range shapes {
		// Edges in supersampled pixel space.
		type edge struct{ x0, y0, x1, y1 float64 }
		var edges []edge
		for _, p := range s.polys {
			for i := range p {
				a, b := p[i], p[(i+1)%len(p)]
				edges = append(edges, edge{(a.x - vbX) * sx, (a.y - vbY) * sy, (b.x - vbX) * sx, (b.y - vbY) * sy})
			}
		}
		layer := make([]uint8, px*px)
		for row := 0; row < n; row++ {
			y := float64(row) + 0.5
			type cross struct {
				x   float64
				dir int
			}
			var xs []cross
			for _, e := range edges {
				if e.y0 == e.y1 {
					continue
				}
				dir := 1
				y0, y1, x0, x1 := e.y0, e.y1, e.x0, e.x1
				if y0 > y1 {
					y0, y1, x0, x1, dir = y1, y0, x1, x0, -1
				}
				if y < y0 || y >= y1 {
					continue
				}
				xs = append(xs, cross{x0 + (y-y0)/(y1-y0)*(x1-x0), dir})
			}
			if len(xs) == 0 {
				continue
			}
			sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
			for i := range inside {
				inside[i] = 0
			}
			wind := 0
			for i := 0; i+1 < len(xs); i++ {
				wind += xs[i].dir
				in := wind != 0
				if s.evenOdd {
					in = (i+1)%2 == 1
				}
				if !in {
					continue
				}
				from := int(math.Ceil(xs[i].x - 0.5))
				to := int(math.Ceil(xs[i+1].x - 0.5))
				for x := max(from, 0); x < min(to, n); x++ {
					inside[x] = 1
				}
			}
			py := row / ss
			for x := 0; x < n; x++ {
				if inside[x] != 0 {
					layer[py*px+x/ss]++
				}
			}
		}
		for i, v := range layer {
			a := float64(v) / (ss * ss) * s.alpha
			cov[i] = cov[i] + a*(1-cov[i])
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	for i, c := range cov {
		if c > 0 {
			img.Pix[i*4+0] = col.R
			img.Pix[i*4+1] = col.G
			img.Pix[i*4+2] = col.B
			img.Pix[i*4+3] = uint8(math.Min(c, 1) * float64(col.A))
		}
	}
	return img
}
