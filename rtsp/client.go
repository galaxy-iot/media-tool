package rtsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/galaxy-iot/media-tool/sdp"
	"github.com/galaxy-iot/media-tool/util"
	"github.com/wh8199/log"
)

var (
	ErrUnsupportMethod = errors.New("unsupport method")
)

type Transport byte

const (
	UdpTransport          Transport = 1
	TcpTransport          Transport = 2
	UdpMulticastTransport Transport = 3
)

type Config struct {
	URL       string
	Transport Transport
	Timeout   time.Duration
}

type Client struct {
	Config
	url    *url.URL
	conn   *connWithTimeout
	brconn *bufio.Reader
	cseq   uint
	//session string
	medias []*sdp.Media

	ctx        context.Context
	cancelFunc context.CancelFunc

	sessions     map[string]Session
	sessionsLock sync.RWMutex
	cseqs        []string
}

type Request struct {
	Uri           string
	Method        string
	Headers       textproto.MIMEHeader
	Version       string
	ContentLength int
	Body          []byte
	Cseq          int
	SessionID     string
}

type Response struct {
	Method        string
	StatusCode    int
	StatusMessage string
	Headers       textproto.MIMEHeader
	ContentLength int
	Body          []byte
	Block         []byte
	Cseq          int
	SessionID     string
}

func DialTimeout(cfg Config) (c *Client, err error) {
	var URL *url.URL
	if URL, err = url.Parse(cfg.URL); err != nil {
		return
	}

	if _, _, err := net.SplitHostPort(URL.Host); err != nil {
		URL.Host = URL.Host + ":554"
	}

	dailer := net.Dialer{Timeout: cfg.Timeout}
	var conn net.Conn
	if conn, err = dailer.Dial("tcp", URL.Host); err != nil {
		return
	}

	u2 := *URL
	u2.User = nil
	connt := &connWithTimeout{Conn: conn}

	c = &Client{
		conn:   connt,
		brconn: bufio.NewReaderSize(connt, 256),
		url:    URL,
		Config: Config{
			URL:       u2.String(),
			Transport: cfg.Transport,
			Timeout:   cfg.Timeout,
		},

		sessions:     map[string]Session{},
		sessionsLock: sync.RWMutex{},
	}

	c.ctx, c.cancelFunc = context.WithCancel(context.Background())

	go func() {

	}()
	return
}

func (c *Client) WriteRequest(req Request) (err error) {
	c.conn.Timeout = c.Timeout
	c.cseq++

	c.cseqs = append(c.cseqs, req.Method)

	buf := &bytes.Buffer{}

	fmt.Fprintf(buf, "%s %s RTSP/1.0\r\n", req.Method, req.Uri)
	fmt.Fprintf(buf, "CSeq: %d\r\n", c.cseq)

	for key := range req.Headers {
		io.WriteString(buf, key)
		io.WriteString(buf, ": ")
		io.WriteString(buf, req.Headers.Get(key))
		io.WriteString(buf, "\r\n")
	}

	io.WriteString(buf, "\r\n")
	bufout := buf.Bytes()

	if _, err = c.conn.Write(bufout); err != nil {
		return
	}

	return
}

func (c *Client) setup(m *sdp.Media) error {
	uri := ""
	control := m.Control
	if strings.HasPrefix(control, "rtsp://") {
		uri = control
	} else {
		uri = c.URL + "/" + control
	}

	req := Request{
		Method:  "SETUP",
		Uri:     uri,
		Headers: textproto.MIMEHeader{},
	}

	transport := ""

	switch c.Transport {
	case TcpTransport:
		transport = fmt.Sprintf("RTP/AVP/TCP;unicast;interleaved=%d-%d", 0, 1)
	case UdpMulticastTransport:
		rtpPort, rtcpPort := allocPortPair()
		transport = fmt.Sprintf("RTP/AVP;multicast;client_port=%d-%d",
			rtpPort, rtcpPort)
	case UdpTransport:
		rtpPort, rtcpPort := allocPortPair()
		transport = fmt.Sprintf("RTP/AVP;unicast;client_port=%d-%d",
			rtpPort, rtcpPort)
	default:
		return fmt.Errorf("unsupport transport")
	}

	req.Headers.Add("Transport", transport)

	if err := c.WriteRequest(req); err != nil {
		return err
	}

	resp, err := c.ReadResponse()
	if err != nil {
		return err
	}

	return c.OnSetupResponse(resp, control)
}

func (c *Client) Setup(medias ...*sdp.Media) error {
	for _, media := range medias {
		if err := c.setup(media); err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) ReadResponse() (*Response, error) {
	res := Response{
		Headers: make(textproto.MIMEHeader),
	}

	byts, _, err := readBytesLimited(c.brconn, ' ', 255)
	if err != nil {
		return nil, err
	}
	proto := byts[:len(byts)-1]

	//rtspProtocol10           = "RTSP/1.0"
	if util.Bytes2String(proto) != "RTSP/1.0" {
		return nil, fmt.Errorf("expected '%s', got %v", "RTSP/1.0", proto)
	}

	byts, _, err = readBytesLimited(c.brconn, ' ', 4)
	if err != nil {
		return nil, err
	}
	statusCodeStr := string(byts[:len(byts)-1])

	statusCode64, err := strconv.Atoi(statusCodeStr)
	if err != nil {
		return nil, fmt.Errorf("unable to parse status code")
	}
	res.StatusCode = statusCode64

	byts, _, err = readBytesLimited(c.brconn, '\r', 255)
	if err != nil {
		return nil, err
	}

	res.StatusMessage = string(byts[:len(byts)-1])
	if len(res.StatusMessage) == 0 {
		return nil, fmt.Errorf("empty status message")
	}

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

			res.ContentLength = contentLength
		}

		if strings.ToLower(key) == "session" {
			res.SessionID = value
		}

		if strings.ToLower(key) == "cseq" {
			cseq, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid cseq: %s", value)
			}

			res.Cseq = cseq
		}

		res.Headers.Add(key, value)
	}

	if res.ContentLength > 0 {
		res.Body = make([]byte, res.ContentLength)
		if _, err := io.ReadFull(c.brconn, res.Body); err != nil {
			return nil, err
		}
	}

	return &res, nil
}

