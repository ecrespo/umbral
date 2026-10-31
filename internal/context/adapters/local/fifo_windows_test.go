package local

import "errors"

func mkfifo(string) error { return errors.New("Windows has no FIFO in a directory") }
