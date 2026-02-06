package raria2

import "io"

func closeQuietly(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
}
