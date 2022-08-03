package rtsp

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"

	"github.com/galaxy-iot/media-tool/sdp"
	"github.com/galaxy-iot/media-tool/util"
	"github.com/wh8199/log"
)

const (
	MethodOption   = "OPTIONS"
	MethodAnnounce = "ANNOUNCE"
	MethodDescribe = "DESCRIBE"
	MethodPlay     = "PLAY"
	MethodPause    = "PAUSE"
	MethodSetup    = "SETUP"
	MethodTeardown = "TEARDOWN"
	MethodRecord   = "RECORD"
)

var (
	SupportMethods = []string{MethodOption, MethodDescribe, MethodPlay, MethodPause, MethodSetup, MethodTeardown}
)

type RTSPClientConnection struct {
	socket net.Conn
	brconn *bufio.Reader

	Uri string

	localPort  string
	remotePort string
	localAddr  string
	remoteAddr string

	server *Server
	digest *Digest

	session *sdp.SessionAttribute
	medias  []*sdp.Media

	clientSessions    map[string]Session
	clientSessionLock sync.RWMutex
}

func newRTSPClientConnection(server *Server, socket net.Conn) *RTSPClientConnection {
	localAddr := strings.Split(socket.LocalAddr().String(), ":")
	remoteAddr := strings.Split(socket.RemoteAddr().String(), ":")
	return &RTSPClientConnection{
		server:     server,
		socket:     socket,
		localAddr:  localAddr[0],
		localPort:  localAddr[1],
		remoteAddr: remoteAddr[0],
		remotePort: remoteAddr[1],
		digest:     NewDigest(),
		brconn:     bufio.NewReader(socket),

		clientSessions:    map[string]Session{},
		clientSessionLock: sync.RWMutex{},
	}
}

func (c *RTSPClientConnection) destroy() {
	if c.socket != nil {
		c.socket.Close()
	}

	for _, session := range c.clientSessions {
		session.Stop()
	}
}

func (c *RTSPClientConnection) incomingRequestHandler() {
	defer c.destroy()

	for {
		b, err := c.brconn.ReadByte()
		if err != nil {
			log.Error(err)
			return
		}
		c.brconn.UnreadByte()

		if b == 36 {
			header := make([]byte, 4)
			if _, err := io.ReadFull(c.brconn, header); err != nil {
				log.Error(err)
				return
			}

			tracker := header[1]

			log.Info(tracker)

			payloadLen := int(binary.BigEndian.Uint16(header[2:]))
			payload := make([]byte, payloadLen)

			if _, err := io.ReadFull(c.brconn, payload); err != nil {
				log.Error(err)
				return
			}

			_ = header
			_ = payload

			continue
		}

		req, err := c.readRequest()
		if err != nil {
			log.Error(err)
			return
		}

		resp, exit, err := c.requestHandler(req)
		if err != nil {
			log.Error(err)
			return
		}

		if err := c.writeResponse(resp); err != nil {
			log.Error(err)
			return
		}

		if exit {
			return
		}
	}
}

func (c *RTSPClientConnection) readRequest() (*Request, error) {
	// method url RTSP/1.0 \r\n
	// header \r\n
	// \r\n
	// body

	req := &Request{
		Headers: make(textproto.MIMEHeader),
	}

	byts, n, err := readBytesLimited(c.brconn, ' ', 255)
	if err != nil {
		return nil, err
	}
	req.Method = string(byts[:n-1])

	byts, n, err = readBytesLimited(c.brconn, ' ', 255)
	if err != nil {
		return nil, err
	}
	req.Uri = string(byts[:n-1])

	byts, n, err = readBytesLimited(c.brconn, '\r', 255)
	if err != nil {
		return nil, err
	}
	req.Version = string(byts[:n-1])

	err = readByteEqual(c.brconn, '\n')
	if err != nil {
		return nil, err
	}

	for {
		line, err := c.brconn.ReadString('\n')
		if err != nil {
			return nil, err
		}

		if line == "" || line == "\r\n" {
			break
		}

		headerPair := strings.SplitN(line, ":", 2)
		if len(headerPair) < 2 {
			return nil, fmt.Errorf("invalid header: %s", line)
		}

		key := headerPair[0]
		value := strings.TrimSpace(headerPair[1])

		if strings.ToLower(key) == "content-length" {
			contentLength, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid content length: %s", value)
			}

			req.ContentLength = contentLength
		}

		if strings.ToLower(key) == "cseq" {
			cseq, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid content length: %s", value)
			}

			req.Cseq = cseq
		}

		req.Headers.Add(key, value)
	}

	if req.ContentLength > 0 {
		req.Body = make([]byte, req.ContentLength)
		if _, err := io.ReadFull(c.brconn, req.Body); err != nil {
			return nil, err
		}
	}

	return req, nil
}

