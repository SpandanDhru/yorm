package game

import (
	"maps"
	"slices"
)

// Fog is fog of war over a map. Players see a cell if it is revealed to
// the party or to them; the DM sees everything.
type Fog struct {
	Enabled bool            `json:"enabled"`
	Party   Bits            `json:"party"`
	Users   map[UserID]Bits `json:"users"` // reveals for one player only
}

// Bits is a bitset over a map's cells, row by row: cell (x, y) is bit
// y*cols + x. In JSON it is base64, so even a 200x200 map's fog is ~7 KB.
type Bits []byte

func (b Bits) Has(i int) bool {
	return i >= 0 && i/8 < len(b) && b[i/8]&(1<<(i%8)) != 0
}

// set sets bit i of a bitset for n cells, allocating it on first use.
func (b *Bits) set(i, n int, on bool) {
	if *b == nil {
		if !on {
			return
		}
		*b = make(Bits, (n+7)/8)
	}
	if on {
		(*b)[i/8] |= 1 << (i % 8)
	} else {
		(*b)[i/8] &^= 1 << (i % 8)
	}
}

func (f Fog) clone() Fog {
	f.Party = slices.Clone(f.Party)
	if f.Users != nil {
		users := make(map[UserID]Bits, len(f.Users))
		for u, b := range f.Users {
			users[u] = slices.Clone(b)
		}
		f.Users = users
	}
	return f
}

// viewFor returns the fog as a player sees it: the party's reveals and
// only their own personal ones.
func (f Fog) viewFor(u UserID) Fog {
	f = f.clone()
	users := f.Users
	f.Users = nil
	if b, ok := users[u]; ok {
		f.Users = map[UserID]Bits{u: b}
	}
	return f
}

func (m *Map) cellIndex(c Cell) int { return c.Y*m.Cols + c.X }

func (m *Map) paintFog(user UserID, cells []Cell, reveal bool) {
	if m == nil {
		return
	}
	n := m.Cols * m.Rows
	bits := &m.Fog.Party
	if user != "" {
		if m.Fog.Users == nil {
			m.Fog.Users = map[UserID]Bits{}
		}
		b := m.Fog.Users[user]
		defer func() { m.Fog.Users[user] = b }()
		bits = &b
	}
	for _, c := range cells {
		if m.InBounds(c, 1) {
			bits.set(m.cellIndex(c), n, reveal)
		}
	}
}

// Revealed reports whether user can see cell c through the fog.
func (m *Map) Revealed(user UserID, c Cell) bool {
	if !m.Fog.Enabled {
		return true
	}
	i := m.cellIndex(c)
	return m.Fog.Party.Has(i) || m.Fog.Users[user].Has(i)
}

// regridFog carries the fog of old over to m, a new grid for the same map:
// cells that still exist keep their state.
func (m *Map) regridFog(old *Map) {
	m.Fog = Fog{Enabled: old.Fog.Enabled}
	move := func(from Bits) Bits {
		if from == nil {
			return nil
		}
		var to Bits
		for y := range min(old.Rows, m.Rows) {
			for x := range min(old.Cols, m.Cols) {
				if c := (Cell{x, y}); from.Has(old.cellIndex(c)) {
					to.set(m.cellIndex(c), m.Cols*m.Rows, true)
				}
			}
		}
		return to
	}
	m.Fog.Party = move(old.Fog.Party)
	for _, u := range slices.Sorted(maps.Keys(old.Fog.Users)) {
		if b := move(old.Fog.Users[u]); b != nil {
			if m.Fog.Users == nil {
				m.Fog.Users = map[UserID]Bits{}
			}
			m.Fog.Users[u] = b
		}
	}
}
