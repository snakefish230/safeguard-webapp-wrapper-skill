package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Step helpers: thin, logged wrappers around chromedp actions. All selectors
// are CSS (ByQuery) unless stated. Secrets are never written to logs.

// Type waits for sel to be visible, clears it, and types a non-secret value.
func Type(sel, val string) chromedp.Action {
	return logged("type "+sel, chromedp.Tasks{
		chromedp.WaitVisible(sel, chromedp.ByQuery),
		chromedp.Clear(sel, chromedp.ByQuery),
		chromedp.SendKeys(sel, val, chromedp.ByQuery),
	})
}

// Secret is Type for passwords/OTPs (value is not logged).
func Secret(sel, val string) chromedp.Action {
	return logged("secret "+sel, chromedp.Tasks{
		chromedp.WaitVisible(sel, chromedp.ByQuery),
		chromedp.Clear(sel, chromedp.ByQuery),
		chromedp.SendKeys(sel, val, chromedp.ByQuery),
	})
}

// Click waits for sel to be visible and clicks it.
func Click(sel string) chromedp.Action {
	return logged("click "+sel, chromedp.Tasks{
		chromedp.WaitVisible(sel, chromedp.ByQuery),
		chromedp.Click(sel, chromedp.ByQuery),
	})
}

// Enter presses Enter in sel.
func Enter(sel string) chromedp.Action {
	return logged("enter "+sel, chromedp.SendKeys(sel, kb.Enter, chromedp.ByQuery))
}

// Sleep pauses (use sparingly; prefer waits).
func Sleep(d time.Duration) chromedp.Action { return chromedp.Sleep(d) }

// WaitVisible waits for an element.
func WaitVisible(sel string) chromedp.Action {
	return logged("wait "+sel, chromedp.WaitVisible(sel, chromedp.ByQuery))
}

// Visible reports whether sel becomes visible within d.
func Visible(ctx context.Context, sel string, d time.Duration) bool {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return chromedp.Run(c, chromedp.WaitVisible(sel, chromedp.ByQuery)) == nil
}

// WentGone waits until sel stops being visible, at most d, and reports whether
// it did. Use it to tell "the form was accepted" from "the form is still up
// showing an error".
func WentGone(ctx context.Context, sel string, d time.Duration) bool {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return chromedp.Run(c, chromedp.WaitNotVisible(sel, chromedp.ByQuery)) == nil
}

// WaitGone waits until sel is no longer visible, at most d. It never fails: if
// the element is still there it just continues. Use it to pin a page transition
// before an optional click, when the same selector exists on both pages (e.g.
// Entra's #idBtn_Back is "Back" on the password page and "No" on "Stay signed in?").
func WaitGone(sel string, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		slog.Debug("gone", "sel", sel, "ok", WentGone(ctx, sel, d))
		return nil
	})
}

// ClickIfPresent clicks sel if it appears within d; otherwise continues.
// Useful for cookie banners, "Stay signed in?" prompts, etc.
func ClickIfPresent(sel string, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		c, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		if err := chromedp.Run(c, chromedp.WaitVisible(sel, chromedp.ByQuery), chromedp.Click(sel, chromedp.ByQuery)); err != nil {
			slog.Debug("optional element absent", "sel", sel)
		} else {
			slog.Debug("clicked optional", "sel", sel)
		}
		return nil
	})
}

// ClickIfPresentUnless clicks sel if it appears within d, unless skip is
// already visible (waiting up to skipWait for it). Use when an optional control
// also exists on the page you are trying to reach, so clicking it would
// navigate away and come back: Entra's "sign in another way" link is on the
// verification-code page as well as the push-approval page.
func ClickIfPresentUnless(sel, skip string, skipWait, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if Visible(ctx, skip, skipWait) {
			slog.Debug("already on target page, skipping optional click", "sel", sel, "skip", skip)
			return nil
		}
		return ClickIfPresent(sel, d).Do(ctx)
	})
}

