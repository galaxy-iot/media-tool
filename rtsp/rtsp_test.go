package rtsp_test

import (
	"testing"
	"time"

	"github.com/galaxy-iot/media-tool/rtsp"
	"github.com/wh8199/log"
)

func TestNewClient(t *testing.T) {
	c, err := rtsp.DialTimeout("rtsp://172.21.84.107/screenlive", 10*time.Second)
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
}

func TestReadResponse(t *testing.T) {
	c, err := rtsp.DialTimeout("rtsp://172.21.84.107/screenlive", 10*time.Second)
	if err != nil {
		t.Error(err)
		return
	}
	defer c.Close()

	req := rtsp.Request{
		Method: "OPTIONS",
		Uri:    "rtsp://172.21.84.107/screenlive",
	}

	if err = c.WriteRequest(req); err != nil {
		return
	}

	resp, err := c.ReadResponse()
	if err != nil {
		return
	}

	t.Log(resp)
}
