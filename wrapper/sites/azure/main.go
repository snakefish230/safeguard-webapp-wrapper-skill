// azure — Safeguard RDP-app wrapper for the Azure portal (Entra ID cloud-only users).
//
// Launcher (>= 3.0.1):
//   --use-stdin --cmd "C:\SafeguardWrappers\azure.exe" --args "-incognito"
// Safeguard values: username, password, optional Target.TotpCodes, optional Target.AccountDomainName.
// Username: if the SPP account name has no '@', it is suffixed with -domain or {Target.AccountDomainName}
// or the asset network address (set the asset's NetworkAddress to the tenant, e.g. contoso.onmicrosoft.com).
//
// Flow verified 2026-10-14: login.microsoftonline.com username page (#i0116 name=loginfmt, Next #idSIButton9).
// Password/TOTP/KMSI selectors from One Identity's web_generic Azure sample with name-based fallbacks.
package main

import (
	"flag"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"sgwrap/core"
)

const (
	userSel = `input[name="loginfmt"]`
	passSel = `input[name="passwd"]`
	nextBtn = `#idSIButton9`
	otpSel  = `#idTxtBx_SAOTCC_OTC, input[name="otc"]`
	otpBtn  = `#idSubmit_SAOTCC_Continue, #idSIButton9`
	otpErr  = `#idSpan_SAOTCC_Error_OTC`
	kmsiNo  = `#idBtn_Back`
)

func main() {
	fs := flag.NewFlagSet("azure", flag.ContinueOnError)
	url := fs.String("url", "https://portal.azure.com", "start URL (e.g. https://portal.azure.com/#@contoso.onmicrosoft.com)")
	domain := fs.String("domain", "", "UPN suffix if the account name has no @ (default: Target.AccountDomainName, then asset address)")
	staySignedIn := fs.Bool("stay-signed-in", false, "answer Yes to 'Stay signed in?' (default No)")
	totpMin := fs.Int("totp-min-valid", 10, "min seconds a TOTP code must remain valid when picked")
	totpTries := fs.Int("totp-attempts", 2, "how many TOTP codes to try before giving up")
	opts := core.StdFlags(fs, core.Options{Incognito: true})
	in := core.Load("azure", fs)
	o := opts()

	user := in.Must("username")
	if !strings.Contains(user, "@") {
		d := *domain
		if d == "" {
			d = in.Get("Target.AccountDomainName")
		}
		if d == "" && strings.Contains(in.Get("asset"), ".") {
			d = in.Get("asset")
		}
		if d != "" {
			user = user + "@" + d
		}
	}
	pass := in.Must("password")

	steps := []chromedp.Action{
		core.Type(userSel, user),
		core.Click(nextBtn),
		// Some tenants show an account-type picker ("Work or school account").
		// Raced against the password field so tenants without one don't wait.
		core.ClickIfPresentUnless(`#aadTile, div[data-test-id="aadTile"]`, passSel, 10*time.Second),
		core.Secret(passSel, pass),
		core.Click(nextBtn),
		// #idBtn_Back is "Back" on the password page and "No" on "Stay signed in?".
		// Wait for the password field to go away so the later optional clicks
		// can't land on the password page and navigate backwards.
		core.WaitGone(passSel, 20*time.Second),
	}
	if t := in.Get("Target.TotpCodes"); t != "" {
		steps = append(steps,
			// "Sign in another way" is also on the verification-code page, so
			// only take that detour when the OTP box is not already showing.
			// Both are raced, so neither path waits on a fixed timeout.
			core.ClickIfPresentUnless(`#signInAnotherWay`, otpSel, 20*time.Second),
			core.ClickIfPresentUnless(`div[data-value="PhoneAppOTP"]`, otpSel, 10*time.Second),
			core.TOTPSubmit(otpSel, otpBtn, otpErr, t, *totpMin, *totpTries),
		)
	}
	if *staySignedIn {
		steps = append(steps, core.ClickIfPresent(`#idSIButton9`, 15*time.Second))
	} else {
		steps = append(steps, core.ClickIfPresent(kmsiNo, 15*time.Second))
	}
	core.Run(o, *url, steps...)
}
