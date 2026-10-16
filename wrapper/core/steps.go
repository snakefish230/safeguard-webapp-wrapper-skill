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

// WaitGone waits until sel is no longer visible, at most d. It never fails: if
// the element is still there it just continues. Use it to pin a page transition
// before an optional click, when the same selector exists on both pages (e.g.
// Entra's #idBtn_Back is "Back" on the password page and "No" on "Stay signed in?").
func WaitGone(sel string, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		c, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		if err := chromedp.Run(c, chromedp.WaitNotVisible(sel, chromedp.ByQuery)); err != nil {
			slog.Debug("still visible after wait", "sel", sel)
		} else {
			slog.Debug("gone", "sel", sel)
		}
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

// TOTP picks a code from Safeguard's Target.TotpCodes JSON that is valid for at
// least minValid more seconds and types it into sel. Evaluated at run time so
// the code is fresh when the OTP field appears.
func TOTP(sel, totpJSON string, minValid int) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		code, err := pickTOTP(totpJSON, minValid)
		if err != nil {
			return err
		}
		return chromedp.Run(ctx, Secret(sel, code))
	})
}

func pickTOTP(js string, minValid int) (string, error) {
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
		if int(c.UnixTime+c.Period)-int(now) >= minValid {
			return c.Code, nil
		}
	}
	return "", fmt.Errorf("no TOTP code valid for >= %ds", minValid)
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