func (c *RTSPClientConnection) writeResponse(resp *Response) error {
	buf := &bytes.Buffer{}

	fmt.Fprintf(buf, "RTSP/1.0 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	fmt.Fprintf(buf, "CSeq: %d\r\n", resp.Cseq)

	for key := range resp.Headers {
		io.WriteString(buf, key)
		io.WriteString(buf, ": ")
		io.WriteString(buf, resp.Headers.Get(key))
		io.WriteString(buf, "\r\n")
	}

	io.WriteString(buf, "\r\n")
	bufout := buf.Bytes()

	if _, err := c.socket.Write(bufout); err != nil {
		return err
	}

	return nil
}

func getTrackID(uri, uriWithTrack string) string {
	trackID := strings.TrimPrefix(uriWithTrack, uri)

	if len(trackID) > 0 && trackID[0] == '/' {
		trackID = trackID[1:]
	}

	return trackID
}

func (c *RTSPClientConnection) requestHandler(req *Request) (*Response, bool, error) {
	switch req.Method {
	case MethodOption:
		resp := &Response{
			Method:     MethodOption,
			Cseq:       req.Cseq,
			Headers:    textproto.MIMEHeader{},
			StatusCode: http.StatusOK,
		}

		c.Uri = req.Uri

		resp.Headers.Add("Public", strings.Join(SupportMethods, ", "))
		return resp, false, nil
	case MethodAnnounce:
		session, medias, err := sdp.Parse(util.Bytes2String(req.Body))
		if err != nil {
			return nil, false, err
		}

		c.session = session
		c.medias = medias

		resp := &Response{
			Method:     MethodAnnounce,
			Cseq:       req.Cseq,
			StatusCode: http.StatusOK,
		}

		return resp, false, nil
	case MethodSetup:
		resp := &Response{
			Method:     MethodSetup,
			Cseq:       req.Cseq,
			StatusCode: http.StatusOK,
			Headers:    textproto.MIMEHeader{},
		}

		transport := req.Headers.Get("Transport")

		trackID := getTrackID(c.Uri, req.Uri)
		s, err := NewSession(trackID, transport, "", true, nil)
		if err != nil {
			return nil, false, err
		}

		c.clientSessionLock.Lock()
		c.clientSessions[trackID] = s
		c.clientSessionLock.Unlock()

		resp.Headers.Add("Session", s.GetSessionID())
		resp.Headers.Add("Transport", s.String())

		return resp, false, nil
	case MethodRecord:
		resp := &Response{
			Method:     MethodRecord,
			Cseq:       req.Cseq,
			StatusCode: http.StatusOK,
			Headers:    textproto.MIMEHeader{},
		}

		trackID := getTrackID(c.Uri, req.Uri)

		if trackID == "" {
			for _, session := range c.clientSessions {
				go session.Start()
			}
		} else {
			c.clientSessionLock.Lock()
			s := c.clientSessions[trackID]
			c.clientSessionLock.Unlock()

			if s == nil {
				resp.StatusCode = http.StatusNotFound
				return resp, false, nil
			}

			resp.Headers.Add("Session", s.GetSessionID())
			go s.Start()
		}

		return resp, false, nil
	case MethodPause:
		resp := &Response{
			Method:     MethodPause,
			Cseq:       req.Cseq,
			StatusCode: http.StatusOK,
			Headers:    textproto.MIMEHeader{},
		}

		trackID := getTrackID(c.Uri, req.Uri)

		if trackID == "" {
			for _, session := range c.clientSessions {
				go session.Stop()
			}
		} else {
			c.clientSessionLock.Lock()
			s := c.clientSessions[trackID]
			c.clientSessionLock.Unlock()

			if s == nil {
				resp.StatusCode = http.StatusNotFound
				return resp, false, nil
			}
			resp.Headers.Add("Session", s.GetSessionID())
			go s.Stop()
		}

		return resp, false, nil
	case MethodTeardown:
		resp := &Response{
			Method:     MethodOption,
			Cseq:       req.Cseq,
			StatusCode: http.StatusOK,
			Headers:    textproto.MIMEHeader{},
		}
		resp.Headers.Add("Session", req.SessionID)
		return resp, true, nil
	default:
		return nil, false, fmt.Errorf("unsupport method [%s]", req.Method)
	}
}
