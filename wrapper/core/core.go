// Package core is the shared runtime for Safeguard RDP-application web wrappers.
//
// A wrapper is a small Windows executable that the One Identity Safeguard
// RemoteApp Launcher (OI-SG-RemoteApp-Launcher.exe) starts on the RDS jump host.
// The wrapper launches Chrome/Edge via the DevTools protocol (chromedp), opens
// the target login URL, injects the credential released by Safeguard, and then
// stays alive until the user closes the browser — so the RDP app session (and
// the SPS recording) ends when the browser closes.
//
// Credential delivery (preferred → fallback):
//  1. --use-stdin (Launcher >= 3.0.1): the Launcher writes ONE JSON line on
//     stdin with username, password, asset, Target.* keys and "cli_args" (the
//     --args string). Nothing secret appears on the process command line.
//  2. Legacy flags: -username/-password/-asset on the command line. Works with
//     older launchers but exposes secrets in the process list. Avoid.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// Input is everything the wrapper knows about the session.
type Input struct {
	Values map[string]string // username, password, asset, Target.*, plus -set extras
	Args   []string          // positional (non-flag) args left after parsing
	Debug  bool
}

// Get returns a value by key ("" if absent). Keys are case-insensitive.
func (in *Input) Get(key string) string {
	if v, ok := in.Values[key]; ok {
		return v
	}
	for k, v := range in.Values {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// Must returns a value or exits with a clear (non-secret) error.
func (in *Input) Must(key string) string {
	v := in.Get(key)
	if v == "" {
		Fatal(fmt.Errorf("required value %q was not provided (check Launcher --args / -set)", key))
	}
	return v
}

// Expand replaces {key} placeholders, e.g. "https://{asset}/login" or
// "{username}@{Target.AccountDomainName}".
func (in *Input) Expand(tmpl string) string {
	var b strings.Builder
	for {
		i := strings.Index(tmpl, "{")
		if i < 0 {
			break
		}
		j := strings.Index(tmpl[i:], "}")
		if j < 0 {
			break
		}
		b.WriteString(tmpl[:i])
		b.WriteString(in.Get(tmpl[i+1 : i+j]))
		tmpl = tmpl[i+j+1:]
	}
	b.WriteString(tmpl)
	return b.String()
}

// Options controls browser launch.
type Options struct {
	Browser     string // "chrome" (default) or "edge"
	Incognito   bool   // incognito / InPrivate (recommended: no cached sessions between users)
	Kiosk       bool
	Insecure    bool   // ignore TLS errors (lab appliances with self-signed certs)
	UserDataDir string // optional persistent profile; %AppData% is expanded
	WindowSize  string // default 1280,800
	StepTimeout time.Duration
	// NoWait: exit right after the login sequence instead of waiting for the
	// browser to close. Leave false so the RDP app session lasts as long as the browser.
	NoWait bool
}

// StdFlags registers the common browser flags on fs and returns a func that
// builds Options after parsing. Sites call this so every wrapper behaves alike.
func StdFlags(fs *flag.FlagSet, def Options) func() Options {
	browser := fs.String("browser", orStr(def.Browser, "chrome"), "chrome|edge")
	edge := fs.Bool("edge", false, "shortcut for -browser edge")
	inc := fs.Bool("incognito", def.Incognito, "incognito/InPrivate mode")
	kiosk := fs.Bool("kiosk", def.Kiosk, "kiosk (full-screen, no address bar)")
	insecure := fs.Bool("insecure", def.Insecure, "ignore TLS certificate errors")
	udd := fs.String("user-data-dir", def.UserDataDir, "browser profile dir")
	timeout := fs.Duration("step-timeout", orDur(def.StepTimeout, 90*time.Second), "per-step timeout")
	noWait := fs.Bool("no-wait", def.NoWait, "exit after login instead of waiting for browser close")
	return func() Options {
		o := Options{Browser: *browser, Incognito: *inc, Kiosk: *kiosk, Insecure: *insecure,
			UserDataDir: *udd, StepTimeout: *timeout, NoWait: *noWait}
		if *edge {
			o.Browser = "edge"
		}
		return o
	}
}

// Load reads Safeguard values (stdin JSON if present, else legacy flags),
// parses fs (site + std flags), and initialises logging.
func Load(name string, fs *flag.FlagSet) *Input {
	in := &Input{Values: map[string]string{}}
	args := os.Args[1:]

	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		data, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if line := lastJSONLine(string(data)); line != "" {
			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err == nil {
				for k, v := range raw {
					switch t := v.(type) {
					case string:
						in.Values[k] = t
					case nil:
					default:
						bs, _ := json.Marshal(t) // arrays/objects, e.g. Target.TotpCodes
						in.Values[k] = string(bs)
					}
				}
				// With --use-stdin, --args arrives as cli_args; merge with any real argv.
				args = append(SplitArgs(in.Values["cli_args"]), args...)
			}
		}
	}

	debug := fs.Bool("debug", false, "debug logging (secrets are never logged)")
	user := fs.String("username", "", "LEGACY: username (prefer Launcher --use-stdin)")
	fs.StringVar(user, "account", "", "LEGACY alias of -username")
	pass := fs.String("password", "", "LEGACY: password (prefer Launcher --use-stdin)")
	asset := fs.String("asset", "", "asset network address")
	fs.Var(kvFlag(in.Values), "set", "extra key=value (repeatable), e.g. -set tenant=contoso")
	fs.Init(name, flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		initLog(name, false)
		Fatal(err)
	}
	in.Debug = *debug
	in.Args = fs.Args()
	for k, v := range map[string]string{"username": *user, "password": *pass, "asset": *asset} {
		if v != "" && in.Values[k] == "" {
			in.Values[k] = v
		}
	}
	alias(in.Values, "asset", "Target.AssetNetworkAddress", "Target.AssetName")
	alias(in.Values, "username", "Target.AccountName")

	initLog(name, in.Debug)
	slog.Info("starting", "wrapper", name, "keys", keys(in.Values), "positional", len(in.Args))
	return in
}

// Run launches the browser, navigates to url, executes steps, then (unless
// NoWait) blocks until the user closes the browser.
func Run(o Options, url string, steps ...chromedp.Action) {
	if o.WindowSize == "" {
		o.WindowSize = "1280,800"
	}
	if o.StepTimeout == 0 {
		o.StepTimeout = 90 * time.Second
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", false),
		chromedp.Flag("disable-infobars", true),
		// Never offer to save the injected password in the browser.
		chromedp.Flag("disable-features", "PasswordManagerOnboarding,AutofillServerCommunication,Translate"),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("window-size", o.WindowSize),
		chromedp.Flag("start-maximized", true),
	)
	if p := browserPath(o.Browser); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	if o.Incognito {
		if strings.EqualFold(o.Browser, "edge") {
			opts = append(opts, chromedp.Flag("inprivate", true))
		} else {
			opts = append(opts, chromedp.Flag("incognito", true))
		}
	}
	if o.Kiosk {
		opts = append(opts, chromedp.Flag("kiosk", true))
	}
	if o.Insecure {
		opts = append(opts, chromedp.Flag("ignore-certificate-errors", true))
	}
	if o.UserDataDir != "" {
		dir := strings.ReplaceAll(o.UserDataDir, "%AppData%", os.Getenv("APPDATA"))
		_ = os.MkdirAll(dir, 0o700)
		opts = append(opts, chromedp.UserDataDir(dir))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithErrorf(func(f string, a ...any) {
		slog.Debug("chromedp: " + fmt.Sprintf(f, a...))
	}))
	defer cancel()

	if err := chromedp.Run(ctx, chromedp.Navigate(url)); err != nil {
		Fatal(fmt.Errorf("navigate %s: %w", url, err))
	}
	slog.Info("navigated", "url", url)

	for i, s := range steps {
		sctx, scancel := context.WithTimeout(ctx, o.StepTimeout)
		err := chromedp.Run(sctx, s)
		scancel()
		if err != nil {
			// Leave the browser up so the user can finish manually.
			slog.Error("step failed", "step", i+1, "err", err)
			fmt.Fprintf(os.Stderr, "Automatic sign-in failed at step %d: %v\nYou may finish signing in manually.\n", i+1, err)
			break
		}
		slog.Debug("step ok", "step", i+1)
	}
	slog.Info("login sequence finished")
	if o.NoWait {
		return
	}
	if b := chromedp.FromContext(ctx).Browser; b != nil {
		if p := b.Process(); p != nil {
			_, _ = p.Wait()
		}
	}
	slog.Info("browser closed; exiting")
}

// Fatal logs, shows the error briefly in the console, and exits non-zero.
func Fatal(err error) {
	slog.Error("fatal", "err", err)
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	if runtime.GOOS == "windows" {
		time.Sleep(10 * time.Second)
	}
	os.Exit(1)
}

// ---- helpers ----

func browserPath(b string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	var cands []string
	if strings.EqualFold(b, "edge") {
		cands = []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
	} else {
		cands = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "" // fall back to chromedp's PATH search
}

func initLog(name string, debug bool) {
	base := os.Getenv("APPDATA")
	dir := filepath.Join(base, "OneIdentity", "OI-SG-RemoteApp-Launcher-Orchestration")
	if base == "" {
		dir = os.TempDir()
	}
	_ = os.MkdirAll(dir, 0o700)
	var w io.Writer = os.Stderr
	if f, err := os.OpenFile(filepath.Join(dir, name+"_"+time.Now().Format("2006-01-02")+".log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		w = f
	}
	lvl := slog.LevelInfo
	if debug {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})))
}

func lastJSONLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") {
			return l
		}
	}
	return ""
}

func keys(m map[string]string) []string { // keys only — values are never logged
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func alias(m map[string]string, dst string, srcs ...string) {
	if m[dst] != "" {
		return
	}
	for _, s := range srcs {
		if m[s] != "" {
			m[dst] = m[s]
			return
		}
	}
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func orDur(a, b time.Duration) time.Duration {
	if a != 0 {
		return a
	}
	return b
}

type kvFlag map[string]string

func (k kvFlag) String() string { return "" }
func (k kvFlag) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	if !ok {
		return errors.New("expected key=value")
	}
	if _, exists := k[key]; !exists { // Safeguard-provided values win
		k[key] = val
	}
	return nil
}

// SplitArgs splits a command line, honouring double quotes.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQ, has = !inQ, true
		case (r == ' ' || r == '\t') && !inQ:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}
