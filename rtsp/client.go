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
	"time"

	"github.com/galaxy-iot/media-tool/sdp"
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
	Headers     []string
	authHeaders func(method string) []string
	url         *url.URL
	conn        *connWithTimeout
	brconn      *bufio.Reader
	cseq        uint
	session     string

	ctx        context.Context
	cancelFunc context.CancelFunc

	rtpListener  *clientUDPListener
	rtcpListener *clientUDPListener
}

type Request struct {
	Header []string
	Uri    string
	Method string
}

type Response struct {
	StatusCode    int
	StatusMessage string
	Headers       textproto.MIMEHeader
	ContentLength int
	Body          []byte
	Block         []byte
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

	for _, s := range req.Header {
		io.WriteString(buf, s)
		io.WriteString(buf, "\r\n")
	}
	for _, s := range c.Headers {
		io.WriteString(buf, s)
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

func (c *Client) Setup(m *sdp.Media) error {
	uri := ""
	control := m.Control
	if strings.HasPrefix(control, "rtsp://") {
		uri = control
	} else {
		uri = c.URL + "/" + control
	}

	req := Request{Method: "SETUP", Uri: uri}

	switch c.Transport {
	case TcpTransport:
		req.Header = append(req.Header, fmt.Sprintf("Transport: RTP/AVP/TCP;unicast;interleaved=%d-%d",
			0, 1))
	case UdpMulticastTransport:
		c.rtpListener, c.rtcpListener = newClientUDPListenerPair(c, false)
		req.Header = append(req.Header, fmt.Sprintf("Transport: RTP/AVP;multicast;client_port=%d-%d",
			0, 1))
	case UdpTransport:
		c.rtpListener, c.rtcpListener = newClientUDPListenerPair(c, false)
		req.Header = append(req.Header, fmt.Sprintf("Transport: RTP/AVP;unicast;client_port=%d-%d",
			c.rtpListener.localPort, c.rtcpListener.localPort))
	default:
		return fmt.Errorf("unsupport transport")
	}

	if c.session != "" {
		req.Header = append(req.Header, "Session: "+c.session)
	}

	if err := c.WriteRequest(req); err != nil {
		return err
	}

	resp, err := c.ReadResponse()
	if err != nil {
		return err
	}

	if c.Transport == TcpTransport {
		return nil
	}

	transport := resp.Headers.Get("Transport")
	if transport == "" {
		return fmt.Errorf("header [Transport] not found")
	}

	index := strings.Index(transport, "server_port")
	if index < 0 {
		return fmt.Errorf("service port is not provided for header [Transport]")
	}

	transport = transport[index:]
	index = strings.Index(transport, "=")
	if index < 0 {
		return fmt.Errorf("service port is not provided for header [Transport]")
	}
	transport = transport[index+1:]

	ports := strings.Split(transport, "-")
	if len(ports) < 2 {
		return fmt.Errorf("service port is not provided for header [Transport]")
	}

	rtpPort, err := strconv.Atoi(ports[0])
	if err != nil {
		return fmt.Errorf("invalid server port")
	}

	rtcpPort, err := strconv.Atoi(ports[1])
	if err != nil {
		return fmt.Errorf("invalid server port")
	}

	c.rtpListener.setIP(c.conn, rtpPort)
	c.rtcpListener.setIP(c.conn, rtcpPort)

	return nil
}

func (c *Client) ReadResponse() (*Response, error) {
	res := Response{
		Headers: make(textproto.MIMEHeader),
	}

	byts, err := readBytesLimited(c.brconn, ' ', 255)
	if err != nil {
		return nil, err
	}
	proto := byts[:len(byts)-1]

	//rtspProtocol10           = "RTSP/1.0"
	if string(proto) != "RTSP/1.0" {
		return nil, fmt.Errorf("expected '%s', got %v", "RTSP/1.0", proto)
	}

	byts, err = readBytesLimited(c.brconn, ' ', 4)
	if err != nil {
		return nil, err
	}
	statusCodeStr := string(byts[:len(byts)-1])

	statusCode64, err := strconv.Atoi(statusCodeStr)
	if err != nil {
		return nil, fmt.Errorf("unable to parse status code")
	}
	res.StatusCode = statusCode64

	byts, err = readBytesLimited(c.brconn, '\r', 255)
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

	// $ channel length nalu
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
		Method: "DESCRIBE",
		Uri:    c.URL,
		Header: []string{"Accept: application/sdp"},
	}

	if err = c.WriteRequest(req); err != nil {
		return
	}

	res, err := c.ReadResponse()
	if err != nil {
		return
	}

	if res.ContentLength == 0 || res.StatusCode != 200 {
		err = fmt.Errorf("rtsp: Describe failed, StatusCode=%d", res.StatusCode)
		return
	}

	c.session = res.Headers.Get("Session")

	body := string(res.Body)

	_, medias, err := sdp.Parse(body)
	if err != nil {
		return nil, err
	}

	return medias, nil
}

func (c *Client) Options() (err error) {
	req := Request{
		Method: "OPTIONS",
		Uri:    c.URL,
	}

	if c.session != "" {
		req.Header = append(req.Header, "Session: "+c.session)
	}

	if err = c.WriteRequest(req); err != nil {
		return
	}

	if _, err = c.ReadResponse(); err != nil {
		return
	}

	return
}

func (c *Client) Play() error {
	req := Request{
		Method: "PLAY",
		Uri:    c.URL,
	}

	req.Header = append(req.Header, "Session: "+c.session)
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

	if c.Transport == TcpTransport {
		return nil
	}

	go func() {
		if c.rtpListener != nil {
			bytes := make([]byte, 2048)

			for {
				select {
				case <-c.ctx.Done():
					return
				default:
					n, _, err := c.rtpListener.pc.ReadFrom(bytes)
					if err != nil {
						log.Error(err)
						continue
					}

					log.Info(bytes[:n])
				}
			}
		}
	}()

	go func() {
		if c.rtcpListener != nil {
			bytes := make([]byte, 2048)

			for {
				select {
				case <-c.ctx.Done():
					return
				default:
					n, _, err := c.rtcpListener.pc.ReadFrom(bytes)
					if err != nil {
						log.Error(err)
						continue
					}

					log.Info(bytes[:n])
				}
			}
		}
	}()

	return nil
}

func (c *Client) Teardown() (err error) {
	req := Request{
		Method: "TEARDOWN",
		Uri:    c.URL,
	}

	req.Header = append(req.Header, "Session: "+c.session)
	if err = c.WriteRequest(req); err != nil {
		return
	}
	return
}

func (c *Client) Close() (err error) {
	return c.conn.Conn.Close()
}
