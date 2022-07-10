package rtsp

import (
	"bufio"
	"fmt"
	"net"
	"time"
)

type connWithTimeout struct {
	Timeout time.Duration
	net.Conn
}

func (c connWithTimeout) Read(p []byte) (n int, err error) {
	if c.Timeout > 0 {
		c.Conn.SetReadDeadline(time.Now().Add(c.Timeout))
	}
	return c.Conn.Read(p)
}

func (c connWithTimeout) Write(p []byte) (n int, err error) {
	if c.Timeout > 0 {
		c.Conn.SetWriteDeadline(time.Now().Add(c.Timeout))
	}
	return c.Conn.Write(p)
}

func readBytesLimited(rb *bufio.Reader, delim byte, n int) ([]byte, int, error) {
	for i := 1; i <= n; i++ {
		byts, err := rb.Peek(i)
		if err != nil {
			return nil, -1, err
		}

		if byts[len(byts)-1] == delim {
			rb.Discard(len(byts))
			return byts, i, nil
		}
	}

	return nil, -1, fmt.Errorf("buffer length exceeds %d", n)
}

func readByteEqual(rb *bufio.Reader, cmp byte) error {
	byt, err := rb.ReadByte()
	if err != nil {
		return err
	}

	if byt != cmp {
		return fmt.Errorf("expected '%c', got '%c'", cmp, byt)
	}

	return nil
}
