package notification

import "strings"

// RenderTemplate performs simple {var} → vars[var] substitution. Variables
// not present in vars are left untouched so receivers can detect missing
// data instead of silently emitting empty strings.
//
// This is the v1 templating contract per CONTEXT D-21 — sophisticated
// templating (text/template, go-pongo, etc.) is deliberately out of scope.
func RenderTemplate(tpl string, vars map[string]string) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}
