// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Issue 56718: in a normal build, a package does not compile the
// generic instantiations that its imports already compiled, but a
// plugin build (-dynlink) compiles all of them. The export data of
// the packages, and so the package fingerprints that plugin.Open
// compares, must not depend on that.

package main

import (
	"log"
	"plugin"
	"slices"
	"sync/atomic"
)

var last atomic.Pointer[[]int]

func sorted(s []int) []int {
	s = slices.Clone(s)
	slices.Sort(s)
	last.Store(&s)
	return *last.Load()
}

func main() {
	if got := sorted([]int{3, 1, 2}); !slices.Equal(got, []int{1, 2, 3}) {
		log.Fatalf("host: got %v", got)
	}
	p, err := plugin.Open("issue56718.so")
	if err != nil {
		log.Fatal(err)
	}
	f, err := p.Lookup("Sorted")
	if err != nil {
		log.Fatal(err)
	}
	if got := f.(func([]int) []int)([]int{6, 4, 5}); !slices.Equal(got, []int{4, 5, 6}) {
		log.Fatalf("plugin: got %v", got)
	}
}
