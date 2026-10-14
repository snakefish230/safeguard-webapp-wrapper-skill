//go:build ignore

// SITE_NAME — Safeguard RDP-app wrapper for SITE_DESCRIPTION.
//
// Launcher:
//   --use-stdin --cmd "C:\SafeguardWrappers\SITE_NAME.exe" --args "-incognito"
// Safeguard values used: username, password[, asset][, Target.TotpCodes]
// Selectors verified on: YYYY-MM-DD (see references/sites/SITE_NAME.md)
//
// To use: copy this dir to sites/<name>/, delete the //go:build ignore line,
// replace placeholders, then: scripts/build.sh <name>
package main

import (
	"flag"

	"github.com/chromedp/chromedp"
	"sgwrap/core"
)

func main() {
	fs := flag.NewFlagSet("SITE_NAME", flag.ContinueOnError)
	// Site-specific non-secret flags go here, e.g.:
	// tenant := fs.String("tenant", "", "tenant subdomain")
	opts := core.StdFlags(fs, core.Options{Incognito: true})
	in := core.Load("SITE_NAME", fs)
	o := opts()

	url := in.Expand("https://{asset}/login") // LOGIN_URL
	steps := []chromedp.Action{
		// core.ClickIfPresent("#cookie-accept", 3*time.Second),
		core.Type("USER_SELECTOR", in.Must("username")),
		// core.Click("NEXT_SELECTOR"),            // two-step logins
		core.Secret("PASS_SELECTOR", in.Must("password")),
		core.Click("SUBMIT_SELECTOR"),
		// if t := in.Get("Target.TotpCodes"); t != "" { steps = append(steps, core.TOTP("OTP_SEL", t, 5), core.Click("OTP_SUBMIT")) }
	}
	core.Run(o, url, steps...)
}
