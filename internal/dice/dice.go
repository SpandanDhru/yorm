// Package dice parses and rolls dice expressions like "2d6+1d4+3" or
// "2d20kh1+5" (advantage), and checks physical rolls entered by players.
//
// A roll line is an expression, optionally followed by "=" and what was
// rolled on real dice, optionally followed by a label:
//
//	1d20+5 longsword        server rolls
//	2d6+3 = 4 5 fire        physical: each die's face
//	1d20+5 = 17 stealth     physical: just the total
package dice

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on what can be rolled.
const (
	MaxDice     = 100
	MinSides    = 2
	MaxSides    = 100
	MaxTerms    = 20
	MaxConstant = 1000
	MaxLineLen  = 200
	MaxLabelLen = 60
)

var ErrSyntax = errors.New("dice: not a dice expression")

// Term is one "NdS" (optionally keeping the highest or lowest K) or a
// constant, with its sign.
type Term struct {
	Neg   bool `json:"neg,omitempty"`
	Count int  `json:"count,omitempty"` // 0 for a constant
	Sides int  `json:"sides,omitempty"`
	Keep  int  `json:"keep,omitempty"` // 0 keeps all
	Low   bool `json:"low,omitempty"`  // keep the lowest instead of the highest
	Const int  `json:"const,omitempty"`
}

type Expr []Term

// Line is a parsed roll line.
type Line struct {
	Expr  Expr
	Faces []int // physical: each die's face, in order
	Total *int  // physical: only the total
	Label string
}

// ParseLine parses an expression, an optional "= results", and a label.
func ParseLine(s string) (Line, error) {
	if len(s) > MaxLineLen {
		return Line{}, fmt.Errorf("dice: line longer than %d characters", MaxLineLen)
	}
	p := parser{s: s}
	expr, err := p.expr()
	if err != nil {
		return Line{}, err
	}
	l := Line{Expr: expr}
	p.space()
	if p.peek() == '=' {
		p.i++
		nums := p.numbers()
		switch len(nums) {
		case 0:
			return Line{}, errors.New("dice: nothing after =")
		case 1:
			l.Total = &nums[0]
		default:
			l.Faces = nums
		}
	}
	l.Label = strings.TrimSpace(p.s[p.i:])
	if utf8.RuneCountInString(l.Label) > MaxLabelLen {
		return Line{}, fmt.Errorf("dice: label longer than %d characters", MaxLabelLen)
	}
	return l, nil
}

// Parse parses a bare expression; anything after it is an error.
func Parse(s string) (Expr, error) {
	p := parser{s: s}
	e, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.space(); p.i != len(p.s) {
		return nil, fmt.Errorf("dice: unexpected %q", p.s[p.i:])
	}
	return e, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *parser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

// int reads digits; ok is false if there were none. Values are capped so
// a long digit string can't overflow.
func (p *parser) int() (n int, ok bool) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		if n < 1_000_000 {
			n = n*10 + int(p.s[p.i]-'0')
		}
		p.i++
	}
	return n, p.i > start
}

func (p *parser) expr() (Expr, error) {
	var e Expr
	dice := 0
	neg := false
	p.space()
	if c := p.peek(); c == '+' || c == '-' {
		neg = c == '-'
		p.i++
	}
	for {
		p.space()
		t, err := p.term()
		if err != nil {
			return nil, err
		}
		t.Neg = neg
		dice += t.Count
		e = append(e, t)
		if len(e) > MaxTerms {
			return nil, fmt.Errorf("dice: more than %d terms", MaxTerms)
		}
		if dice > MaxDice {
			return nil, fmt.Errorf("dice: more than %d dice", MaxDice)
		}
		// Another term follows only if an operator does. Anything else
		// ends the expression (the rest is "= results" or a label).
		save := p.i
		p.space()
		c := p.peek()
		if c != '+' && c != '-' {
			p.i = save
			return e, nil
		}
		p.i++
		neg = c == '-'
	}
}

func (p *parser) term() (Term, error) {
	start := p.i
	n, hasN := p.int()
	if c := p.peek(); c != 'd' && c != 'D' {
		if !hasN {
			return Term{}, ErrSyntax
		}
		if n > MaxConstant {
			return Term{}, fmt.Errorf("dice: constants go up to %d", MaxConstant)
		}
		return Term{Const: n}, nil
	}
	p.i++ // 'd'
	if !hasN {
		n = 1
	}
	var sides int
	if p.peek() == '%' {
		p.i++
		sides = 100
	} else {
		var ok bool
		if sides, ok = p.int(); !ok {
			return Term{}, fmt.Errorf("dice: %q needs a number of sides", p.s[start:p.i])
		}
	}
	if n < 1 || n > MaxDice {
		return Term{}, fmt.Errorf("dice: roll 1 to %d dice", MaxDice)
	}
	if sides < MinSides || sides > MaxSides {
		return Term{}, fmt.Errorf("dice: dice have %d to %d sides", MinSides, MaxSides)
	}
	t := Term{Count: n, Sides: sides}
	if rest := strings.ToLower(p.s[p.i:min(p.i+2, len(p.s))]); rest == "kh" || rest == "kl" {
		p.i += 2
		k, ok := p.int()
		if !ok {
			k = 1
		}
		if k < 1 || k > n {
			return Term{}, fmt.Errorf("dice: can keep 1 to %d of %dd%d", n, n, sides)
		}
		t.Keep, t.Low = k, rest == "kl"
		if k == n {
			t.Keep, t.Low = 0, false // keeping all is no keep
		}
	}
	// A letter right after a term ("2d6x") is a typo, not a label.
	if r, _ := utf8.DecodeRuneInString(p.s[p.i:]); p.i < len(p.s) && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
		return Term{}, fmt.Errorf("dice: unexpected %q", p.s[start:p.i+utf8.RuneLen(r)])
	}
	return t, nil
}

