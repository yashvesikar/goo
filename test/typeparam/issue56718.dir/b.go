// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package b

import (
	"fmt"

	"./a"
)

// Wrapped embeds an instantiated type.
type Wrapped struct {
	a.Named[string]
}

var W = Wrapped{a.Named[string]{a.Box[string]{"inner"}, "outer"}}

// Interface conversions of instantiated types.

func NewGetter(v int) a.Getter[int] { return a.Box[int]{v} }

func NewPtrGetter(v int) a.Getter[int] { return &a.Box[int]{v} }

func Stringer() fmt.Stringer { return W }

// Method values.

func GetFunc(b *a.Box[int]) func() int { return b.Get }

func SetFunc(b *a.Box[int]) func(int) { return b.Set }

func Strings(xs []int) []string {
	return a.Map(xs, func(x int) string { return fmt.Sprint(x) })
}

func Count() func(string) int { return a.Counter[string]() }
