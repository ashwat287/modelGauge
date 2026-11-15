//go:build !bundled

package control

import "errors"

// Guest builds exclude embedded runtimes to avoid recursively embedding themselves.
func nodeBinary(arch string) ([]byte, error) {
	return nil, errors.New("Linux runtimes are not bundled; build the CLI with make build or make install")
}
