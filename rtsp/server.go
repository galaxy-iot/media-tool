package rtsp

import (
	"net"
	"strconv"

	"github.com/wh8199/log"
)

type Server struct {
	rtspListen net.Listener
	rtspPort   int
}

func Listen(portNum int) (*Server, error) {
	s := &Server{
		rtspPort: portNum,
	}

	listener, err := net.Listen("tcp", ":"+strconv.Itoa(portNum))
	if err != nil {
		return nil, err
	}

	s.rtspListen = listener

	return s, err
}

func (s *Server) Start() {
	go s.incomingConnectionHandler()
}

func (s *Server) incomingConnectionHandler() {
	for {
		tcpConn, err := s.rtspListen.Accept()
		if err != nil {
			log.Error(err)
			continue
		}

		// Create a new object for handling server RTSP connection:
		go s.newClientConnection(tcpConn)
	}
}

func (s *Server) newClientConnection(conn net.Conn) {
	c := newRTSPClientConnection(s, conn)
	if c != nil {
		c.incomingRequestHandler()
	}
}
