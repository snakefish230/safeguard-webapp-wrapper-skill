// aws — Safeguard RDP-app wrapper for the AWS Management Console (IAM user or root).
//
// Launcher (recommended, Launcher >= 3.0.1):
//   --use-stdin --cmd "C:\SafeguardWrappers\aws.exe" --args "-set account=123456789012 -incognito"
// Root user:
//   --use-stdin --cmd "C:\SafeguardWrappers\aws.exe" --args "-root -incognito"
// Values used from Safeguard: username, password, optional Target.TotpCodes.
// The AWS account id/alias comes from -set account=..., or {Target.AssetNetworkAddress}
// if the SPP asset's network address is the account id/alias (use -account-from-asset).
//
// Selectors verified: see references/sites/aws.md (re-verify when AWS changes its sign-in page).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/chromedp"
	"sgwrap/core"
)

func main() {
	fs := flag.NewFlagSet("aws", flag.ContinueOnError)
	root := fs.Bool("root", false, "root user sign-in instead of IAM user")
	fromAsset := fs.Bool("account-from-asset", false, "use the Safeguard asset address as the AWS account id/alias")
	totpMin := fs.Int("totp-min-valid", 5, "min seconds a TOTP code must remain valid")
	opts := core.StdFlags(fs, core.Options{Incognito: true})
	in := core.Load("aws", fs)
	o := opts()

	user := in.Must("username")
	pass := in.Must("password")
	totp := in.Get("Target.TotpCodes")

	var url string
	var steps []chromedp.Action
	if *root {
		url = "https://signin.aws.amazon.com/console"
		steps = []chromedp.Action{
			core.ClickIfPresent("#root_user_radio_button", 5*time.Second),
			core.Type("#resolving_input", user),
			core.Click("#next_button"),
			core.FirstVisible(map[string]chromedp.Action{
				"#ap_password": chromedp.Tasks{core.Secret("#ap_password", pass), core.Click("#signInSubmit-input")},
				"#password":    chromedp.Tasks{core.Secret("#password", pass), core.Click("#signin_button")},
			}),
		}
		if totp != "" {
			steps = append(steps, core.FirstVisible(map[string]chromedp.Action{
				"#ap_tokenCode": chromedp.Tasks{core.TOTP("#ap_tokenCode", totp, *totpMin), core.Click("#signInSubmit-input")},
				"#mfacode":      chromedp.Tasks{core.TOTP("#mfacode", totp, *totpMin), core.Click("#submitMfa_button")},
			}))
		}
	} else {
		acct := in.Get("account")
		if *fromAsset || acct == "" {
			acct = in.Get("asset")
		}
		if acct == "" {
			core.Fatal(fmt.Errorf("AWS account id/alias missing: add -set account=<id> to --args"))
		}
		url = fmt.Sprintf("https://%s.signin.aws.amazon.com/console", acct)
		steps = []chromedp.Action{
			core.Type("#username", user),
			core.Secret("#password", pass),
			core.Click("#signin_button"),
		}
		if totp != "" {
			steps = append(steps, core.TOTP("#mfacode", totp, *totpMin), core.Click("#submitMfa_button"))
		}
	}
	core.Run(o, url, steps...)
	os.Exit(0)
}
