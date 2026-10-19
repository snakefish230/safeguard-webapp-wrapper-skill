// probe: dump visible inputs/buttons of a login flow to discover selectors.
// Usage: go run ./tools/probe -url https://portal.azure.com -step 'type:input[name=loginfmt]:probe@contoso.onmicrosoft.com' -step 'click:#idSIButton9'
// Only ever type DUMMY values; never real credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const dump = `[...document.querySelectorAll('input:not([type=hidden]),button,[role=button],a[role=link]')].filter(e=>e.offsetParent).map(e=>[e.tagName,'#'+e.id,'name='+(e.name||''),'type='+(e.type||''),'ac='+(e.autocomplete||''),'aria='+(e.getAttribute('aria-label')||''),JSON.stringify((e.value||e.innerText||'').trim().slice(0,30))].join(' | ')).join('\n')`

type steps []string

func (s *steps) String() string     { return strings.Join(*s, ",") }
func (s *steps) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	url := flag.String("url", "", "start URL")
	chrome := flag.String("chrome", os.Getenv("CHROME"), "chrome binary; defaults to $CHROME, else whatever chromedp finds on PATH")
	wait := flag.Duration("wait", 5*time.Second, "settle time after each step")
	var st steps
	flag.Var(&st, "step", "type:<css>:<dummy> | click:<css> (repeatable)")
	flag.Parse()
	o := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(*chrome), chromedp.NoSandbox, chromedp.WindowSize(1280, 900))
	a, _ := chromedp.NewExecAllocator(context.Background(), o...)
	c, _ := chromedp.NewContext(a)
	c, cancel := context.WithTimeout(c, 90*time.Second)
	defer cancel()
	show := func(label string) {
		var u, d, t string
		_ = chromedp.Run(c, chromedp.Sleep(*wait), chromedp.Location(&u), chromedp.Evaluate(dump, &d), chromedp.Evaluate(`document.body.innerText.replace(/\s+/g,' ').slice(0,250)`, &t))
		fmt.Printf("== %s  %s\n%s\nTEXT: %s\n\n", label, u, d, t)
	}
	if err := chromedp.Run(c, chromedp.Navigate(*url)); err != nil {
		fmt.Println("navigate:", err)
		os.Exit(1)
	}
	show("start")
	for i, s := range st {
		p := strings.SplitN(s, ":", 3)
		var err error
		switch p[0] {
		case "type":
			err = chromedp.Run(c, chromedp.WaitVisible(p[1], chromedp.ByQuery), chromedp.SendKeys(p[1], p[2], chromedp.ByQuery))
		case "click":
			err = chromedp.Run(c, chromedp.WaitVisible(p[1], chromedp.ByQuery), chromedp.Click(p[1], chromedp.ByQuery))
		}
		if err != nil {
			fmt.Printf("step %d (%s) failed: %v\n", i+1, s, err)
		}
		show(fmt.Sprintf("after step %d (%s)", i+1, p[0]+" "+p[1]))
	}
}
