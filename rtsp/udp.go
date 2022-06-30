package rtsp

import (
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/net/ipv4"
)

func randUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func randIntn(n int) int {
	return int(randUint32() & (uint32(n) - 1))
}

type clientUDPListener struct {
	c     *Client
	pc    *net.UDPConn
	isRTP bool

	writeTimeout time.Duration

	localPort int
	readIP    net.IP
	readPort  int
	writeAddr *net.UDPAddr
}

func newClientUDPListenerPair(c *Client, multiCast bool) (*clientUDPListener, *clientUDPListener) {
	// choose two consecutive ports in range 65535-10000
	// RTP port must be even and RTCP port odd
	for {
		rtpPort := (randIntn((65535-10000)/2) * 2) + 10000
		rtpListener, err := newClientUDPListener(c, rtpPort, multiCast, true)
		if err != nil {
			continue
		}

		rtcpPort := rtpPort + 1
		rtcpListener, err := newClientUDPListener(c, rtcpPort, multiCast, true)
		if err != nil {
			rtpListener.close()
			continue
		}

		return rtpListener, rtcpListener
	}
}

func listenPacket2Conn(listenPacket interface{}) (*net.UDPConn, error) {
	c, ok := listenPacket.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("invalid listen packet")
	}

	return c, nil
}

func newClientUDPListener(c *Client, port int, multicast, isRTP bool) (*clientUDPListener, error) {
	var (
		pc *net.UDPConn
	)

	if multicast {
		conn, err := net.ListenPacket("udp", "224.0.0.0:"+strconv.Itoa(int(port)))
		if err != nil {
			fmt.Println(err)
		}

		p := ipv4.NewPacketConn(conn)
		err = p.SetMulticastTTL(100)
		if err != nil {
			return nil, err
		}

		intfs, err := net.Interfaces()
		if err != nil {
			return nil, err
		}

		for _, intf := range intfs {
			err := p.JoinGroup(&intf, &net.UDPAddr{IP: net.ParseIP("")})
			if err != nil {
				return nil, err
			}
		}

		pc, err = listenPacket2Conn(p)
		if err != nil {
			return nil, err
		}
	} else {
		p, err := net.ListenPacket("udp", ":"+strconv.Itoa(int(port)))
		if err != nil {
			return nil, err
		}

		pc, err = listenPacket2Conn(p)
		if err != nil {
			return nil, err
		}
	}

	if err := pc.SetReadBuffer(0x80000); err != nil {
		return nil, err
	}

	return &clientUDPListener{
		c:         c,
		pc:        pc,
		isRTP:     isRTP,
		localPort: port,
	}, nil
}

func (u *clientUDPListener) close() {
	u.pc.Close()
}

func (u *clientUDPListener) setIP(c net.Conn, port int) {
	u.readIP = c.RemoteAddr().(*net.TCPAddr).IP

	u.readPort = port
	u.writeAddr = &net.UDPAddr{
		IP:   u.readIP,
		Zone: c.RemoteAddr().(*net.TCPAddr).Zone,
		Port: port,
	}
}

func (u *clientUDPListener) getPort() int {
	return u.localPort
}

func (u *clientUDPListener) write(payload []byte) error {
	// no mutex is needed here since Write() has an internal lock.
	// https://github.com/golang/go/issues/27203#issuecomment-534386117

	u.pc.SetWriteDeadline(time.Now().Add(u.writeTimeout))
	_, err := u.pc.WriteTo(payload, u.writeAddr)
	return err
}
