// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"

	"./a"
	"./b"
)

func main() {
	// Values from b, used through interfaces.
	fmt.Println(b.NewGetter(1).Get(), b.NewPtrGetter(2).Get(), b.Stringer())

	// The same instantiations, used directly in main.
	box := &a.Box[int]{3}
	var g a.Getter[int] = box // (*Box[int]).Get pointer wrapper
	set := b.SetFunc(box)
	set(4)
	get := box.Get // Box[int].Get-fm
	fmt.Println(g.Get(), get(), b.GetFunc(box)())

	// Promoted methods of an embedded instantiated type.
	w := b.W
	var s fmt.Stringer = &w
	getW := w.Get
	w.Set("changed")
	fmt.Println(s, getW(), w.Get(), w.Box.Get())

	// Generic functions with closures.
	fmt.Println(b.Strings([]int{5, 6}), a.Map([]int{7, 8}, func(x int) string { return fmt.Sprint(-x) }))
	count := b.Count()
	count("x")
	fmt.Println(count("x"), a.Counter[string]()("x"))
}
