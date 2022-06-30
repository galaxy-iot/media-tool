package rtsp_test

import (
	"sync"
	"testing"
	"time"

	"github.com/galaxy-iot/media-tool/rtsp"
	"github.com/wh8199/log"
)

func TestNewClient(t *testing.T) {
	c, err := rtsp.DialTimeout(rtsp.Config{
		URL:       "rtsp://172.21.84.107/screenlive",
		Timeout:   10 * time.Second,
		Transport: rtsp.UdpTransport,
	})
	if err != nil {
		t.Error(err)
		return
	}
	defer c.Close()

	if err := c.Options(); err != nil {
		t.Error(err)
		return
	}

	medias, err := c.Describe()
	if err != nil {
		t.Error(err)
		return
	}

	for _, media := range medias {
		log.Info(media)
	}

	if err := c.Setup(medias[0]); err != nil {
		t.Error(err)
		return
	}

	if err := c.Play(); err != nil {
		t.Error(err)
		return
	}

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}