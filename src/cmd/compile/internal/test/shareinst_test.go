// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"internal/testenv"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Tests for sharing generic instantiations across packages through
// the instantiation manifests in export data (-d=shareinst; see
// cmd/compile/internal/noder/instmanifest.go and go.dev/issue/56718).
//
// The fixture is the import chain from go.dev/issue/70511:
//
//	foo  declares the generic type Foo[A, B]
//	bar  imports foo and instantiates Foo[Bar, int]
//	baz  imports bar only, and uses Foo[bar.Bar, int] through an alias
//	main imports baz only, and converts Foo[bar.Bar, int] to an interface
//
// Without sharing, bar, baz and main each compile their own copies of
// Foo[bar.Bar, int]'s methods and method wrappers. With sharing, only
// bar compiles them; baz skips them because bar's manifest lists them,
// and main skips them because baz's manifest forwards bar's entries.
// So baz compiles only its own function, and main only its own.

const shareInstModule = "example.com/shareinst"

var shareInstFiles = map[string]string{
	"go.mod": "module " + shareInstModule + "\n\ngo 1.24\n",
	"foo/foo.go": `package foo

type Foo[A, B any] struct{}

func (Foo[A, B]) FooFunc() {}

func (Foo[A, B]) Name() string { return "Foo" }

func (*Foo[A, B]) PtrFunc() int { return 1 }

// Pair has a value receiver and returns a second generic type.
func (Foo[A, B]) Pair(a A, b B) Pair[A, B] { return Pair[A, B]{a, b} }

type Pair[K, V any] struct {
	K K
	V V
}

func (p Pair[K, V]) Value() V { return p.V }
`,
	"bar/bar.go": shareInstBar,
	"baz/baz.go": `package baz

import "` + shareInstModule + `/bar"

// FooBar is foo.Foo[bar.Bar, int].
type FooBar = bar.FooOf[bar.Bar]

//go:noinline
func Run() string {
	var x any = bar.Bar{}
	_, ok := x.(bar.Bar)
	var f FooBar
	f.FooFunc()
	name := f.Name // method value: Foo[bar.Bar,int].Name-fm
	if !ok {
		return "fail"
	}
	return name()
}
`,
	"main.go": `package main

import "` + shareInstModule + `/baz"

type namer interface{ Name() string }

func main() {
	var x any = baz.FooBar{}
	println(baz.Run(), x.(namer).Name())
}
`,
}

const shareInstBar = `package bar

import "` + shareInstModule + `/foo"

type Bar struct{}

func (Bar) BarFooFunc(foo.Foo[Bar, int]) {}

// FooOf lets importers name instances of foo.Foo without importing foo.
type FooOf[T any] = foo.Foo[T, int]

//go:noinline
func Use() int {
	var f foo.Foo[Bar, int]
	f.FooFunc()
	name := f.Name // method value: Foo[Bar,int].Name-fm
	_ = name()
	return f.Pair(Bar{}, 42).Value()
}
`

// shareInstBarNoFoo is bar edited to no longer instantiate
// foo.Foo[Bar, int] at all.
const shareInstBarNoFoo = `package bar

import "` + shareInstModule + `/foo"

type Bar struct{}

// FooOf lets importers name instances of foo.Foo without importing foo.
type FooOf[T any] = foo.Foo[T, int]
`

const shareInstWantOutput = "Foo Foo\n"

// Symbol names used by the tests, with "M" standing for shareInstModule.
const (
	shapedFooFunc   = "M/foo.Foo[go.shape.struct {},go.shape.int].FooFunc"
	instanceFooFunc = "M/foo.Foo[M/bar.Bar,int].FooFunc"
)

