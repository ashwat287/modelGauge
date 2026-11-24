//go:build remotehost

package control

import "errors"

func readMacRuntime(string) ([]byte, error) {
	return nil, errors.New("remote helper does not provision other Mac hosts")
}
