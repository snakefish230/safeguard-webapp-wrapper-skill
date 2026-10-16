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

// setValueJS assigns a value through the prototype setter so frameworks that
// wrap the input (Entra's sign-in is React) see the change, then fires the
// events the page's own validation listens for.
const setValueJS = `(function(s,v){
  var e=document.querySelector(s); if(!e) return null;
  var d=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e),'value');
  if(d&&d.set){d.set.call(e,v);}else{e.value=v;}
  e.dispatchEvent(new Event('input',{bubbles:true}));
  e.dispatchEvent(new Event('change',{bubbles:true}));
  return e.value;})(%s,%s)`

const readValueJS = `(function(s){var e=document.querySelector(s);return e?e.value:null;})(%s)`

// typeInto types val into sel and verifies the field really holds it.
//
// Raw key events are what login pages expect, but the first keystroke is lost
// if the browser has not finished focusing the field. That silently truncates a
// username or password and surfaces much later as a failed sign-in, so the
// value is read back and repaired rather than trusted. Never logs val.
func typeInto(sel, val, desc string) chromedp.Action {
	return logged(desc, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(sel, chromedp.ByQuery),
			chromedp.Clear(sel, chromedp.ByQuery),
			chromedp.SendKeys(sel, val, chromedp.ByQuery),
		); err != nil {
			return err
		}
		jsSel, err := json.Marshal(sel)
		if err != nil {
			return err
		}
		var got string
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(readValueJS, jsSel), &got)); err != nil {
			slog.Debug("could not verify field; leaving typed value", "sel", sel)
			return nil
		}
		if got == val {
			return nil
		}
		slog.Debug("typed value did not stick; setting it directly",
			"sel", sel, "want_len", len(val), "got_len", len(got))
		jsVal, err := json.Marshal(val)
		if err != nil {
			return err
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(setValueJS, jsSel, jsVal), &got)); err != nil {
			return fmt.Errorf("set %s: %w", sel, err)
		}
		if got != val {
			return fmt.Errorf("field %s holds %d chars, expected %d", sel, len(got), len(val))
		}
		return nil
	}))
}

// Type waits for sel to be visible, clears it, and types a non-secret value.
func Type(sel, val string) chromedp.Action { return typeInto(sel, val, "type "+sel) }

// Secret is Type for passwords/OTPs (value is not logged).
func Secret(sel, val string) chromedp.Action { return typeInto(sel, val, "secret "+sel) }

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

const visibleIdxJS = `(function(sels){
for (var i=0;i<sels.length;i++){
  var e=document.querySelector(sels[i]);
  if(!e) continue;
  var r=e.getBoundingClientRect();
  if(r.width>0&&r.height>0&&getComputedStyle(e).visibility!=='hidden') return i;
}
return -1;})(%s)`

// probeVisible reports the index of the first visible selector, or -1 if none
// are. ok is false when the page could not be queried at all, which happens
// mid-navigation and must not be confused with "nothing is visible".
func probeVisible(ctx context.Context, sels []string) (idx int, ok bool) {
	js, err := json.Marshal(sels)
	if err != nil {
		return -1, false
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(visibleIdxJS, js), &idx)); err != nil {
		return -1, false
	}
	return idx, true
}

// WaitAny polls sels in priority order and returns the index of the first one
// visible, or -1 if none appear within d. Earlier selectors win when several
// are visible at once, which matters on pages that render a control and its
// alternative together. Polling both beats waiting on one then the other: no
// branch pays a fixed timeout.
func WaitAny(ctx context.Context, d time.Duration, sels ...string) int {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		if idx, ok := probeVisible(c, sels); ok && idx >= 0 {
			return idx
		}
		select {
		case <-c.Done():
			return -1
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ClickIfPresentUnless clicks sel if it appears, unless skip appears first.
// Use when an optional control also exists on the page you are heading for, so
// clicking it would navigate away and come back: Entra's "sign in another way"
// link is on the verification-code page as well as the push-approval page.
func ClickIfPresentUnless(sel, skip string, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		switch WaitAny(ctx, d, skip, sel) {
		case 0:
			slog.Debug("already on target page, skipping optional click", "sel", sel, "skip", skip)
		case 1:
			if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
				slog.Debug("optional click failed", "sel", sel, "err", err)
			} else {
				slog.Debug("clicked optional", "sel", sel)
			}
		default:
			slog.Debug("neither appeared", "sel", sel, "skip", skip)
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

// TOTPSubmit fills sel with a TOTP code from Safeguard's Target.TotpCodes JSON
// and clicks btn, then waits for the form to resolve.
//
// The code is picked only *after* the field is visible. Picking it earlier
// spends the code's remaining lifetime waiting for the page, which is how a
// correct code arrives expired.
//
// errSel, when set, is the site's "that code is wrong" message. Racing it
// against the field disappearing turns a rejection into an immediate retry
// instead of sitting out the whole timeout. Pass "" if the site has no such
// element; rejection is then only detected by timeout.
//
// Target.TotpCodes is a snapshot taken at launch, so a rejected code is retried
// with a different one from that snapshot (typically the next period's) rather
// than by regenerating.
func TOTPSubmit(sel, btn, errSel, totpJSON string, minValid, attempts int) chromedp.Action {
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
			if err := chromedp.Run(ctx, Secret(sel, code)); err != nil {
				return err
			}
			// An error left over from the previous attempt would be read as an
			// instant rejection of this one. Typing normally clears it; if it
			// is still up, fall back to detecting success only.
			watchErr := errSel
			if watchErr != "" {
				if idx, ok := probeVisible(ctx, []string{watchErr}); ok && idx == 0 {
					slog.Debug("previous error still displayed; using field-gone detection", "n", i)
					watchErr = ""
				}
			}
			if err := chromedp.Run(ctx, Click(btn)); err != nil {
				return err
			}
			if accepted, why := otpOutcome(ctx, sel, watchErr, 20*time.Second); accepted {
				slog.Debug("otp accepted", "n", i)
				return nil
			} else {
				slog.Debug("otp not accepted", "n", i, "why", why)
			}
		}
		return fmt.Errorf("OTP rejected after %d attempt(s)", attempts)
	})
}

// otpOutcome waits for an OTP submission to resolve: the field going away means
// the code was taken, errSel appearing means it was refused.
func otpOutcome(ctx context.Context, sel, errSel string, d time.Duration) (accepted bool, why string) {
	// The error must be probed before the field. A refused code leaves the
	// field on screen, so checking the field first would match every time and
	// hide the error.
	var sels []string
	errIdx := -1
	if errSel != "" {
		sels = append(sels, errSel)
		errIdx = 0
	}
	sels = append(sels, sel)

	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		if idx, ok := probeVisible(c, sels); ok {
			switch {
			case idx == -1:
				return true, "field gone"
			case idx == errIdx:
				return false, "site reported an invalid code"
			}
		}
		select {
		case <-c.Done():
			return false, "still on screen after timeout"
		case <-time.After(200 * time.Millisecond):
		}
	}
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