// shareInstPlacement says, for each instantiated body of the fixture,
// what -d=shareinst=2 must report for it in each package: "emit"
// (compiled there) or "skip" (referenced by name, because an import's
// manifest lists it).
//
// Method wrappers ((*Foo[...]).M pointer wrappers, M-fm method value
// wrappers) are not listed, so as not to depend on which wrappers the
// compiler generates or how it names them. Instead the tests check that
// baz and main emit nothing at all from package foo, and that only bar
// defines text symbols of package foo.
var shareInstPlacement = []struct {
	sym          string
	bar, baz, mn string
}{
	// Shaped bodies.
	{sym: shapedFooFunc, bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.Foo[go.shape.struct {},go.shape.int].Name", bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.Foo[go.shape.struct {},go.shape.int].Pair", bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.(*Foo[go.shape.struct {},go.shape.int]).PtrFunc", bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.Pair[go.shape.struct {},go.shape.int].Value", bar: "emit", baz: "skip", mn: "skip"},

	// Instance wrappers (calling the shaped bodies with a dictionary).
	{sym: instanceFooFunc, bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.Foo[M/bar.Bar,int].Name", bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.Foo[M/bar.Bar,int].Pair", bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.(*Foo[M/bar.Bar,int]).PtrFunc", bar: "emit", baz: "skip", mn: "skip"},
	{sym: "M/foo.Pair[M/bar.Bar,int].Value", bar: "emit", baz: "skip", mn: "skip"},
}

// shareInstOwnText gives, for baz and main, the prefix of every text
// symbol the package may define: with sharing, each compiles only its
// own functions.
var shareInstOwnText = map[string]string{
	shareInstModule + "/baz": shareInstModule + "/baz.",
	shareInstModule:          "main.",
}

// noFooEmits reports an error for each instantiation from package foo
// that l says pkg compiled.
func noFooEmits(t *testing.T, pkg string, l *shareInstLog) {
	t.Helper()
	for _, sym := range slices.Sorted(maps.Keys(l.emit)) {
		if strings.HasPrefix(sym, shareInstModule+"/foo.") {
			t.Errorf("%s: emits %s, want it skipped", pkg, sym)
		}
	}
}

// shareInstSym expands "M" in sym to shareInstModule.
func shareInstSym(sym string) string {
	return strings.ReplaceAll(sym, "M/", shareInstModule+"/")
}

// writeShareInstModule writes the fixture to a new temporary directory
// and returns it.
func writeShareInstModule(t *testing.T) string {
	dir := t.TempDir()
	for name, src := range shareInstFiles {
		writeShareInstFile(t, dir, name, src)
	}
	return dir
}

func writeShareInstFile(t *testing.T, dir, name, src string) {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(file), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(src), 0666); err != nil {
		t.Fatal(err)
	}
}

// goShareInst runs the go command with args in the fixture directory dir
// and returns its combined output.
func goShareInst(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := testenv.Command(t, testenv.GoToolPath(t), args...)
	cmd.Dir = dir
	cmd = testenv.CleanCmdEnv(cmd)
	// Keep -work directories inside the test's temporary directory, and
	// don't let GOFLAGS from the environment change the build.
	cmd.Env = append(cmd.Env, "GOTMPDIR="+t.TempDir(), "GOFLAGS=")
	t.Log(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", cmd, err, out)
	}
	return out
}

// shareInstLog is the -d=shareinst=2 output for one package.
type shareInstLog struct {
	emit, skip map[string]bool
	imports    []string // packages whose manifests were read
}

var shareInstLine = regexp.MustCompile(`^shareinst: (emit|skip|import) (.*?)(?:: \d+ entries)?$`)

// parseShareInstOutput parses the output of a go build with
// -gcflags=all=-d=shareinst=2 into per-package logs, keyed by import path.
// cmd/go prints each package's compiler output after a "# importpath" line.
func parseShareInstOutput(t *testing.T, out []byte) map[string]*shareInstLog {
	logs := make(map[string]*shareInstLog)
	var cur *shareInstLog
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if pkg, ok := strings.CutPrefix(line, "# "); ok {
			cur = &shareInstLog{emit: map[string]bool{}, skip: map[string]bool{}}
			logs[pkg] = cur
			continue
		}
		m := shareInstLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if cur == nil {
			t.Fatalf("shareinst output before any package header: %q", line)
		}
		switch m[1] {
		case "emit":
			cur.emit[m[2]] = true
		case "skip":
			cur.skip[m[2]] = true
		case "import":
			cur.imports = append(cur.imports, m[2])
		}
	}
	return logs
}

// state returns "emit", "skip" or "" for sym in l.
func (l *shareInstLog) state(t *testing.T, sym string) string {
	switch {
	case l.emit[sym] && l.skip[sym]:
		t.Errorf("%s both emitted and skipped", sym)
		return "emit+skip"
	case l.emit[sym]:
		return "emit"
	case l.skip[sym]:
		return "skip"
	}
	return ""
}

func (l *shareInstLog) String() string {
	var b strings.Builder
	for _, sym := range slices.Sorted(maps.Keys(l.emit)) {
		b.WriteString("\temit " + sym + "\n")
	}
	for _, sym := range slices.Sorted(maps.Keys(l.skip)) {
		b.WriteString("\tskip " + sym + "\n")
	}
	return b.String()
}

// shareInstLogs returns the logs of bar, baz and main from out.
func shareInstLogs(t *testing.T, out []byte) (bar, baz, mn *shareInstLog) {
	t.Helper()
	logs := parseShareInstOutput(t, out)
	get := func(pkg string) *shareInstLog {
		l := logs[pkg]
		if l == nil {
			t.Fatalf("no compiler output for %s in:\n%s", pkg, out)
		}
		return l
	}
	return get(shareInstModule + "/bar"), get(shareInstModule + "/baz"), get(shareInstModule)
}