// numbers reads whitespace- or comma-separated integers. A total can be
// negative ("1d4-5 = -2").
func (p *parser) numbers() []int {
	var nums []int
	for {
		p.space()
		save := p.i
		neg := p.peek() == '-'
		if neg {
			p.i++
		}
		n, ok := p.int()
		if !ok || (p.i < len(p.s) && p.s[p.i] != ' ' && p.s[p.i] != ',' && p.s[p.i] != '\t') {
			p.i = save
			return nums
		}
		if neg {
			n = -n
		}
		nums = append(nums, n)
		if p.peek() == ',' {
			p.i++
		}
	}
}

// Dice counts the dice in e.
func (e Expr) Dice() int {
	n := 0
	for _, t := range e {
		n += t.Count
	}
	return n
}

// Bounds returns the lowest and highest possible totals.
func (e Expr) Bounds() (lo, hi int) {
	for _, t := range e {
		kept := t.Count
		if t.Keep > 0 {
			kept = t.Keep
		}
		tlo, thi := t.Const, t.Const
		if t.Count > 0 {
			tlo, thi = kept, kept*t.Sides
		}
		if t.Neg {
			tlo, thi = -thi, -tlo
		}
		lo, hi = lo+tlo, hi+thi
	}
	return lo, hi
}

func (e Expr) String() string {
	var b strings.Builder
	for i, t := range e {
		switch {
		case t.Neg:
			b.WriteByte('-')
		case i > 0:
			b.WriteByte('+')
		}
		if t.Count == 0 {
			b.WriteString(strconv.Itoa(t.Const))
			continue
		}
		fmt.Fprintf(&b, "%dd%d", t.Count, t.Sides)
		if t.Keep > 0 {
			k := "kh"
			if t.Low {
				k = "kl"
			}
			fmt.Fprintf(&b, "%s%d", k, t.Keep)
		}
	}
	return b.String()
}

// TermResult is one term's outcome.
type TermResult struct {
	Term
	Faces   []int  `json:"faces,omitempty"`   // each die, in roll order
	Dropped []bool `json:"dropped,omitempty"` // parallel to Faces, for keep
	Value   int    `json:"value"`             // signed contribution to the total
}

type Result struct {
	Terms []TermResult `json:"terms"`
	Total int          `json:"total"`
}

// Roll rolls e with roll, which returns 1 to sides.
func (e Expr) Roll(roll func(sides int) int) Result {
	faces := make([]int, 0, e.Dice())
	for _, t := range e {
		for range t.Count {
			faces = append(faces, roll(t.Sides))
		}
	}
	r, _ := e.WithFaces(faces) // faces came from dice of the right size
	return r
}

// WithFaces totals e using faces rolled on physical dice, one per die in
// the order the terms list them.
func (e Expr) WithFaces(faces []int) (Result, error) {
	if len(faces) != e.Dice() {
		return Result{}, fmt.Errorf("dice: %s has %d dice, got %d results", e, e.Dice(), len(faces))
	}
	var r Result
	for _, t := range e {
		tr := TermResult{Term: t}
		if t.Count == 0 {
			tr.Value = t.Const
		} else {
			tr.Faces = faces[:t.Count:t.Count]
			faces = faces[t.Count:]
			for _, f := range tr.Faces {
				if f < 1 || f > t.Sides {
					return Result{}, fmt.Errorf("dice: a d%d can't show %d", t.Sides, f)
				}
			}
			tr.Dropped = dropped(tr.Faces, t.Keep, t.Low)
			for i, f := range tr.Faces {
				if tr.Dropped == nil || !tr.Dropped[i] {
					tr.Value += f
				}
			}
		}
		if t.Neg {
			tr.Value = -tr.Value
		}
		r.Total += tr.Value
		r.Terms = append(r.Terms, tr)
	}
	return r, nil
}

// dropped marks the faces not kept, or returns nil if all are kept. Ties
// drop the later die, so the result doesn't depend on sort stability.
func dropped(faces []int, keep int, low bool) []bool {
	if keep == 0 {
		return nil
	}
	drop := make([]bool, len(faces))
	for range len(faces) - keep {
		worst := -1
		for i, f := range faces {
			if drop[i] {
				continue
			}
			if worst < 0 || (!low && f <= faces[worst]) || (low && f >= faces[worst]) {
				worst = i
			}
		}
		drop[worst] = true
	}
	return drop
}
