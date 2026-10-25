// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"slices"
	"sync/atomic"
)

var last atomic.Pointer[[]int]

func Sorted(s []int) []int {
	s = slices.Clone(s)
	slices.Sort(s)
	last.Store(&s)
	return *last.Load()
}

func main() {}