// buildShareInstArchives runs go build -a -work with the extra args in
// dir and returns the build output and the path of each module package's
// full archive (with object code, unlike the export data that go list
// -export reports), keyed by import path.
func buildShareInstArchives(t *testing.T, dir string, args ...string) ([]byte, map[string]string) {
	t.Helper()
	graph := filepath.Join(t.TempDir(), "actiongraph.json")
	args = append([]string{"build", "-a", "-work", "-debug-actiongraph=" + graph}, args...)
	out := goShareInst(t, dir, args...)
	data, err := os.ReadFile(graph)
	if err != nil {
		t.Fatal(err)
	}
	var actions []struct {
		Mode    string
		Package string
		Objdir  string
	}
	if err := json.Unmarshal(data, &actions); err != nil {
		t.Fatalf("parsing action graph: %v", err)
	}
	archives := make(map[string]string)
	for _, a := range actions {
		if a.Mode != "build" || a.Objdir == "" || !strings.HasPrefix(a.Package, shareInstModule) {
			continue
		}
		f := filepath.Join(a.Objdir, "_pkg_.a")
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("archive of %s: %v", a.Package, err)
		}
		archives[a.Package] = f
	}
	for _, pkg := range []string{"/foo", "/bar", "/baz", ""} {
		if archives[shareInstModule+pkg] == "" {
			t.Fatalf("no archive for %s in action graph", shareInstModule+pkg)
		}
	}
	return out, archives
}

// nmSymbols returns the nm symbol code (T, U, ...) of each symbol in
// archive.
func nmSymbols(t *testing.T, archive string) map[string]string {
	t.Helper()
	cmd := testenv.Command(t, testenv.GoToolPath(t), "tool", "nm", archive)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", cmd, err, out)
	}
	syms := make(map[string]string)
	// Lines are "addr code name", with an empty address for undefined
	// symbols. Names may contain spaces (go.shape.struct {}).
	line := regexp.MustCompile(`^\s*[0-9a-f]*\s+([A-Za-z?])\s+(.+)$`)
	for _, l := range strings.Split(string(out), "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		syms[m[2]] = m[1]
	}
	return syms
}

func runShareInst(t *testing.T, exe string) {
	t.Helper()
	out, err := testenv.Command(t, exe).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", exe, err, out)
	}
	if string(out) != shareInstWantOutput {
		t.Errorf("%s printed %q, want %q", exe, out, shareInstWantOutput)
	}
}

// TestShareInstPlacement checks that each instantiation in the
// go.dev/issue/70511 import chain is compiled exactly once, in bar,
// and that the program still links and runs.
func TestShareInstPlacement(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	t.Parallel()

	dir := writeShareInstModule(t)
	exe := filepath.Join(dir, "main.exe")
	out, archives := buildShareInstArchives(t, dir, "-gcflags=all=-d=shareinst=2", "-o", exe, ".")
	bar, baz, mn := shareInstLogs(t, out)

	// main reads only baz's manifest; it skips bar's instantiations
	// because baz's manifest includes the entries baz skipped.
	if want := []string{shareInstModule + "/baz"}; !slices.Equal(mn.imports, want) {
		t.Errorf("main read manifests of %q, want %q", mn.imports, want)
	}

	for _, row := range shareInstPlacement {
		sym := shareInstSym(row.sym)
		for _, c := range []struct {
			pkg  string
			log  *shareInstLog
			want string
		}{
			{"bar", bar, row.bar},
			{"baz", baz, row.baz},
			{"main", mn, row.mn},
		} {
			if got := c.log.state(t, sym); got != c.want {
				t.Errorf("%s: %s is %q, want %q", c.pkg, sym, got, c.want)
			}
		}
	}
	// baz and main must not compile anything from foo: not the bodies
	// above, and not the method wrappers for Foo[bar.Bar, int] either.
	noFooEmits(t, "baz", baz)
	noFooEmits(t, "main", mn)
	if t.Failed() {
		t.Logf("bar:\n%sbaz:\n%smain:\n%s", bar, baz, mn)
	}

	// Check the object files.
	syms := make(map[string]map[string]string)
	for pkg, archive := range archives {
		syms[pkg] = nmSymbols(t, archive)
	}
	pkgs := slices.Sorted(maps.Keys(syms))
	// Every text symbol of package foo, wrappers included, is defined
	// in bar and nowhere else.
	fooText := 0
	for _, pkg := range pkgs {
		for _, sym := range slices.Sorted(maps.Keys(syms[pkg])) {
			if syms[pkg][sym] != "T" || !strings.HasPrefix(sym, shareInstModule+"/foo.") {
				continue
			}
			if pkg == shareInstModule+"/bar" {
				fooText++
			} else {
				t.Errorf("%s defines %s, want it only in bar", pkg, sym)
			}
		}
	}
	for _, row := range shareInstPlacement {
		if sym := shareInstSym(row.sym); syms[shareInstModule+"/bar"][sym] != "T" {
			t.Errorf("bar does not define %s", sym)
		}
	}
	if fooText <= len(shareInstPlacement) {
		t.Errorf("bar defines %d text symbols of foo, want more than the %d bodies (method wrappers too)", fooText, len(shareInstPlacement))
	}
	// baz and main compile only their own functions.
	for pkg, prefix := range shareInstOwnText {
		for _, sym := range slices.Sorted(maps.Keys(syms[pkg])) {
			if syms[pkg][sym] == "T" && !strings.HasPrefix(sym, prefix) {
				t.Errorf("%s defines %s, want only %s* text symbols", pkg, sym, prefix)
			}
		}
	}
	// The instance wrapper is in the method tables of the type
	// descriptors baz and main emit, so they must reference it.
	inst := shareInstSym(instanceFooFunc)
	for _, pkg := range []string{shareInstModule + "/baz", shareInstModule} {
		if got := syms[pkg][inst]; got != "U" {
			t.Errorf("%s: nm code of %s is %q, want U", pkg, inst, got)
		}
	}

	t.Run("Runs", func(t *testing.T) {
		runShareInst(t, exe)
	})
}

