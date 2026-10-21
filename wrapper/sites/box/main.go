// box — Safeguard RDP-app wrapper for Box (account.box.com), native email + password login.
//
// Launcher:
//   --use-stdin --cmd "C:\OI_Custom\box.exe" --args "-incognito"
// Safeguard values used: username, password. Username is used as-is; if it has no '@' it is
// suffixed with -domain or {Target.AccountDomainName}.
// Selectors verified on: 2026-10-21 (see references/sites/box.md)
package main

import (
	"flag"
	"strings"

	"sgwrap/core"
)

func main() {
	fs := flag.NewFlagSet("box", flag.ContinueOnError)
	url := fs.String("url", "https://account.box.com/login", "start URL")
	domain := fs.String("domain", "", "email suffix if the account name has no @ (default: Target.AccountDomainName)")
	opts := core.StdFlags(fs, core.Options{Incognito: true})
	in := core.Load("box", fs)
	o := opts()

	user := in.Must("username")
	if !strings.Contains(user, "@") {
		d := *domain
		if d == "" {
			d = in.Get("Target.AccountDomainName")
		}
		if d != "" {
			user += "@" + d
		}
	}

	core.Run(o, *url,
		core.Type(`#login-email`, user),
		core.Click(`#login-submit`),
		core.WaitVisible(`#password-login`),
		core.Secret(`#password-login`, in.Must("password")),
		core.Click(`#login-submit-password`),
	)
}
