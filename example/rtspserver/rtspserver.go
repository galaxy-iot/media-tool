package main

import (
	"sync"

	"github.com/galaxy-iot/media-tool/rtsp"
	"github.com/wh8199/log"
)

func main() {
	s, err := rtsp.Listen(8000)
	if err != nil {
		panic(err)
	}
	s.Start()

	log.Info("start to handle rtsp connections")

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}
