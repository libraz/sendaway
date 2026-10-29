package cli

import "runtime/debug"

// version reports Version, or the module version recorded by `go install`
// when no -ldflags value was set.
func version() string {
	module := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		module = bi.Main.Version
	}
	return resolveVersion(Version, module)
}

func resolveVersion(ldflags, module string) string {
	if ldflags != "dev" || module == "" || module == "(devel)" {
		return ldflags
	}
	return module
}
