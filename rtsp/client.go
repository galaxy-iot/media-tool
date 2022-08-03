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

	"github.com/galaxy-iot/media-tool/av"
	"github.com/galaxy-iot/media-tool/rtp"
	"github.com/galaxy-iot/media-tool/sdp"
	"github.com/galaxy-iot/media-tool/util"
	"github.com/wh8199/log"
)

var (
	ErrUnsupportMethod = errors.New("unsupport method")
	ErrInvalidControl  = errors.New("invalid control id")
)

type Transport byte

const (
	UdpTransport          Transport = 1
	TcpTransport          Transport = 2
	UdpMulticastTransport Transport = 3
)

type Mode byte

const (
	ModePlay   Mode = 1
	ModeRecord Mode = 2
)

func (m Mode) String() string {
	switch m {
	case ModePlay:
		return "play"
	case ModeRecord:
		return "record"
	default:
		return ""
	}
}

type Config struct {
	URL        string
	Transport  Transport
	Timeout    time.Duration
	Verb       bool
	OnAVPacket func(avPacket *av.AVPacket) error
}

type Client struct {
	Config
	url    *url.URL
	conn   *connWithTimeout
	brconn *bufio.Reader
	cseq   uint
	medias []*sdp.Media

	ctx        context.Context
	cancelFunc context.CancelFunc

	sessions     map[string]Session
	sessionsLock sync.RWMutex
	cseqs        []string

	disConnectChan chan struct{}
	// current controls
	// used for reconnecting
	currentControls []string
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

type RTPResponse struct {
	ControlID string `json:"controlId"`
	Payload   []byte `json:"payload"`
}

func dial(cfg Config) (*connWithTimeout, *url.URL, error) {
	parsedUrl, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, nil, err
	}

	if _, _, err := net.SplitHostPort(parsedUrl.Host); err != nil {
		parsedUrl.Host = parsedUrl.Host + ":554"
	}

	dailer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dailer.Dial("tcp", parsedUrl.Host)
	if err != nil {
		return nil, nil, err
	}

	parsedUrl.User = nil
	return &connWithTimeout{Conn: conn}, parsedUrl, nil
}

func DialTimeout(cfg Config) (c *Client, err error) {
	connt, parsedUrl, err := dial(cfg)
	if err != nil {
		return nil, err
	}

	c = &Client{
		conn:   connt,
		brconn: bufio.NewReaderSize(connt, 256),
		url:    parsedUrl,
		Config: cfg,

		sessions:     map[string]Session{},
		sessionsLock: sync.RWMutex{},

		disConnectChan:  make(chan struct{}),
		currentControls: []string{},
	}

	c.ctx, c.cancelFunc = context.WithCancel(context.Background())
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

func (c *Client) setup(control string) error {
	uri := ""
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

func (c *Client) Setup(controls ...string) error {
	for _, control := range controls {
		if err := c.setup(control); err != nil {
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

		controlID := ""
		for c, s := range c.sessions {
			p1, p2 := s.GetInterleavedPort()
			if p1 == int(header[1]) || p2 == int(header[1]) {
				controlID = c
				break
			}
		}

		if controlID == "" {
			return nil, ErrInvalidControl
		}

		payloadLen := int(binary.BigEndian.Uint16(header[2:]))
		payload := make([]byte, payloadLen)

		if _, err := io.ReadFull(c.brconn, payload); err != nil {
			return nil, err
		}

		return &RTPResponse{
			ControlID: controlID,
			Payload:   payload,
		}, nil
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

func (c *Client) optionRequest() error {
	req := Request{
		Method:  MethodOption,
		Uri:     c.URL,
		Headers: textproto.MIMEHeader{},
	}

	return c.WriteRequest(req)
}

func (c *Client) Options() error {
	if err := c.optionRequest(); err != nil {
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
	if resp.ContentLength == 0 || resp.StatusCode != 200 {
		return fmt.Errorf("rtsp: Describe failed, StatusCode=%d", resp.StatusCode)
	}

	_, medias, err := sdp.Parse(util.Bytes2String(resp.Body))
	if err != nil {
		return err
	}

	c.medias = medias

	return nil
}

func (c *Client) OnSetupResponse(resp *Response, control string) error {
	var mediaType av.MediaType = av.Fake
	for _, media := range c.medias {
		if media.Control == control {
			mediaType = media.MediaType
			break
		}
	}

	builder := rtp.GetRTPCodecBuilder(mediaType)
	if builder == nil {
		return fmt.Errorf("invliad or unsupport mediatype")
	}

	codec, err := builder()
	if err != nil {
		return err
	}

	s, err := NewSession(control, resp.Headers.Get("Transport"), resp.SessionID, false, codec)
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
		return ErrUnsupportMethod
	}
}

func (c *Client) startReadRoutine() {
	c.currentControls = c.currentControls[:0]

	c.sessionsLock.Lock()
	for control := range c.sessions {
		c.currentControls = append(c.currentControls, control)
	}
	c.sessionsLock.Unlock()

	ticker := time.NewTimer(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.optionRequest(); err != nil {
				log.Error(err)
				c.disConnectChan <- struct{}{}
				return
			}
		default:
			ret, err := c.ReadResponseOrInterleavedFrame()
			if err != nil {
				log.Error(err)
				c.disConnectChan <- struct{}{}
				return
			}

			switch ret := ret.(type) {
			case *Response:
				if err := c.handlerResponse(ret, ""); err != nil {
					log.Error(err)
					c.disConnectChan <- struct{}{}
					return
				}
			case *RTPResponse:
				c.sessionsLock.Lock()
				s := c.sessions[ret.ControlID]
				c.sessionsLock.Unlock()

				if c.OnAVPacket == nil {
					continue
				}

				codec := s.GetRTPCodec()
				if codec == nil {
					continue
				}

				packet, err := codec.Decode(ret.Payload)
				if err != nil {
					log.Error(err)
					continue
				}

				if err := c.OnAVPacket(packet); err != nil {
					log.Error(err)
					continue
				}
			default:
				c.disConnectChan <- struct{}{}
			}
		}
	}
}

func (c *Client) reconnect() error {
	connt, _, err := dial(c.Config)
	if err != nil {
		return err
	}

	c.conn = connt
	c.brconn = bufio.NewReaderSize(connt, 4096)

	if err := c.Options(); err != nil {
		return err
	}

	if _, err := c.Describe(); err != nil {
		return err
	}

	// close old sessions
	for _, s := range c.sessions {
		s.Stop()
	}

	if err := c.Setup(c.currentControls...); err != nil {
		return err
	}

	return c.Play(nil)
}

func (c *Client) disConnectRoutine() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.disConnectChan:
			for {
				time.Sleep(5 * time.Second)
				if err := c.reconnect(); err == nil {
					break
				}
			}

			go c.startReadRoutine()
		}
	}
}

func (c *Client) Start() error {
	go c.disConnectRoutine()
	go c.startReadRoutine()

	for _, s := range c.sessions {
		go s.Start()
	}

	return nil
}
