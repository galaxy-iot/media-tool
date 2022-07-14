package main

import (
	"sync"
	"time"

	"github.com/galaxy-iot/media-tool/rtsp"
	"github.com/wh8199/log"
)

func main() {
	c, err := rtsp.DialTimeout(rtsp.Config{
		URL:       "rtsp://192.168.123.197/myapp/screenlive",
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

	if err := c.Play(nil); err != nil {
		log.Fatal(err)
	}

	c.Start()

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}
