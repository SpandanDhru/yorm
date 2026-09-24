package dice

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want string // canonical form; empty means an error
		lo   int
		hi   int
	}{
		{"1d20", "1d20", 1, 20},
		{"d20", "1d20", 1, 20},
		{"D8", "1d8", 1, 8},
		{"2d6+3", "2d6+3", 5, 15},
		{" 2d6 + 1d4 + 3 ", "2d6+1d4+3", 6, 19},
		{"1d4-1", "1d4-1", 0, 3},
		{"-1d4", "-1d4", -4, -1},
		{"5", "5", 5, 5},
		{"d%", "1d100", 1, 100},
		{"2d20kh1+5", "2d20kh1+5", 6, 25},
		{"2d20kl1", "2d20kl1", 1, 20},
		{"4d6kh3", "4d6kh3", 3, 18},
		{"2d20kh", "2d20kh1", 1, 20},
		{"3d6kh3", "3d6", 3, 18}, // keeping all is no keep
		{"1d6-1d6", "1d6-1d6", -5, 5},
		{"100d2", "100d2", 100, 200},
		{"", "", 0, 0},
		{"d", "", 0, 0},
		{"1d", "", 0, 0},
		{"1d1", "", 0, 0},
		{"1d101", "", 0, 0},
		{"0d6", "", 0, 0},
		{"101d6", "", 0, 0},
		{"60d6+41d6", "", 0, 0},
		{"2d6kh3", "", 0, 0},
		{"2d6kh0", "", 0, 0},
		{"2d6x", "", 0, 0},
		{"1001", "", 0, 0},
		{"2d6+", "", 0, 0},
		{"++2", "", 0, 0},
		{"2d6 fire", "", 0, 0}, // Parse takes no label
		{"99999999999999999999d6", "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			e, err := Parse(tt.in)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("Parse = %s, want an error", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.String() != tt.want {
				t.Fatalf("String = %s, want %s", e, tt.want)
			}
			if lo, hi := e.Bounds(); lo != tt.lo || hi != tt.hi {
				t.Fatalf("Bounds = %d..%d, want %d..%d", lo, hi, tt.lo, tt.hi)
			}
		})
	}
}

