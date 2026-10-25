// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a

// Box has a value receiver method, so *Box[T] gets a pointer wrapper.
type Box[T any] struct{ V T }

func (b Box[T]) Get() T { return b.V }

func (b *Box[T]) Set(v T) { b.V = v }

type Getter[T any] interface{ Get() T }

// Named embeds an instantiated type, so it has promoted method wrappers.
type Named[T any] struct {
	Box[T]
	Name string
}

func (n Named[T]) String() string { return n.Name }

// Map is a generic function with closures.
func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	each(xs, func(x T) { out = append(out, f(x)) })
	return out
}

func each[T any](xs []T, f func(T)) {
	for _, x := range xs {
		f(x)
	}
}

// Counter returns a closure over generic state.
func Counter[T comparable]() func(T) int {
	seen := map[T]int{}
	return func(x T) int {
		seen[x]++
		return seen[x]
	}
}
