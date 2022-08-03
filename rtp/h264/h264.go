package h264

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/galaxy-iot/media-tool/av"
	"github.com/galaxy-iot/media-tool/codec"
	_ "github.com/galaxy-iot/media-tool/codec/h264"
	"github.com/pion/rtp"

	rtpcodec "github.com/galaxy-iot/media-tool/rtp"
)

var (
	ErrInvalidH264Packet = errors.New("invalid h264 packet")
)

func init() {
	rtpcodec.RegisterRTPCodecBuilder(av.H264, NewH264RTPCode)
}

type h264RTPDecode struct {
	fuaStart bool
	cacheBuf bytes.Buffer

	h264Codec codec.Codec
}

func NewH264RTPCode() (rtpcodec.RTPCodec, error) {
	codec, err := codec.GetCodecBuilder(av.H264)()
	if err != nil {
		return nil, err
	}

	return &h264RTPDecode{
		cacheBuf:  bytes.Buffer{},
		fuaStart:  false,
		h264Codec: codec,
	}, nil
}

func (h *h264RTPDecode) Decode(bytes []byte) (*av.AVPacket, error) {
	packet := &rtp.Packet{}
	if err := packet.Unmarshal(bytes); err != nil {
		return nil, err
	}

	if len(packet.Payload) < 1 {
		return nil, ErrInvalidH264Packet
	}

	// get the type of nalu
	naluType := packet.Payload[0] & 0x1f

	switch naluType {
	case 28: // FU-A
		if len(packet.Payload) < 2 {
			return nil, ErrInvalidH264Packet
		}

		return h.decodeFUA(packet)
	case 24: // STAP-A
		//return h.decodeSTAPA(packet)
	default:
		return h.decodeSingle(packet)
	}

	return nil, fmt.Errorf("unsupport nalu type '%d', %d", naluType, packet.Payload[0])
}

func (h *h264RTPDecode) decodeSTAPA(p *rtp.Packet) ([][]byte, error) {
	/*
		0                   1                   2                   3
		0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|                          RTP Header                           |
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|STAP-A NAL HDR |         NALU 1 Size           | NALU 1 HDR    |
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|                         NALU 1 Data                           |
		:                                                               :
		+               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|               | NALU 2 Size                   | NALU 2 HDR    |
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|                         NALU 2 Data                           |
		:                                                               :
		|                               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|                               :...OPTIONAL RTP padding        |
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

		Figure 7.  An example of an RTP packet including an STAP-A
		containing two single-time aggregation units
	*/

	return nil, nil
}

func (h *h264RTPDecode) decodeSingle(p *rtp.Packet) (*av.AVPacket, error) {
	return h.h264Codec.Decode(p.Payload)
}

func (h *h264RTPDecode) decodeFUA(p *rtp.Packet) (*av.AVPacket, error) {
	if len(p.Payload) < 2 {
		return nil, ErrInvalidH264Packet
	}

	/*
		0                   1                   2                   3
		0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		| FU indicator  |   FU header   |                               |
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+                               |
		|                                                               |
		|                         FU payload                            |
		|                                                               |
		|                               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		|                               :...OPTIONAL RTP padding        |
		+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
		Figure 14.  RTP payload format for FU-A

		The FU indicator octet has the following format:
		+---------------+
		|0|1|2|3|4|5|6|7|
		+-+-+-+-+-+-+-+-+
		|F|NRI|  Type   |
		+---------------+


		The FU header has the following format:
		+---------------+
		|0|1|2|3|4|5|6|7|
		+-+-+-+-+-+-+-+-+
		|S|E|R|  Type   |
		+---------------+

		S: 1 bit
		When set to one, the Start bit indicates the start of a fragmented
		NAL unit.  When the following FU payload is not the start of a
		fragmented NAL unit payload, the Start bit is set to zero.

		E: 1 bit
		When set to one, the End bit indicates the end of a fragmented NAL
		unit, i.e., the last byte of the payload is also the last byte of
		the fragmented NAL unit.  When the following FU payload is not the
		last fragment of a fragmented NAL unit, the End bit is set to
		zero.

		R: 1 bit
		The Reserved bit MUST be equal to 0 and MUST be ignored by the
		receiver.

		Type: 5 bits
		The NAL unit payload type as defined in table 7-1 of [1].
	*/

	naluType := p.Payload[1] & 0x1F

	isStart := p.Payload[1]&0b10000000 == 0b10000000
	isEnd := p.Payload[1]&0b01000000 == 0b01000000

	indicator := p.Payload[0]

	indicator &= 0b11100000
	indicator |= naluType

	if p.Marker || isEnd {
		// end of a frame
		h.cacheBuf.Write(p.Payload[2:])

		ret := make([]byte, h.cacheBuf.Len())
		copy(ret, h.cacheBuf.Bytes())
		h.cacheBuf.Reset()

		return h.h264Codec.Decode(ret)
	} else {
		indicator = indicator & 0b11100000
		indicator = indicator | naluType

		if isStart {
			h.cacheBuf.WriteByte(indicator)
		}

		h.cacheBuf.Write(p.Payload[2:])
	}

	return nil, nil
}
