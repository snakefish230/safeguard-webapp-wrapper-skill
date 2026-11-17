//go:build !windows

package main

import "errors"

func runNetOnly(user, domain, password, cmdline string, hideConsole bool) (uint32, error) {
	return 0, errors.New("aduc runs only on Windows")
}
