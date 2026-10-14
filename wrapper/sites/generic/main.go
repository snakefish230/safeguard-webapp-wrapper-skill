// generic — config-free generic web login wrapper.
//
//   --use-stdin --cmd "C:\SafeguardWrappers\generic.exe" --args
//     "-url https://{asset}/login -user-sel #username -pass-sel #password -submit-sel button[type=submit] [-next-sel #next] [-otp-sel #otp -otp-submit-sel #verify] [-insecure]"
//
// -next-sel supports two-page logins (username → Next → password).
package main

import (
	"flag"

	"github.com/chromedp/chromedp"
	"sgwrap/core"
)

func main() {
	fs := flag.NewFlagSet("generic", flag.ContinueOnError)
	url := fs.String("url", "", "login URL; {placeholders} expanded from Safeguard values")
	userFmt := fs.String("user-format", "{username}", "username template, e.g. {username}@{Target.AccountDomainName}")
	userSel := fs.String("user-sel", "", "CSS selector: username field")
	nextSel := fs.String("next-sel", "", "CSS selector: 'Next' button between username and password (optional)")
	passSel := fs.String("pass-sel", "", "CSS selector: password field")
	submitSel := fs.String("submit-sel", "", "CSS selector: submit button (empty = press Enter)")
	otpSel := fs.String("otp-sel", "", "CSS selector: TOTP field (optional; needs Target.TotpCodes)")
	otpSubmit := fs.String("otp-submit-sel", "", "CSS selector: TOTP submit button")
	successSel := fs.String("success-sel", "", "CSS selector visible after successful login (optional; confirms + logs success)")
	opts := core.StdFlags(fs, core.Options{Incognito: true})
	in := core.Load("generic", fs)
	o := opts()

	if *url == "" || *userSel == "" || *passSel == "" {
		core.Fatal(errMissing)
	}
	steps := []chromedp.Action{core.Type(*userSel, in.Expand(*userFmt))}
	if *nextSel != "" {
		steps = append(steps, core.Click(*nextSel))
	}
	steps = append(steps, core.Secret(*passSel, in.Must("password")))
	if *submitSel != "" {
		steps = append(steps, core.Click(*submitSel))
	} else {
		steps = append(steps, core.Enter(*passSel))
	}
	if *otpSel != "" && in.Get("Target.TotpCodes") != "" {
		steps = append(steps, core.TOTP(*otpSel, in.Get("Target.TotpCodes"), 5))
		if *otpSubmit != "" {
			steps = append(steps, core.Click(*otpSubmit))
		} else {
			steps = append(steps, core.Enter(*otpSel))
		}
	}
	if *successSel != "" {
		steps = append(steps, core.WaitVisible(*successSel))
	}
	core.Run(o, in.Expand(*url), steps...)
}

type e string

func (x e) Error() string { return string(x) }

const errMissing = e("-url, -user-sel and -pass-sel are required")
