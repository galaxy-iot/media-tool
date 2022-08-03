package rtp

import (
	"sync"

	"github.com/galaxy-iot/media-tool/av"
)

type RTPCodec interface {
	Decode(bytes []byte) (*av.AVPacket, error)
}

type RTPCodecBuilder func() (RTPCodec, error)

var (
	rtpCodecBuilder     = map[av.MediaType]RTPCodecBuilder{}
	rtpCodecBuilderLock = sync.RWMutex{}
)

func RegisterRTPCodecBuilder(mediaType av.MediaType, builder RTPCodecBuilder) {
	rtpCodecBuilderLock.Lock()
	defer rtpCodecBuilderLock.Unlock()
	rtpCodecBuilder[mediaType] = builder
}

func GetRTPCodecBuilder(mediaType av.MediaType) RTPCodecBuilder {
	rtpCodecBuilderLock.RLock()
	defer rtpCodecBuilderLock.RUnlock()
	return rtpCodecBuilder[mediaType]
}
