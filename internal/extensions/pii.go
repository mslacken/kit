package extensions

import (
	"reflect"

	filterusername "github.com/openSUSE/piiplugin/filter/username"
	"github.com/openSUSE/piiplugin/piiplugin"
	"github.com/traefik/yaegi/interp"
)

// PiiSymbols exposes the openSUSE/piiplugin API to extensions via a Yaegi
// symbol table. Extensions import it as:
//
//	import pii "kit/pii"
//
// The export key is "kit/pii/pii" because Yaegi's Use() treats an export key
// as <importPath>/<packageName> (see Symbols() which keys "kit/ext/ext" to
// serve import "kit/ext"); the doubled base is required for the import to
// resolve.
//
// Exposed symbols:
//
//	pii.NewPiiFilter(opts ...PiiPluginOption) *PiiFilter
//	pii.WithoutEmail(), pii.WithoutUsername(), pii.WithoutHost()
//	pii.WithUsernameSource(source)
//	pii.SourceGetent / pii.SourceCgo / pii.SourceAuto
//	(*PiiFilter).Redact(text, fullInput string) string
//	(*PiiFilter).Unredact(text string) string
//	*PiiFilter.Replacements (shared replacement table)
//
// PiiFilter is registered with the nil-pointer trick so its pointer-receiver
// methods are reachable from interpretation. The functional-option pattern
// (NewPiiFilter ...PiiPluginOption) is safe here because it calls a host
// function that already carries a concrete interface; no runtime interface
// synthesis (genInterfaceWrapper) is involved.
//
// Configurer and PiiPluginOption are deliberately NOT exposed: interpreting
// code producing values of a host interface type is the documented danger
// zone, and the Without*/WithUsernameSource helpers make them unnecessary.
//
// Kept in its own file so the stdlib and kit/ext symbol tables remain stable
// and so that removing the PII dependency is a one-file deletion.
func PiiSymbols() interp.Exports {
	return interp.Exports{
		"kit/pii/pii": {
			"PiiFilter":          reflect.ValueOf((*piiplugin.PiiFilter)(nil)),
			"NewPiiFilter":       reflect.ValueOf(piiplugin.NewPiiFilter),
			"WithoutEmail":       reflect.ValueOf(piiplugin.WithoutEmail),
			"WithoutUsername":    reflect.ValueOf(piiplugin.WithoutUsername),
			"WithoutHost":        reflect.ValueOf(piiplugin.WithoutHost),
			"WithUsernameSource": reflect.ValueOf(piiplugin.WithUsernameSource),
			"SourceGetent":       reflect.ValueOf(filterusername.SourceGetent),
			"SourceCgo":          reflect.ValueOf(filterusername.SourceCgo),
			"SourceAuto":         reflect.ValueOf(filterusername.SourceAuto),
		},
	}
}
