package codec

import (
	"sync"

	"github.com/galaxy-iot/media-tool/av"
)

type CodecBuilder func() (Codec, error)

var (
	codecBuilder     = map[av.MediaType]CodecBuilder{}
	codecBuilderLock = sync.RWMutex{}
)

func RegisterCodecBuilder(mediaType av.MediaType, builder CodecBuilder) {
	codecBuilderLock.Lock()
	defer codecBuilderLock.Unlock()
	codecBuilder[mediaType] = builder
}

func GetCodecBuilder(mediaType av.MediaType) CodecBuilder {
	codecBuilderLock.RLock()
	defer codecBuilderLock.RUnlock()
	return codecBuilder[mediaType]
}

type Codec interface {
	Decode(bytes []byte) (*av.AVPacket, error)
}