func (c *Client) ReadResponseOrInterleavedFrame() (interface{}, error) {
	b, err := c.brconn.ReadByte()
	if err != nil {
		return nil, err
	}
	c.brconn.UnreadByte()

	// [$(1 byte) channel(1 byte) length(2 byte)] nalu
	// 36 is $, so this is a inter leaved frame
	if b == 36 {
		header := make([]byte, 4)
		if _, err := io.ReadFull(c.brconn, header); err != nil {
			return nil, err
		}

		payloadLen := int(binary.BigEndian.Uint16(header[2:]))
		payload := make([]byte, payloadLen)

		if _, err := io.ReadFull(c.brconn, payload); err != nil {
			return nil, err
		}

		return payload, nil
	}

	return c.ReadResponse()
}

func (c *Client) Describe() (streams []*sdp.Media, err error) {
	req := Request{
		Method:  MethodDescribe,
		Uri:     c.URL,
		Headers: textproto.MIMEHeader{},
	}

	req.Headers.Add("Accept", "application/sdp")

	if err = c.WriteRequest(req); err != nil {
		log.Error(err)
		return
	}

	res, err := c.ReadResponse()
	if err != nil {
		log.Error(err)
		return
	}

	if err := c.handlerResponse(res, ""); err != nil {
		return nil, err
	}

	return c.medias, nil
}

func (c *Client) Options() error {
	req := Request{
		Method:  MethodOption,
		Uri:     c.URL,
		Headers: textproto.MIMEHeader{},
	}

	//if c.session != "" {
	//	req.Headers.Add("Session", c.session)
	//}

	if err := c.WriteRequest(req); err != nil {
		return err
	}

	resp, err := c.ReadResponse()
	if err != nil {
		return err
	}

	return c.handlerResponse(resp, "")
}

func (c *Client) Play(media *sdp.Media) error {
	url := c.URL

	if media != nil {
		url = url + "/" + media.Control
	}

	req := Request{
		Method:  MethodPlay,
		Uri:     url,
		Headers: textproto.MIMEHeader{},
	}

	if media != nil {
		c.sessionsLock.Lock()
		s := c.sessions[media.Control]
		c.sessionsLock.Unlock()
		req.Headers.Add("Session", s.GetSessionID())
	}

	if err := c.WriteRequest(req); err != nil {
		return err
	}

	resp, err := c.ReadResponse()
	if err != nil {
		return err
	}

	return c.handlerResponse(resp, "")
}

func (c *Client) Pause(media *sdp.Media) error {
	return nil
}

func (c *Client) Teardown() (err error) {
	req := Request{
		Method:  "TEARDOWN",
		Uri:     c.URL,
		Headers: textproto.MIMEHeader{},
	}

	//req.Headers.Add("Session", c.session)
	if err = c.WriteRequest(req); err != nil {
		return
	}
	return
}

func (c *Client) Close() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}

	c.sessionsLock.Lock()
	for _, session := range c.sessions {
		session.Stop()
	}
	c.sessionsLock.Unlock()

	if c.conn != nil && c.conn.Conn != nil {
		c.conn.Conn.Close()
	}
}

// response handlers
func (c *Client) OnPlayResponse(resp *Response) error {
	return nil
}

func (c *Client) OnOptionsReponse(resp *Response) error {
	return nil
}

func (c *Client) OnDescribeResponse(resp *Response) error {
	log.Info(resp.Headers)
	log.Info(resp.ContentLength)
	log.Info(resp.Body)

	if resp.ContentLength == 0 || resp.StatusCode != 200 {
		return fmt.Errorf("rtsp: Describe failed, StatusCode=%d", resp.StatusCode)
	}

	//c.session = resp.Headers.Get("Session")

	_, medias, err := sdp.Parse(util.Bytes2String(resp.Body))
	if err != nil {
		return err
	}

	c.medias = medias

	return nil
}

func (c *Client) OnSetupResponse(resp *Response, control string) error {
	s, err := NewSession(control, resp.Headers.Get("Transport"), resp.SessionID, false)
	if err != nil {
		return err
	}

	c.sessionsLock.Lock()
	c.sessions[control] = s
	c.sessionsLock.Unlock()
	return nil
}

func (c *Client) handlerResponse(resp *Response, control string) error {
	resp.Method = c.cseqs[resp.Cseq-1]

	switch resp.Method {
	case MethodOption:
		return c.OnOptionsReponse(resp)
	case MethodDescribe:
		return c.OnDescribeResponse(resp)
	case MethodSetup:
		return c.OnSetupResponse(resp, control)
	case MethodPlay:
		return c.OnPlayResponse(resp)
	case MethodPause:
		return nil
	case MethodTeardown:
		return nil
	default:
		log.Info(resp.Method)
		return ErrUnsupportMethod
	}
}

func (c *Client) Start() error {
	go func() {
		for {
			select {
			case <-c.ctx.Done():
				return
			default:
				ret, err := c.ReadResponseOrInterleavedFrame()
				if err != nil {
					log.Error(err)
				}

				switch ret.(type) {
				case *Response:
					c.handlerResponse(ret.(*Response), "")
				default:
					p := ret.([]byte)
					log.Info(p)
				}
			}
		}
	}()

	for _, s := range c.sessions {
		go s.Start()
	}

	return nil
}
