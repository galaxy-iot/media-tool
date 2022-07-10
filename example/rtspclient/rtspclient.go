package main

import (
	"sync"
	"time"

	"github.com/galaxy-iot/media-tool/rtsp"
	"github.com/wh8199/log"
)

func main() {
	c, err := rtsp.DialTimeout(rtsp.Config{
		URL:       "rtsp://172.21.84.107/screenlive",
		Timeout:   10 * time.Second,
		Transport: rtsp.TcpTransport,
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

	if err := c.Setup(medias[0]); err != nil {
		log.Fatal(err)
	}

	if err := c.Play(); err != nil {
		log.Fatal(err)
	}

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}