func TestTooManyTerms(t *testing.T) {
	if _, err := Parse(strings.Repeat("1+", MaxTerms) + "1"); err == nil {
		t.Fatal("parsed more than MaxTerms terms")
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		in    string
		expr  string
		faces []int
		total *int
		label string
		bad   bool
	}{
		{in: "1d20+5 longsword", expr: "1d20+5", label: "longsword"},
		{in: "1d20 + 5   Stealth check ", expr: "1d20+5", label: "Stealth check"},
		{in: "2d6+3 = 4 5 fire damage", expr: "2d6+3", faces: []int{4, 5}, label: "fire damage"},
		{in: "2d6+3=4,5", expr: "2d6+3", faces: []int{4, 5}},
		{in: "1d20+5 = 17 stealth", expr: "1d20+5", total: ptr(17), label: "stealth"},
		{in: "1d4-5 = -2", expr: "1d4-5", total: ptr(-2)},
		{in: "d20", expr: "1d20"},
		{in: "2d6 = ", bad: true},
		{in: "2d6 = fire", bad: true},
		{in: "fire", bad: true},
		{in: "2d6 " + strings.Repeat("x", MaxLabelLen+1), bad: true},
		{in: strings.Repeat("1", MaxLineLen+1), bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			l, err := ParseLine(tt.in)
			if tt.bad {
				if err == nil {
					t.Fatalf("ParseLine = %+v, want an error", l)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.Expr.String() != tt.expr || !reflect.DeepEqual(l.Faces, tt.faces) || !reflect.DeepEqual(l.Total, tt.total) || l.Label != tt.label {
				t.Fatalf("got expr %s faces %v total %v label %q", l.Expr, l.Faces, deref(l.Total), l.Label)
			}
		})
	}
}

func ptr(n int) *int { return &n }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestWithFaces(t *testing.T) {
	tests := []struct {
		expr    string
		faces   []int
		total   int
		dropped [][]bool // per term
		bad     bool
	}{
		{expr: "2d6+3", faces: []int{4, 5}, total: 12},
		{expr: "2d20kh1+5", faces: []int{7, 16}, total: 21, dropped: [][]bool{{true, false}, nil}},
		{expr: "2d20kl1", faces: []int{7, 16}, total: 7, dropped: [][]bool{{false, true}}},
		{expr: "4d6kh3", faces: []int{3, 1, 6, 1}, total: 10, dropped: [][]bool{{false, false, false, true}}},
		{expr: "2d20kh1", faces: []int{9, 9}, total: 9, dropped: [][]bool{{false, true}}},
		{expr: "1d8-1d4", faces: []int{5, 3}, total: 2},
		{expr: "2d6", faces: []int{4}, bad: true},
		{expr: "2d6", faces: []int{4, 5, 6}, bad: true},
		{expr: "1d20", faces: []int{21}, bad: true},
		{expr: "1d20", faces: []int{0}, bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			e, err := Parse(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			r, err := e.WithFaces(tt.faces)
			if tt.bad {
				if err == nil {
					t.Fatalf("WithFaces = %+v, want an error", r)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Total != tt.total {
				t.Fatalf("total = %d, want %d", r.Total, tt.total)
			}
			for i, d := range tt.dropped {
				if !reflect.DeepEqual(r.Terms[i].Dropped, d) {
					t.Fatalf("term %d dropped = %v, want %v", i, r.Terms[i].Dropped, d)
				}
			}
		})
	}
}

// Rolling uses one die per term die, of the right size, in order.
func TestRoll(t *testing.T) {
	e, err := Parse("2d20kh1+1d4-2")
	if err != nil {
		t.Fatal(err)
	}
	var asked []int
	next := []int{3, 18, 2}
	r := e.Roll(func(sides int) int {
		asked = append(asked, sides)
		f := next[0]
		next = next[1:]
		return f
	})
	if !reflect.DeepEqual(asked, []int{20, 20, 4}) || r.Total != 18 {
		t.Fatalf("asked %v, total %d; want [20 20 4] and 18+2-2", asked, r.Total)
	}
}

// Every total a roll can produce is within Bounds.
func TestRollStaysInBounds(t *testing.T) {
	for _, s := range []string{"4d6kh3", "2d20kl1-5", "1d6-1d6+3", "3d8"} {
		e, _ := Parse(s)
		lo, hi := e.Bounds()
		for _, face := range []func(int) int{func(int) int { return 1 }, func(n int) int { return n }} {
			if r := e.Roll(face); r.Total < lo || r.Total > hi {
				t.Errorf("%s: total %d outside %d..%d", s, r.Total, lo, hi)
			}
		}
	}
}

// Parsing never panics, and whatever parses rolls within its bounds and
// prints back to something that parses the same.
func FuzzParseLine(f *testing.F) {
	for _, s := range []string{"1d20+5 longsword", "2d6+3 = 4 5 fire", "1d20+5 = 17", "4d6kh3", "d%-1", "2d20kl1=3,9", "1d4-5 = -2"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l, err := ParseLine(s)
		if err != nil {
			return
		}
		lo, hi := l.Expr.Bounds()
		r := l.Expr.Roll(func(n int) int { return n/2 + 1 })
		if r.Total < lo || r.Total > hi {
			t.Fatalf("%q: total %d outside %d..%d", s, r.Total, lo, hi)
		}
		again, err := Parse(l.Expr.String())
		if err != nil || again.String() != l.Expr.String() {
			t.Fatalf("%q: canonical %q reparses as %q, %v", s, l.Expr, again, err)
		}
		if l.Faces != nil {
			_, _ = l.Expr.WithFaces(l.Faces) // must not panic on any faces
		}
	})
}
