# GOO: Go Optimized

GOO ("Go Optimized") is a fork of the Go toolchain at
https://github.com/yashvesikar/goo for prototyping compiler and build
performance work that upstream has not landed yet. Changes are developed on
topic branches and proposed back to upstream when they are ready.

Current work:

- **Sharing generic instantiations across packages** (upstream issues
  56718 and 70511): each package records in its export data which generic
  instantiations it compiled, and importing packages reference those instead
  of compiling their own copies. Merged from branch `shared-generic-inst`.

Branches: `master` is a pure mirror of upstream Go and is never committed to
directly; `goo` (the default branch) is upstream plus GOO's own changes. A
scheduled workflow fast-forwards `master` and rebases `goo` onto it daily.

The rest of this file is the upstream Go README.

---

# The Go Programming Language

Go is an open source programming language that makes it easy to build simple,
reliable, and efficient software.

![Gopher image](https://golang.org/doc/gopher/fiveyears.jpg)
*Gopher image by [Renee French][rf], licensed under [Creative Commons 4.0 Attribution license][cc4-by].*

Our canonical Git repository is located at https://go.googlesource.com/go.
There is a mirror of the repository at https://github.com/golang/go.

Unless otherwise noted, the Go source files are distributed under the
BSD-style license found in the LICENSE file.

### Download and Install

#### Binary Distributions

Official binary distributions are available at https://go.dev/dl/.

After downloading a binary release, visit https://go.dev/doc/install
for installation instructions.

#### Install From Source

If a binary distribution is not available for your combination of
operating system and architecture, visit
https://go.dev/doc/install/source
for source installation instructions.

### Contributing

Go is the work of thousands of contributors. We appreciate your help!

To contribute, please read the contribution guidelines at https://go.dev/doc/contribute.

Note that the Go project uses the issue tracker for bug reports and
proposals only. See https://go.dev/wiki/Questions for a list of
places to ask questions about the Go language.

[rf]: https://reneefrench.blogspot.com/
[cc4-by]: https://creativecommons.org/licenses/by/4.0/
