// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"fmt"
	"internal/pkgbits"
	"slices"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/types"
	"cmd/internal/obj"
)

// Instantiation manifests.
//
// Every package that uses a generic function or type re-reads the
// generic body from export data and compiles its own copy of each
// instantiation it needs (shaped bodies such as pkg.F[go.shape.int],
// instance wrappers such as pkg.F[int], and method wrappers for
// instantiated types). These are emitted as DUPOK text symbols, so
// the linker keeps just one copy, but the duplicate compilation work
// is wasted (go.dev/issue/56718).
//
// To let downstream packages avoid that work, each package's export
// data records an instantiation manifest in its compiler-private
// root. The manifest's contract is:
//
//	Every name listed in a package's manifest is the link symbol
//	name of a function that is defined as a DUPOK text symbol in
//	that package's object file.
//
// Consequently, a package that imports it may refer to any listed
// function by name instead of compiling its own copy, and the linker
// will resolve the reference.
//
// The manifest only lists functions; runtime dictionaries and type
// descriptors are not included.

// instManifest is a set of instantiated function link symbol names.
//
// It is the only place that knows how manifest entries are
// represented and encoded in export data.
type instManifest struct {
	names map[string]struct{}
}

// add adds the function with link symbol name to m.
func (m *instManifest) add(name string) {
	if m.names == nil {
		m.names = make(map[string]struct{})
	}
	m.names[name] = struct{}{}
}

// has reports whether m contains the function with link symbol name.
func (m *instManifest) has(name string) bool {
	_, ok := m.names[name]
	return ok
}

// len returns the number of entries in m.
func (m *instManifest) len() int {
	return len(m.names)
}

// merge adds all entries of other to m.
func (m *instManifest) merge(other *instManifest) {
	for name := range other.names {
		m.add(name)
	}
}

// sorted returns m's entries in sorted order.
func (m *instManifest) sorted() []string {
	names := make([]string, 0, len(m.names))
	for name := range m.names {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// write encodes m into w. Entries are written in sorted order, so
// the encoding is deterministic.
func (m *instManifest) write(w *pkgbits.Encoder) {
	names := m.sorted()
	w.Len(len(names))
	for _, name := range names {
		w.String(name)
	}
}

// read decodes a manifest written by write from r and adds its
// entries to m.
func (m *instManifest) read(r *pkgbits.Decoder) {
	for i, n := 0, r.Len(); i < n; i++ {
		m.add(r.String())
	}
}

// instFuncs lists the generic function instantiations and method
// wrappers for instantiated types that were added to
// typecheck.Target.Funcs for compilation. These are the candidates
// for the local package's manifest.
var instFuncs []*ir.Func

// localInsts is the local package's manifest, computed on first
// use by localInstManifest.
var localInsts *instManifest

// importedInsts is the union of the manifests of all directly
// imported packages.
var importedInsts instManifest

// addInstFunc records that fn, a generic function instantiation or a
// method wrapper for an instantiated type, has been added to
// typecheck.Target.Funcs and will be compiled.
func addInstFunc(fn *ir.Func) {
	if fn.OClosure != nil {
		// Closures within instantiations are named after their
		// enclosing function and are compiled along with it.
		base.FatalfAt(fn.Pos(), "unexpected closure %v in instantiation manifest", fn)
	}
	instFuncs = append(instFuncs, fn)
}

// isInstantiatedType reports whether methods of typ (or of *typ)
// belong to an instantiated generic type.
func isInstantiatedType(typ *types.Type) bool {
	if typ.IsPtr() {
		typ = typ.Elem()
	}
	return typ.IsFullyInstantiated()
}

// localInstManifest returns the manifest for the local package.
//
// It is computed once, at the time export data is first written,
// and subsequent calls return the same manifest. In particular, the
// self-check in CheckInstManifest validates exactly the manifest that
// is written to export data.
func localInstManifest() *instManifest {
	if localInsts != nil {
		return localInsts
	}
	localInsts = new(instManifest)
	for _, fn := range instFuncs {
		if !fn.Dupok() {
			base.FatalfAt(fn.Pos(), "instantiation %v is not DUPOK", fn)
		}
		localInsts.add(fn.Linksym().Name)
	}
	return localInsts
}

// writeInstManifest writes the local package's manifest to w.
func writeInstManifest(w *pkgbits.Encoder) {
	m := localInstManifest()
	if base.Debug.ShareInst >= 2 {
		for _, name := range m.sorted() {
			fmt.Printf("shareinst: emit %s\n", name)
		}
	}
	m.write(w)
}

// readInstManifest reads the manifest of the imported package pr from
// r and adds its entries to importedInsts.
func readInstManifest(pr *pkgReader, r *pkgbits.Decoder) {
	pr.insts.read(r)
	importedInsts.merge(&pr.insts)
	if base.Debug.ShareInst >= 2 {
		fmt.Printf("shareinst: import %s: %d entries\n", pr.PkgPath(), pr.insts.len())
	}
}

// ImportedInstantiation reports whether a directly imported package's
// manifest lists the function with the given link symbol name, which
// means that package's object file defines it.
func ImportedInstantiation(name string) bool {
	return importedInsts.has(name)
}

// CheckInstManifest verifies that every entry of the local package's
// manifest names a DUPOK text symbol defined in this compilation
// unit. It must be called after all functions have been compiled.
//
// It is a no-op unless -d=shareinst=2 is set.
func CheckInstManifest() {
	if base.Debug.ShareInst < 2 {
		return
	}

	defined := make(map[*obj.LSym]bool, len(base.Ctxt.Text))
	for _, s := range base.Ctxt.Text {
		defined[s] = true
	}

	m := localInstManifest()
	for _, fn := range instFuncs {
		s := fn.Linksym()
		if !m.has(s.Name) {
			base.Fatalf("shareinst: %s missing from manifest", s.Name)
		}
		if !defined[s] {
			base.Fatalf("shareinst: manifest entry %s is not defined as a text symbol", s.Name)
		}
		if !s.DuplicateOK() {
			base.Fatalf("shareinst: manifest entry %s is not DUPOK", s.Name)
		}
	}
	if len(instFuncs) != m.len() {
		base.Fatalf("shareinst: manifest has %d entries, but %d instantiations were compiled", m.len(), len(instFuncs))
	}
}
