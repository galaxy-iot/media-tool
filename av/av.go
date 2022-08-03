package av

import "sync"

type MediaType int

const (
	Fake MediaType = -1
	H264 MediaType = 96
	AAC  MediaType = 97
	PCMA MediaType = 8
	PCMU MediaType = 0
)

var (
	mediaTypes = map[MediaType]string{}
)

func (m MediaType) String() string {
	return mediaTypes[m]
}

type AVType byte

const (
	AVAudio AVType = 0
	AVVideo AVType = 1
)

// RGB 888 format
type Image struct {
	Data   []byte `json:"data"`
	Height int    `json:"height"`
	Width  int    `json:"width"`
}

// PCM format
type Audio struct {
	Sample int    `json:"sample"`
	Data   []byte `json:"data"`
}

type AVPacket struct {
	// audio or video
	AVType AVType
	Audio  Audio
	Image  Image
	// which control(stream) this packet belongs to
	Control string
}

type AVPacketPool struct {
	pool sync.Pool
}

func (a *AVPacketPool) Get() *AVPacket {
	avPacket := a.pool.Get()

	if avPacket == nil {
		return &AVPacket{}
	}

	p := avPacket.(*AVPacket)

	p.Audio.Data = p.Audio.Data[:0]
	p.Image.Data = p.Image.Data[:0]
	p.Image.Height = 0
	p.Image.Width = 0

	return p
}

func (a *AVPacketPool) Put(avPacket *AVPacket) {
	a.pool.Put(avPacket)
}
