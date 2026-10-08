//go:build !production || devtools

package bindings

// devBuild is true in the builds that Wails gives developer tools.
func init() { devBuild = true }
