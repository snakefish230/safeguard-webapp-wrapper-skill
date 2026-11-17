// aduc — opens Active Directory Users and Computers (or any .msc) as the account
// released by Safeguard, the same way `runas /netonly` does.
//
//   --use-stdin --cmd "C:\OneIdentity\aduc.exe" --args "[-server {asset}] [-domain corp.example.com]"
//
// The console runs under the RDS user's local token; only network authentication
// (LDAP/Kerberos/NTLM to the DCs) uses the vaulted credential. The password goes
// straight from the Launcher's stdin pipe to CreateProcessWithLogonW and never
// appears on a command line. The wrapper waits for the console to close so the
// RemoteApp session (and the SPS recording) lasts exactly as long as ADUC.
//
// Not a browser wrapper: none of the core browser flags apply.
package main

import (
	"errors"
	"flag"
	"log/slog"
	"os"
	"strings"

	"sgwrap/core"
)

var version = "dev"

func main() {
	fs := flag.NewFlagSet("aduc", flag.ContinueOnError)
	msc := fs.String("msc", "dsa.msc", `snap-in console to open (bare names resolve to %SystemRoot%\System32)`)
	server := fs.String("server", "", "DC for /server= ({placeholders} expanded, e.g. {asset}); empty = let ADUC pick")
	domain := fs.String("domain", "", "domain for /domain= ({placeholders} expanded); empty = let ADUC pick")
	in := core.Load("aduc", fs)
	slog.Info("aduc", "version", version)

	user, dom, err := identity(in.Must("username"), in.Get("Target.AccountDomainName"), in.Get("logon_domain"))
	if err != nil {
		core.Fatal(err)
	}
	cmd := commandLine(mmcPath(), mscPath(*msc), in.Expand(*server), in.Expand(*domain))
	slog.Info("launching", "cmdline", cmd, "logon_domain_set", dom != "")

	code, err := runNetOnly(user, dom, in.Must("password"), cmd, !in.Debug)
	if err != nil {
		core.Fatal(err)
	}
	slog.Info("console closed", "exit", code)
}

// identity splits the vaulted account into the user/domain pair CreateProcessWithLogonW
// wants. DOMAIN\user and user@domain in the username win; otherwise the domain is
// -set logon_domain=... or Safeguard's Target.AccountDomainName.
func identity(username, accountDomain, override string) (user, domain string, err error) {
	if i := strings.IndexByte(username, '\\'); i > 0 {
		return username[i+1:], username[:i], nil
	}
	if strings.Contains(username, "@") {
		return username, "", nil // UPN: lpDomain must be NULL
	}
	if domain = strings.TrimSpace(override); domain == "" {
		domain = strings.TrimSpace(accountDomain)
	}
	if domain == "" {
		return "", "", errors.New("no domain for the account: use a directory (AD) account in Safeguard or add -set logon_domain=CORP to --args")
	}
	return username, domain, nil
}

func commandLine(mmc, msc, server, domain string) string {
	parts := []string{quote(mmc), quote(msc)}
	if domain != "" {
		parts = append(parts, quote("/domain="+domain))
	}
	if server != "" {
		parts = append(parts, quote("/server="+server))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, ``) + `"`
	}
	return s
}

func system32() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\System32`
}

func mmcPath() string { return system32() + `\mmc.exe` }

func mscPath(m string) string {
	if strings.ContainsAny(m, `\/:`) {
		return m
	}
	return system32() + `\` + m
}
