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

// Submit clicks btn and confirms the form actually advanced, pressing Enter in
// field as a fallback when it did not.
//
// A synthetic click is delivered at the element's coordinates, so it misses
// whenever the page moves the button between the hit-test and the dispatch --
// Entra animates its view transitions, and re-renders when its own validation
// runs. The click reports success because it was dispatched; the form simply
// sits there. To the user that looks exactly like the automation waiting for
// them to press Enter, which is the symptom this fixes. Every login form
// submits on Enter, so that is the fallback.
//
// errSel, when set, names the page's "those credentials were rejected" element.
// A rejected credential also leaves field on screen, and re-submitting it would
// burn a second failed attempt and walk the account towards a lockout, so the
// fallback is suppressed when that error is showing.
// submitGrace is how long Submit waits for a click to take effect before
// falling back to Enter.
const submitGrace = 3 * time.Second

// submitted reports whether a login form has moved on. It deliberately does not
// ask "is the field gone", because that answer is wrong on Entra: the password
// page leaves input[name="loginfmt"] in the DOM and visible, so a
// disappearance test reports failure on every single step. It asks the positive
// question instead — has the thing we are heading for appeared, or has the form
// rejected us — and only falls back to the field's own absence.
//
// The selectors are probed in priority order, so next winning over field
// matters: both are on screen at once during the overlap.
func submitted(ctx context.Context, field, next, errSel string) (done bool, rejected bool) {
	var sels []string
	if next != "" {
		sels = append(sels, next)
	}
	errIdx := -1
	if errSel != "" {
		errIdx = len(sels)
		sels = append(sels, errSel)
	}
	fieldIdx := len(sels)
	sels = append(sels, field)

	idx, ok := probeVisible(ctx, sels)
	if !ok {
		// The page could not be queried, which happens mid-navigation. That is
		// not evidence either way, so keep waiting.
		return false, false
	}
	if idx == fieldIdx {
		return false, false
	}
	return true, idx == errIdx
}

// awaitSubmit polls submitted until the form moves on or d expires.
func awaitSubmit(ctx context.Context, field, next, errSel string, d time.Duration) (done bool, rejected bool) {
	deadline := time.Now().Add(d)
	for {
		if done, rejected := submitted(ctx, field, next, errSel); done {
			return true, rejected
		}
		if time.Now().After(deadline) {
			return false, false
		}
		select {
		case <-ctx.Done():
			return false, false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Submit advances a login form and confirms it actually advanced.
//
// chromedp.Click dispatches at the element's coordinates, so it can be lost
// when the page re-renders between hit-test and dispatch: the click reports
// success and the form just sits there, looking to the user exactly like it is
// waiting for them to press Enter. Submit therefore clicks, waits a short grace
// for the form to move on, and otherwise sends Enter into the field itself.
//
// next is the selector we expect on the other side. Pass it whenever one
// exists; without it Submit can only fall back to the field's own absence,
// which several identity providers never deliver.
//
// errSel guards against lockouts: a rejected credential also leaves the field
// on screen, and pressing Enter there resubmits a value already known to be
// wrong, spending a second attempt.
func Submit(field, btn, next, errSel string, d time.Duration) chromedp.Action {
	return logged("submit "+field, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(btn, chromedp.ByQuery),
			chromedp.Click(btn, chromedp.ByQuery),
		); err != nil {
			slog.Debug("submit click failed, will try Enter", "btn", btn, "err", err)
		}
		// Short grace, not d: this is dead time on every sign-in, and a click
		// that registered moves the form on in about a second.
		done, rejected := awaitSubmit(ctx, field, next, errSel, min(submitGrace, d))
		if rejected {
			slog.Debug("form rejected the value; not resubmitting", "field", field)
			return nil
		}
		if done {
			return nil
		}
		slog.Debug("form did not advance on click, pressing Enter", "field", field)
		if err := chromedp.Run(ctx, chromedp.SendKeys(field, kb.Enter, chromedp.ByQuery)); err != nil {
			slog.Debug("enter failed", "field", field, "err", err)
		}
		if done, rejected := awaitSubmit(ctx, field, next, errSel, d); !done {
			slog.Debug("form never advanced", "field", field, "next", next)
		} else if rejected {
			slog.Debug("form rejected the value", "field", field)
		}
		return nil
	}))
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

// visibleIdxJS asks whether an element is really on screen and reachable, not
// merely present with a non-zero box. Entra parks the view you came from
// instead of removing it, so input[name="loginfmt"] still has a box while the
// password page is up (measured 2026-10-19). A naive test calls that visible
// and every "has the form moved on" check reports a false negative.
//
// An element counts only if it is inside the viewport, not inside an
// aria-hidden subtree, and is what a click at its centre would actually reach.
// That last test is what rules out a parked view: it is either scrolled away,
// covered by the live view, or inert, and in all three cases elementFromPoint
// returns something else.
const visibleIdxJS = `(function(sels){
function vis(e){
  var r=e.getBoundingClientRect();
  if(r.width<=0||r.height<=0) return false;
  var st=getComputedStyle(e);
  if(st.visibility==='hidden'||st.display==='none'||st.opacity==='0') return false;
  if(e.closest('[aria-hidden="true"]')) return false;
  var cx=r.left+r.width/2, cy=r.top+r.height/2;
  if(cx<0||cy<0||cx>window.innerWidth||cy>window.innerHeight) return false;
  var top=document.elementFromPoint(cx,cy);
  return !!top&&(top===e||e.contains(top));
}
for (var i=0;i<sels.length;i++){
  // querySelectorAll, not querySelector: these are comma-joined selector
  // groups and the first element in document order is routinely a hidden copy
  // on a view that has not been reached yet. Testing only that one reports the
  // whole group as absent while a perfectly visible match sits further down.
  var els=document.querySelectorAll(sels[i]);
  for (var j=0;j<els.length;j++){ if(vis(els[j])) return i; }
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
func TOTPSubmit(sel, btn, okSel, errSel, totpJSON string, minValid, attempts int) chromedp.Action {
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
			if accepted, why := otpOutcome(ctx, sel, okSel, watchErr, 20*time.Second); accepted {
				slog.Debug("otp accepted", "n", i)
				return nil
			} else {
				slog.Debug("otp not accepted", "n", i, "why", why)
			}
		}
		return fmt.Errorf("OTP rejected after %d attempt(s)", attempts)
	})
}

// otpOutcome waits for an OTP submission to resolve: okSel appearing or the
// field going away means the code was taken, errSel appearing means it was
// refused.
//
// okSel is the positive signal and is preferred over the field's absence for
// the same reason Submit needs one — a provider that parks the page it came
// from never delivers the absence, and the submission then reads as a
// rejection even though it succeeded.
func otpOutcome(ctx context.Context, sel, okSel, errSel string, d time.Duration) (accepted bool, why string) {
	// The error must be probed before the field. A refused code leaves the
	// field on screen, so checking the field first would match every time and
	// hide the error.
	var sels []string
	errIdx := -1
	if errSel != "" {
		sels = append(sels, errSel)
		errIdx = 0
	}
	okIdx := -1
	if okSel != "" {
		okIdx = len(sels)
		sels = append(sels, okSel)
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
			case idx == okIdx:
				return true, "next page shown"
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