// FirstVisible waits until any of sels is visible and runs the matching branch.
// Use for login pages that render different forms (e.g. root vs IAM, SSO pickers).
func FirstVisible(branches map[string]chromedp.Action) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for {
			for sel, act := range branches {
				var nodes []*cdp.Node
				if err := chromedp.Run(ctx, chromedp.Nodes(sel, &nodes, chromedp.ByQuery, chromedp.AtLeast(0))); err == nil && len(nodes) > 0 {
					slog.Debug("branch", "sel", sel)
					return chromedp.Run(ctx, act)
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	})
}

// TOTPSubmit fills sel with a TOTP code from Safeguard's Target.TotpCodes JSON
// and clicks btn, then confirms the field went away.
//
// The code is picked only *after* the field is visible. Picking it earlier
// spends the code's remaining lifetime waiting for the page, which is how a
// correct code arrives expired.
//
// Target.TotpCodes is a snapshot taken at launch, so a rejected code is retried
// with a different one from that snapshot (typically the next period's) rather
// than by regenerating.
func TOTPSubmit(sel, btn, totpJSON string, minValid, attempts int) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := chromedp.Run(ctx, chromedp.WaitVisible(sel, chromedp.ByQuery)); err != nil {
			return fmt.Errorf("OTP field %q never appeared: %w", sel, err)
		}
		tried := map[string]bool{}
		for i := 1; i <= attempts; i++ {
			code, err := waitTOTP(ctx, totpJSON, minValid, tried)
			if err != nil {
				return err
			}
			tried[code] = true
			slog.Debug("otp attempt", "n", i, "of", attempts)
			if err := chromedp.Run(ctx, Secret(sel, code), Click(btn)); err != nil {
				return err
			}
			if WentGone(ctx, sel, 20*time.Second) {
				slog.Debug("otp accepted", "n", i)
				return nil
			}
			slog.Debug("otp still on screen, treating as rejected", "n", i)
		}
		return fmt.Errorf("OTP rejected after %d attempt(s)", attempts)
	})
}

// waitTOTP returns an unused code with at least minValid seconds left, waiting
// for the next period to start if every code in the snapshot is used or stale.
func waitTOTP(ctx context.Context, js string, minValid int, tried map[string]bool) (string, error) {
	deadline := time.Now().Add(40 * time.Second)
	for {
		code, err := pickTOTP(js, minValid, tried)
		if err == nil {
			return code, nil
		}
		if time.Now().After(deadline) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// TOTP is TOTPSubmit without a submit button, kept for wrappers whose OTP form
// auto-submits.
func TOTP(sel, totpJSON string, minValid int) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := chromedp.Run(ctx, chromedp.WaitVisible(sel, chromedp.ByQuery)); err != nil {
			return fmt.Errorf("OTP field %q never appeared: %w", sel, err)
		}
		code, err := pickTOTP(totpJSON, minValid, nil)
		if err != nil {
			return err
		}
		return chromedp.Run(ctx, Secret(sel, code))
	})
}

func pickTOTP(js string, minValid int, exclude map[string]bool) (string, error) {
	var codes []struct {
		Code     string  `json:"Code"`
		UnixTime float64 `json:"UnixTime"`
		Period   float64 `json:"Period"`
	}
	if err := json.Unmarshal([]byte(js), &codes); err != nil {
		return "", fmt.Errorf("parse Target.TotpCodes: %w", err)
	}
	now := time.Now().Unix()
	for _, c := range codes {
		if exclude[c.Code] {
			continue
		}
		if int(c.UnixTime+c.Period)-int(now) >= minValid {
			return c.Code, nil
		}
	}
	return "", fmt.Errorf("no unused TOTP code valid for >= %ds", minValid)
}

// WaitURLContains waits until the page URL contains s (e.g. post-login landing).
func WaitURLContains(s string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for {
			var u string
			if err := chromedp.Run(ctx, chromedp.Location(&u)); err == nil && strings.Contains(u, s) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
}

func logged(desc string, a chromedp.Action) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		slog.Debug("step", "do", desc)
		return a.Do(ctx)
	})
}
