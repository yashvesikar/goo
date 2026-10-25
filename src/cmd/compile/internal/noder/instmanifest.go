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
//	name of a DUPOK text function that is defined either in this
//	package's object file or in the object file of a package in its
//	import closure; a program that links this package therefore
//	always has a definition of it.
//
// Consequently, a package that imports it may refer to any listed
// function by name instead of compiling its own copy (the backend
// skips it; see SkippedInstantiation), and the linker will resolve
// the reference.
//
// A package's manifest is the union of the instantiations it compiled
// itself and those it skipped. The contract holds by induction over
// the import graph: a compiled entry is defined in this package's
// object (checked at -d=shareinst=2), and a skipped entry was taken
// from the manifest of a direct import, whose own contract places its
// definition in that import's import closure, which is part of ours.
// The manifest cannot go stale: cmd/go hashes the export data of a
// package's direct imports into its action ID, so a change to what an
// import's closure defines changes that import's manifest and
// recompiles this package.
//
// Because skipped functions still go through the whole frontend, and
// the manifest includes them, a package's export data is the same
// whether or not sharing is enabled. This matters for plugins:
// plugin.Open compares package fingerprints (export data hashes)
// between a host built with sharing and a plugin built without it.
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

// skippedInsts lists the names of the instantiations that will not be
// compiled because a directly imported package's manifest lists them.
// They are referenced by name and resolved by the linker against the
// definition in the import closure.
var skippedInsts instManifest

// skippedFuncs is the set of functions in skippedInsts. They are in
// typecheck.Target.Funcs, so the frontend processes them as usual,
// but the backend must not compile them.
var skippedFuncs = make(map[*ir.Func]bool)

// shareInstEnabled reports whether this compilation may skip
// instantiations that imported packages' manifests list.
//
// Manifests are always written, but they are not consulted when
// dynamically linking against other Go modules, where a symbol defined
// in an imported package's object may end up in a different module.
// -dynlink is what cmd/go passes for -linkshared, -buildmode=shared
// and -buildmode=plugin.
//
// -shared alone is position-independent code for a single module
// (-buildmode=pie, the default on darwin/arm64, or c-shared/c-archive)
// and does not prevent sharing. (-dynlink also sets Ctxt.Flag_shared,
// so Flag_shared itself must not be tested here.)
func shareInstEnabled() bool {
	return base.Debug.ShareInst > 0 && !base.Ctxt.Flag_dynlink && !base.Ctxt.Flag_linkshared
}

// skipInstFunc records that fn, a generic function instantiation
// listed in an imported package's manifest, has been added to
// typecheck.Target.Funcs but must not be compiled.
func skipInstFunc(fn *ir.Func) {
	if fn.OClosure != nil {
		base.FatalfAt(fn.Pos(), "unexpected closure %v in instantiation manifest", fn)
	}
	name := fn.Linksym().Name
	skippedInsts.add(name)
	skippedFuncs[fn] = true
	if base.Debug.ShareInst >= 2 {
		fmt.Printf("shareinst: skip %s\n", name)
	}
}

// SkippedInstantiation reports whether fn is an instantiation that
// an imported package already compiled, so the backend must not
// compile it. Closures within it are compiled only along with it, so
// they are skipped too.
func SkippedInstantiation(fn *ir.Func) bool {
	return skippedFuncs[fn]
}

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

// addOrSkipInstWrapper records fn, a method wrapper for an
// instantiated type that finishWrapperFunc has added to
// typecheck.Target.Funcs. Like an instantiation in readBodies, it is
// skipped (left to the frontend only) if a directly imported
// package's manifest lists it, and compiled otherwise.
//
// Type descriptors and itabs refer to wrappers by symbol; for a
// skipped wrapper those are by-name references to the DUPOK
// definition in the import closure. shareInstEnabled is false under
// -linkshared, where such cross-DSO method references are not
// supported (go.dev/issue/58966).
func addOrSkipInstWrapper(fn *ir.Func) {
	if shareInstEnabled() && ImportedInstantiation(fn.Linksym().Name) {
		skipInstFunc(fn)
	} else {
		addInstFunc(fn)
	}
}

// isInstantiatedType reports whether methods of typ (or of *typ)
// belong to an instantiated generic type.
func isInstantiatedType(typ *types.Type) bool {
	if typ.IsPtr() {
		typ = typ.Elem()
	}
	return typ.IsFullyInstantiated()
}

// localInstManifest returns the manifest for the local package: the
// union of the instantiations compiled locally (instFuncs) and those
// skipped (skippedInsts).
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
	localInsts.merge(&skippedInsts)
	return localInsts
}

// writeInstManifest writes the local package's manifest to w.
func writeInstManifest(w *pkgbits.Encoder) {
	m := localInstManifest()
	if base.Debug.ShareInst >= 2 {
		for _, name := range m.sorted() {
			if !skippedInsts.has(name) {
				fmt.Printf("shareinst: emit %s\n", name)
			}
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

// CheckInstManifest verifies the local package's manifest against
// what this compilation unit actually defined:
//
//   - every locally compiled entry is a DUPOK text symbol defined
//     in this object;
//   - no skipped entry is defined in this object;
//   - the compiled and skipped sets are disjoint, and the manifest is
//     exactly their union.
//
// It must be called after all functions have been compiled.
//
// It is a no-op unless -d=shareinst=2 is set.
func CheckInstManifest() {
	if base.Debug.ShareInst < 2 {
		return
	}

	defined := make(map[string]*obj.LSym, len(base.Ctxt.Text))
	for _, s := range base.Ctxt.Text {
		defined[s.Name] = s
	}

	m := localInstManifest()
	emitted := make(map[string]bool, len(instFuncs))
	for _, fn := range instFuncs {
		s := fn.Linksym()
		emitted[s.Name] = true
		if !m.has(s.Name) {
			base.Fatalf("shareinst: %s missing from manifest", s.Name)
		}
		if defined[s.Name] != s {
			base.Fatalf("shareinst: manifest entry %s is not defined as a text symbol", s.Name)
		}
		if !s.DuplicateOK() {
			base.Fatalf("shareinst: manifest entry %s is not DUPOK", s.Name)
		}
		if skippedInsts.has(s.Name) {
			base.Fatalf("shareinst: manifest entry %s is both compiled and skipped", s.Name)
		}
	}
	for fn := range skippedFuncs {
		name := fn.Linksym().Name
		if !m.has(name) {
			base.Fatalf("shareinst: skipped %s missing from manifest", name)
		}
		if defined[name] != nil {
			base.Fatalf("shareinst: skipped instantiation %s was compiled", name)
		}
	}
	if want := len(emitted) + skippedInsts.len(); m.len() != want || len(emitted) != len(instFuncs) || len(skippedFuncs) != skippedInsts.len() {
		base.Fatalf("shareinst: manifest has %d entries, but %d instantiations were compiled and %d skipped", m.len(), len(instFuncs), skippedInsts.len())
	}
}
