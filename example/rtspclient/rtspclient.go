package main

import (
	"sync"
	"time"

	"github.com/galaxy-iot/media-tool/av"
	"github.com/galaxy-iot/media-tool/rtsp"
	"github.com/wh8199/log"

	"net/http"
	_ "net/http/pprof"

	_ "github.com/galaxy-iot/media-tool/rtp/h264"
)

func main() {
	go func() {
		http.ListenAndServe(":6060", nil)
	}()

	c, err := rtsp.DialTimeout(rtsp.Config{
		URL:       "rtsp://172.21.84.105/myapp/screenlive",
		Timeout:   10 * time.Second,
		Transport: rtsp.TcpTransport,
		OnAVPacket: func(avPacket *av.AVPacket) error {
			if avPacket == nil {
				return nil
			}

			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	if err := c.Options(); err != nil {
		log.Fatal(err)
	}

	medias, err := c.Describe()
	if err != nil {
		log.Fatal(err)
	}

	for _, media := range medias {
		log.Info(media)
	}

	if err := c.Setup(medias[0].Control); err != nil {
		log.Fatal(err)
	}

	if err := c.Play(nil); err != nil {
		log.Fatal(err)
	}

	c.Start()

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}
