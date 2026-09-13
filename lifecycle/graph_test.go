package lifecycle

import (
	"reflect"
	"testing"
)

// mk builds components with the given dependency indices.
func mk(deps ...[]int) []component {
	cs := make([]component, len(deps))
	for i, d := range deps {
		cs[i] = component{name: string(rune('a' + i)), after: d}
	}
	return cs
}

func TestLevelsNoEdgesIsOneLevel(t *testing.T) {
	t.Parallel()
	got := levels(mk(nil, nil, nil))
	want := [][]int{{0, 1, 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("levels = %v, want %v", got, want)
	}
}

func TestLevelsChain(t *testing.T) {
	t.Parallel()
	// a <- b <- c
	got := levels(mk(nil, []int{0}, []int{1}))
	want := [][]int{{0}, {1}, {2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("levels = %v, want %v", got, want)
	}
}

func TestLevelsDiamondPutsSiblingsOnOneLevel(t *testing.T) {
	t.Parallel()
	// a <- b, a <- c, (b,c) <- d
	got := levels(mk(nil, []int{0}, []int{0}, []int{1, 2}))
	want := [][]int{{0}, {1, 2}, {3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("levels = %v, want %v", got, want)
	}
}

func TestLevelsUsesDeepestDependency(t *testing.T) {
	t.Parallel()
	// d depends on a (level 0) and c (level 2) -> d must be level 3
	got := levels(mk(nil, []int{0}, []int{1}, []int{0, 2}))
	want := [][]int{{0}, {1}, {2}, {3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("levels = %v, want %v", got, want)
	}
}

func TestLevelsEmpty(t *testing.T) {
	t.Parallel()
	if got := levels(nil); len(got) != 0 {
		t.Errorf("levels(nil) = %v, want empty", got)
	}
}

func TestLevelsPreservesDeclarationOrderWithinALevel(t *testing.T) {
	t.Parallel()
	got := levels(mk(nil, nil, []int{0}, nil))
	want := [][]int{{0, 1, 3}, {2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("levels = %v, want %v", got, want)
	}
}
