package game

import "container/heap"

// PathCost returns the cheapest cost in feet for a token of the given size
// to move from one cell to another, or ok=false if no route exists.
//
// Rules: a token cannot enter a position where any of its cells is a wall
// or off the map, and cannot cut a corner diagonally past a wall. Entering
// a position where any of its cells is difficult terrain or water costs
// double. Diagonal steps cost one cell, or under the 5-10-5 rule every
// second diagonal step costs two. The search gives up past limit feet; a
// negative limit means no limit.
func PathCost(m *Map, rule DiagonalRule, size int, from, to Cell, limit int) (feet int, ok bool) {
	if from == to {
		return 0, true
	}
	g := grid{m: m, size: size}
	if g.blocked(to) {
		return 0, false
	}
	alternating := rule == DiagonalAlternating

	// Dijkstra over (cell, parity of diagonals taken so far). Parity only
	// matters under 5-10-5, where it decides the next diagonal's cost.
	dist := make(map[pathNode]int)
	start := pathNode{c: from}
	dist[start] = 0
	pq := &pathQueue{{node: start}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(pathItem)
		if cur.cost > dist[cur.node] {
			continue // stale entry
		}
		if cur.node.c == to {
			return cur.cost, true
		}
		for _, d := range steps {
			next := Cell{cur.node.c.X + d.X, cur.node.c.Y + d.Y}
			if g.blocked(next) {
				continue
			}
			diagonal := d.X != 0 && d.Y != 0
			if diagonal && (g.blocked(Cell{next.X, cur.node.c.Y}) || g.blocked(Cell{cur.node.c.X, next.Y})) {
				continue // no squeezing past a wall's corner
			}
			step, parity := m.CellFeet, cur.node.parity
			if diagonal && alternating {
				if parity {
					step *= 2
				}
				parity = !parity
			}
			if g.difficult(next) {
				step *= 2
			}
			cost := cur.cost + step
			if limit >= 0 && cost > limit {
				continue
			}
			n := pathNode{c: next, parity: parity}
			if old, seen := dist[n]; !seen || cost < old {
				dist[n] = cost
				heap.Push(pq, pathItem{node: n, cost: cost})
			}
		}
	}
	return 0, false
}

var steps = []Cell{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}}

// grid answers questions about a token's footprint at a position.
type grid struct {
	m    *Map
	size int
}

func (g grid) blocked(c Cell) bool {
	if !g.m.InBounds(c, g.size) {
		return true
	}
	return g.any(c, func(k TerrainKind) bool { return k == TerrainWall })
}

func (g grid) difficult(c Cell) bool {
	return g.any(c, func(k TerrainKind) bool { return k == TerrainDifficult || k == TerrainWater })
}

func (g grid) any(c Cell, pred func(TerrainKind) bool) bool {
	if len(g.m.Terrain) == 0 {
		return false
	}
	for dy := range g.size {
		for dx := range g.size {
			if k, ok := g.m.Terrain[Cell{c.X + dx, c.Y + dy}]; ok && pred(k) {
				return true
			}
		}
	}
	return false
}

type pathNode struct {
	c      Cell
	parity bool // an odd number of diagonal steps so far
}

type pathItem struct {
	node pathNode
	cost int
}

type pathQueue []pathItem

func (q pathQueue) Len() int           { return len(q) }
func (q pathQueue) Less(i, j int) bool { return q[i].cost < q[j].cost }
func (q pathQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *pathQueue) Push(x any)        { *q = append(*q, x.(pathItem)) }
func (q *pathQueue) Pop() any {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}
