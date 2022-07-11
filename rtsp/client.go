package rtsp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
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
	authHeaders func(method string) []string
	url         *url.URL
	conn        *connWithTimeout
	brconn      *bufio.Reader
	cseq        uint
	session     string
	medias      []*sdp.Media

	ctx        context.Context
	cancelFunc context.CancelFunc

	sessions     map[string]Session
	sessionsLock sync.RWMutex
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
	return
}

func (c *Client) WriteRequest(req Request) (err error) {
	c.conn.Timeout = c.Timeout
	c.cseq++

	buf := &bytes.Buffer{}

	fmt.Fprintf(buf, "%s %s RTSP/1.0\r\n", req.Method, req.Uri)
	fmt.Fprintf(buf, "CSeq: %d\r\n", c.cseq)

	if c.authHeaders != nil {
		headers := c.authHeaders(req.Method)
		for _, s := range headers {
			io.WriteString(buf, s)
			io.WriteString(buf, "\r\n")
		}
	}

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

func (c *Client) handleResp(res *Response) (err error) {
	if sess := res.Headers.Get("Session"); sess != "" && c.session == "" {
		if fields := strings.Split(sess, ";"); len(fields) > 0 {
			c.session = fields[0]
		}
	}

	if res.StatusCode == 401 {
		if err = c.handle401(res); err != nil {
			return
		}
	}

	return
}

func (c *Client) handle401(res *Response) (err error) {
	/*
		RTSP/1.0 401 Unauthorized
		CSeq: 2
		Date: Wed, May 04 2016 10:10:51 GMT
		WWW-Authenticate: Digest realm="LIVE555 Streaming Media", nonce="c633aaf8b83127633cbe98fac1d20d87"
	*/
	authval := res.Headers.Get("WWW-Authenticate")
	hdrval := strings.SplitN(authval, " ", 2)
	var realm, nonce string

	if len(hdrval) == 2 {
		for _, field := range strings.Split(hdrval[1], ",") {
			field = strings.Trim(field, ", ")
			if keyval := strings.Split(field, "="); len(keyval) == 2 {
				key := keyval[0]
				val := strings.Trim(keyval[1], `"`)
				switch key {
				case "realm":
					realm = val
				case "nonce":
					nonce = val
				}
			}
		}

		if realm != "" {
			var username string
			var password string

			if c.url.User == nil {
				err = fmt.Errorf("rtsp: no username")
				return
			}
			username = c.url.User.Username()
			password, _ = c.url.User.Password()

			c.authHeaders = func(method string) []string {
				var headers []string
				if nonce == "" {
					headers = []string{
						fmt.Sprintf(`Authorization: Basic %s`, base64.StdEncoding.EncodeToString([]byte(username+":"+password))),
					}
				} else {
					hs1 := md5hash(username + ":" + realm + ":" + password)
					hs2 := md5hash(method + ":" + c.URL)
					response := md5hash(hs1 + ":" + nonce + ":" + hs2)
					headers = []string{fmt.Sprintf(
						`Authorization: Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
						username, realm, nonce, c.URL, response)}
				}
				return headers
			}
		}
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

	s, err := NewSession(control, resp.Headers.Get("Transport"), resp.SessionID, false)
	if err != nil {
		return err
	}

	log.Info(s)

	c.sessionsLock.Lock()
	log.Info("lock")
	c.sessions[control] = s
	log.Info("unlock")
	c.sessionsLock.Unlock()

	log.Info(s)

	return nil
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
	if string(proto) != "RTSP/1.0" {
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

		if key == "Content-Length" {
			contentLength, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid content length: %s", value)
			}

			res.ContentLength = contentLength
		}

		if key == "Session" {
			res.SessionID = value
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

func md5hash(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func (c *Client) Describe() (streams []*sdp.Media, err error) {
	req := Request{
		Method:  "DESCRIBE",
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

	if res.ContentLength == 0 || res.StatusCode != 200 {
		err = fmt.Errorf("rtsp: Describe failed, StatusCode=%d", res.StatusCode)
		return
	}

	c.session = res.Headers.Get("Session")

	_, medias, err := sdp.Parse(util.Bytes2String(res.Body))
	if err != nil {
		return nil, err
	}

	c.medias = medias

	return medias, nil
}

func (c *Client) Options() (err error) {
	req := Request{
		Method:  "OPTIONS",
		Uri:     c.URL,
		Headers: textproto.MIMEHeader{},
	}

	if c.session != "" {
		req.Headers.Add("Session", c.session)
	}

	if err = c.WriteRequest(req); err != nil {
		return
	}

	if _, err = c.ReadResponse(); err != nil {
		return
	}

	return
}

func (c *Client) Play(media *sdp.Media) error {
	url := c.URL

	if media != nil {
		url = url + "/" + media.Control
	}

	req := Request{
		Method:  "PLAY",
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

	if _, err := c.ReadResponse(); err != nil {
		return err
	}

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

				_ = ret
				log.Info(ret)
			}
		}
	}()

	return nil
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

	req.Headers.Add("Session", c.session)
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

func (c *Client) Start() error {

	return nil
}
