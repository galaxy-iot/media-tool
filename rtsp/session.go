package rtsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/galaxy-iot/media-tool/util"
)

const (
	ModePlay   = "play"
	ModeRecord = "record"
)

var (
	ErrInvalidTransport       = errors.New("invalid transport")
	ErrInvalidInterleavedPort = errors.New("invalid interleaved port")
	ErrInvalidServerPort      = errors.New("invalid server port")
	ErrInvalidClientPort      = errors.New("invalid client port")

	modeMap = map[string]string{
		`"play"`:   ModePlay,
		`"record"`: ModeRecord,
	}
)

type Session interface {
	String() string
	Start() error
	Stop()
	GetSessionID() string
}

type session struct {
	TrackID string

	// port configs
	ClientDataPort         int
	ClientControlPort      int
	ServerDataPort         int
	ServerControlPort      int
	InterleavedDataPort    int
	InterleavedControlPort int

	// transport mode, play or record
	Mode string
	// tcp,udp or multicast transport
	Transport    Transport
	SessionID    string
	RawTransport string
	IsServer     bool

	rtpConn  *net.UDPConn
	rtcpConn *net.UDPConn

	ctx      context.Context
	cancFunc context.CancelFunc
}

func (s *session) String() string {
	buf := bytes.Buffer{}

	switch s.Transport {
	case UdpTransport:
		buf.WriteString("RTP/AVP;unicast;")
		if s.ClientDataPort != 0 && s.ClientControlPort != 0 {
			buf.WriteString("client_port=")
			buf.WriteString(strconv.Itoa(s.ClientDataPort))
			buf.WriteString("-")
			buf.WriteString(strconv.Itoa(s.ClientControlPort))
			buf.WriteString(";")
		}

		if s.ServerDataPort != 0 && s.ServerControlPort != 0 {
			buf.WriteString("server_port=")
			buf.WriteString(strconv.Itoa(s.ServerDataPort))
			buf.WriteString("-")
			buf.WriteString(strconv.Itoa(s.ServerControlPort))
			buf.WriteString(";")
		}

		if s.Mode != "" {
			buf.WriteByte('"')
			buf.WriteString(s.Mode)
			buf.WriteByte('"')
			buf.WriteByte(';')
		}
	case TcpTransport:
		buf.WriteString("RTP/AVP/TCP;")
		if s.InterleavedDataPort != 0 && s.InterleavedControlPort != 0 {
			buf.WriteString("interleaved=")
			buf.WriteString(strconv.Itoa(s.InterleavedDataPort))
			buf.WriteString("-")
			buf.WriteString(strconv.Itoa(s.InterleavedControlPort))
			buf.WriteString(";")
		}
		if s.Mode != "" {
			buf.WriteByte('"')
			buf.WriteString(s.Mode)
			buf.WriteByte('"')
			buf.WriteByte(';')
		}
	case UdpMulticastTransport:
		buf.WriteString("RTP/AVP;multicast;")
	default:

	}

	return buf.String()
}

func convertPortPair(portsStr string) (int, int, error) {
	ports := strings.Split(portsStr, "-")
	dataPort, err := strconv.Atoi(ports[0])
	if err != nil {
		return 0, 0, err
	}

	controlPort, err := strconv.Atoi(ports[1])
	if err != nil {
		return 0, 0, err
	}

	return dataPort, controlPort, nil
}

func NewSession(trackID, trans, sess string, isServer bool) (Session, error) {
	// RTP/AVP;unicast;client_port=4588-4589
	// RTP/AVP;unicast;client_port=4588-4589;server_port=20000-20001;mode="play"
	// RTP/AVP/TCP;interleaved=0-1

	if sess == "" {
		sess = fmt.Sprintf("%08X", util.Random32())
	}

	ctx, cancelFunc := context.WithCancel(context.Background())

	s := &session{
		TrackID:      trackID,
		IsServer:     isServer,
		RawTransport: trans,
		Mode:         ModePlay,
		ctx:          ctx,
		cancFunc:     cancelFunc,
		SessionID:    sess,
	}

	items := strings.Split(trans, ";")
	for _, item := range items {
		item = strings.TrimSpace(item)

		if item == "RTP/AVP" || item == "unicast" || item == "multicast" || item == "RTP/AVP/UDP" {
			s.Transport = UdpTransport
		} else if item == "RTP/AVP/TCP" {
			s.Transport = TcpTransport
		} else if strings.HasPrefix(item, "interleaved") {
			interleaved := strings.TrimPrefix(item, "interleaved=")
			var err error
			s.InterleavedDataPort, s.InterleavedDataPort, err = convertPortPair(interleaved)
			if err != nil {
				return nil, ErrInvalidInterleavedPort
			}

		} else if strings.HasPrefix(item, "client_port") {
			clientPort := strings.TrimPrefix(item, "client_port=")
			var err error
			s.ClientDataPort, s.ClientControlPort, err = convertPortPair(clientPort)
			if err != nil {
				return nil, ErrInvalidClientPort
			}
		} else if strings.HasPrefix(item, "server_port") {
			serverPort := strings.TrimPrefix(item, "server_port=")
			var err error
			s.ServerDataPort, s.ServerControlPort, err = convertPortPair(serverPort)
			if err != nil {
				return nil, ErrInvalidServerPort
			}
		} else if strings.HasPrefix(item, "mode") {
			mode := strings.TrimPrefix(item, "mode=")
			s.Mode = modeMap[mode]
		}
	}

	if s.Transport == UdpTransport || s.Transport == UdpMulticastTransport {
		s.NewServerListenerPair()
	}

	return s, nil
}

func (s *session) Read() ([]byte, error) {
	return nil, nil
}

func (s *session) GetSessionID() string {
	return s.SessionID
}

func newUDPConnection(port int) (*net.UDPConn, error) {
	rtpNet, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}

	return net.ListenUDP("udp", rtpNet)
}

func (s *session) NewServerListenerPair() {
	for {
		rtpPort := (randIntn((65535-10000)/2) * 2) + 10000

		rtpConn, err := newUDPConnection(rtpPort)
		if err != nil {
			continue
		}

		rtcpPort := rtpPort + 1
		rtcpConn, err := newUDPConnection(rtcpPort)
		if err != nil {
			rtpConn.Close()
			continue
		}

		s.rtpConn = rtpConn
		s.ServerDataPort = rtpPort
		s.ServerControlPort = rtcpPort
		s.rtcpConn = rtcpConn
		return
	}
}

func (s *session) Stop() {
	if s.rtpConn != nil {
		s.rtpConn.Close()
	}

	if s.rtcpConn != nil {
		s.rtcpConn.Close()
	}

	if s.cancFunc != nil {
		s.cancFunc()
	}
}

func (s *session) Start() error {
	if s.IsServer && s.Transport == UdpTransport {
		if s.rtpConn == nil || s.rtcpConn == nil {
			return fmt.Errorf("rtp connection or rtcp connection is empty")
		}

		go func() {
			bytes := make([]byte, 2048)
			for {
				select {
				case <-s.ctx.Done():
					return
				default:
					if _, _, err := s.rtpConn.ReadFrom(bytes); err != nil {
						continue
					}
				}
			}
		}()

		go func() {
			bytes := make([]byte, 2048)
			for {
				select {
				case <-s.ctx.Done():
					return
				default:
					if _, _, err := s.rtcpConn.ReadFrom(bytes); err != nil {
						continue
					}
				}
			}
		}()

		return nil
	}

	return nil
}
