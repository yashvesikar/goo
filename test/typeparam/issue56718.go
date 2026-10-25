// rundir -d=shareinst=2

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Instantiations used by several packages of an import chain are
// compiled only by the first package that needs them; the others
// refer to them by name. -d=shareinst=2 makes the compiler check
// its instantiation manifests.

package ignored
