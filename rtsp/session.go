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
	ClientRtpPort  int
	ClientRtcpPort int

	ServerRtpPort  int
	ServerRtcpPort int

	InterleavedDataPort    int
	InterleavedControlPort int

	// transport mode, play or record
	Mode string
	// tcp,udp or multicast transport
	Transport    Transport
	SessionID    string
	RawTransport string
	IsServer     bool

	ClientRtpConn  *net.UDPConn
	ClientRtcpConn *net.UDPConn

	ServerRtpConn  *net.UDPConn
	ServerRtcpConn *net.UDPConn

	ctx      context.Context
	cancFunc context.CancelFunc
}

func (s *session) String() string {
	buf := bytes.Buffer{}

	switch s.Transport {
	case UdpTransport:
		buf.WriteString("RTP/AVP;unicast;")
		if s.ClientRtpPort != 0 && s.ClientRtcpPort != 0 {
			buf.WriteString("client_port=")
			buf.WriteString(strconv.Itoa(s.ClientRtpPort))
			buf.WriteString("-")
			buf.WriteString(strconv.Itoa(s.ClientRtcpPort))
			buf.WriteString(";")
		}

		if s.ServerRtpPort != 0 && s.ServerRtcpPort != 0 {
			buf.WriteString("server_port=")
			buf.WriteString(strconv.Itoa(s.ServerRtpPort))
			buf.WriteString("-")
			buf.WriteString(strconv.Itoa(s.ServerRtcpPort))
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

		if item == "RTP/AVP" || item == "multicast" || item == "RTP/AVP/UDP" {
			s.Transport = UdpTransport
		} else if item == "RTP/AVP/TCP" {
			s.Transport = TcpTransport
		} else if strings.HasPrefix(item, "interleaved") {
			interleaved := strings.TrimPrefix(item, "interleaved=")
			var err error
			s.InterleavedDataPort, s.InterleavedControlPort, err = convertPortPair(interleaved)
			if err != nil {
				return nil, ErrInvalidInterleavedPort
			}
		} else if strings.HasPrefix(item, "client_port") {
			clientPort := strings.TrimPrefix(item, "client_port=")
			var err error
			s.ClientRtpPort, s.ClientRtcpPort, err = convertPortPair(clientPort)
			if err != nil {
				return nil, ErrInvalidClientPort
			}
		} else if strings.HasPrefix(item, "server_port") {
			serverPort := strings.TrimPrefix(item, "server_port=")
			var err error
			s.ServerRtpPort, s.ServerRtcpPort, err = convertPortPair(serverPort)
			if err != nil {
				return nil, ErrInvalidServerPort
			}
		} else if strings.HasPrefix(item, "mode") {
			mode := strings.TrimPrefix(item, "mode=")
			s.Mode = modeMap[mode]
		}
	}

	if s.IsServer {
		if err := s.NewServerListenerPair(); err != nil {
			return nil, err
		}

		return s, nil
	}

	if s.Transport == UdpTransport {
		if err := s.NewClientListernPair(); err != nil {
			return nil, err
		}
	}

	return s, nil
}

func (s *session) GetSessionID() string {
	return s.SessionID
}

func newUDPClient(port int) (*net.UDPConn, error) {
	p, err := net.ListenPacket("udp", ":"+strconv.Itoa(int(port)))
	if err != nil {
		return nil, err
	}

	var listenPacket interface{} = p

	c, ok := listenPacket.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("invalid listen packet")
	}

	return c, nil
}

func (s *session) NewClientListernPair() (err error) {
	s.ClientRtpConn, err = newUDPClient(s.ClientRtpPort)
	if err != nil {
		return
	}

	s.ClientRtcpConn, err = newUDPClient(s.ClientRtcpPort)
	if err != nil {
		return
	}

	return
}

func newUDPServerConnection(port int) (*net.UDPConn, error) {
	rtpNet, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}

	return net.ListenUDP("udp", rtpNet)
}

func (s *session) NewServerListenerPair() (err error) {
	s.ServerRtpPort, s.ServerRtcpPort = allocPortPair()

	s.ClientRtpConn, err = newUDPServerConnection(s.ServerRtpPort)
	if err != nil {
		return
	}

	s.ClientRtcpConn, err = newUDPServerConnection(s.ServerRtcpPort)
	if err != nil {
		return
	}

	return
}

func (s *session) Stop() {
	if s.ClientRtpConn != nil {
		s.ClientRtpConn.Close()
	}

	if s.ClientRtcpConn != nil {
		s.ClientRtcpConn.Close()
	}

	if s.ServerRtcpConn != nil {
		s.ServerRtcpConn.Close()
	}

	if s.ServerRtpConn != nil {
		s.ServerRtpConn.Close()
	}

	if s.cancFunc != nil {
		s.cancFunc()
	}
}

func (s *session) Start() error {
	if s.IsServer && s.Transport == UdpTransport {
		if s.ClientRtpConn == nil || s.ClientRtcpConn == nil {
			return fmt.Errorf("rtp connection or rtcp connection is empty")
		}

		go func() {
			bytes := make([]byte, 2048)
			for {
				select {
				case <-s.ctx.Done():
					return
				default:
					if _, _, err := s.ClientRtpConn.ReadFrom(bytes); err != nil {
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
					if _, _, err := s.ClientRtcpConn.ReadFrom(bytes); err != nil {
						continue
					}
				}
			}
		}()

		return nil
	}

	return nil
}