// TestShareInstDisabled checks that -d=shareinst=0 makes every package
// compile its own copy of each instantiation it needs, as before
// manifests existed.
func TestShareInstDisabled(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	t.Parallel()

	dir := writeShareInstModule(t)
	exe := filepath.Join(dir, "main.exe")
	out, archives := buildShareInstArchives(t, dir, "-gcflags=all=-d=shareinst=0", "-o", exe, ".")
	if bytes.Contains(out, []byte("shareinst: skip")) {
		t.Errorf("-d=shareinst=0 build skipped instantiations:\n%s", out)
	}
	for _, sym := range []string{shapedFooFunc, instanceFooFunc} {
		sym = shareInstSym(sym)
		for _, pkg := range []string{"/bar", "/baz", ""} {
			pkg = shareInstModule + pkg
			if got := nmSymbols(t, archives[pkg])[sym]; got != "T" {
				t.Errorf("%s: nm code of %s is %q, want T", pkg, sym, got)
			}
		}
	}
	runShareInst(t, exe)
}

// TestShareInstCascade checks that manifests cannot go stale: when bar
// stops instantiating Foo[bar.Bar, int], an incremental build must
// recompile baz, which then compiles the instantiations itself, and the
// program must still link.
func TestShareInstCascade(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	t.Parallel()

	dir := writeShareInstModule(t)
	exe := filepath.Join(dir, "main.exe")
	build := func() (bar, baz, mn *shareInstLog) {
		t.Helper()
		out := goShareInst(t, dir, "build", "-gcflags=all=-d=shareinst=2", "-o", exe, ".")
		return shareInstLogs(t, out)
	}

	// Normal (non -a) build, populating the build cache.
	bar, baz, mn := build()
	for _, sym := range []string{shapedFooFunc, instanceFooFunc} {
		sym = shareInstSym(sym)
		if got := [3]string{bar.state(t, sym), baz.state(t, sym), mn.state(t, sym)}; got != [3]string{"emit", "skip", "skip"} {
			t.Errorf("before edit: %s in bar, baz, main is %q, want emit, skip, skip", sym, got)
		}
	}
	noFooEmits(t, "baz before edit", baz)
	noFooEmits(t, "main before edit", mn)
	runShareInst(t, exe)

	// Edit bar so that it no longer instantiates Foo[bar.Bar, int],
	// and rebuild incrementally.
	writeShareInstFile(t, dir, "bar/bar.go", shareInstBarNoFoo)
	bar, baz, mn = build()
	for _, sym := range []string{shapedFooFunc, instanceFooFunc} {
		sym = shareInstSym(sym)
		if got := [3]string{bar.state(t, sym), baz.state(t, sym), mn.state(t, sym)}; got != [3]string{"", "emit", "skip"} {
			t.Errorf("after edit: %s in bar, baz, main is %q, want \"\", emit, skip", sym, got)
		}
	}
	noFooEmits(t, "bar after edit", bar)
	// main still compiles nothing from foo: baz now defines it all.
	noFooEmits(t, "main after edit", mn)
	if t.Failed() {
		t.Logf("bar:\n%sbaz:\n%smain:\n%s", bar, baz, mn)
	}
	runShareInst(t, exe)
}
