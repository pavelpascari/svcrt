package lifecycle

import "slices"

// levels groups components into start levels: every component in level n
// depends only on components in levels < n, so a level may start concurrently.
//
// A single forward pass suffices — no topological sort — because a Ref can only
// name an earlier component, so every dependency index is less than its
// dependent's. That is a direct consequence of the handle-based edge design.
func levels(cs []component) [][]int {
	if len(cs) == 0 {
		return nil
	}
	lvl := make([]int, len(cs))
	depth := 0
	for i, c := range cs {
		l := 0
		for _, d := range c.after {
			if lvl[d]+1 > l {
				l = lvl[d] + 1
			}
		}
		lvl[i] = l
		depth = max(depth, l)
	}
	out := make([][]int, depth+1)
	for i := range cs {
		out[lvl[i]] = append(out[lvl[i]], i)
	}
	return slices.Clip(out)
}
