//go:build !windows

package service

import "errors"

func RunWindowsService(_ string, _ Worker) error {
	return errors.New("Windows SCM is only available on Windows")
}
