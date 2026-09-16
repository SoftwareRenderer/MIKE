//go:build !release

package startup

// frontendDirAllowed is the only difference between a developer's build and a `-tags release` build:
// whether this process may read the built SPA off disk. A binary someone downloads must serve exactly
// what it was compiled with, so this is a property of the build rather than of the environment.
const frontendDirAllowed = true
