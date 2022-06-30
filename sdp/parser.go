package sdp

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/galaxy-iot/media-tool/av"
	"github.com/wh8199/log"
)

type Session struct {
	// u=<sessionUrl>
	Uri string
	// s=<sessionname>
	Name string
	// v=<versiob>
	Version string
	// o=<username> <sessionid> <version> <network type> <address type> <address>
	Username       string
	SessionId      string
	SessionVersion string
	NetworkType    string
	AddressType    string
	Address        string
}

type Media struct {
	AVType    string
	Type      av.CodecType
	FPS       int
	TimeScale int
	Control   string
	Rtpmap    int

	SampleRate         int
	ChannelCount       int
	Config             []byte
	SpropParameterSets [][]byte
	SpropVPS           []byte
	SpropSPS           []byte
	SpropPPS           []byte
	PayloadType        int
	SizeLength         int
	IndexLength        int
}

/*

v=0
o=jdoe 2890844526 2890842807 IN IP4 10.47.16.5
s=SDP Seminar
i=A Seminar on the session description protocol
u=http://www.example.com/seminars/sdp.pdf
e=j.doe@example.com (Jane Doe)
p=12345
c=IN IP4 224.2.17.12/127
b=CT:154798
t=2873397496 2873404696
r=7d 1h 0 25h
k=clear:ab8c4df8b8f4as8v8iuy8re
a=recvonly

m=audio 49170 RTP/AVP 0
m=video 51372 RTP/AVP 99
b=AS:66781
k=prompt
a=rtpmap:99 h263-1998/90000
*/

func Parse(content string) (sess *Session, medias []*Media, err error) {
	var media *Media
	sess = &Session{}

	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		////Camera [BUG] a=x-framerate: 25
		if strings.Contains(line, "x-framerate") {
			line = strings.Replace(line, " ", "", -1)
		}

		typeval := strings.SplitN(line, "=", 2)

		if len(typeval) != 2 {
			return nil, nil, fmt.Errorf("invalid sdp line: %s", line)
		}

		switch typeval[0] {
		case "u":
			sess.Uri = typeval[1]
		case "s":
			sess.Name = typeval[1]
		case "v":
			sess.Version = typeval[1]
		case "o":
			fields := strings.Split(typeval[1], " ")
			//o=<username> <sessionid> <version> <network type> <address type> <address>
			// len(fields) = 6
			if len(fields) < 6 {
				continue
			}

			sess.Username = fields[0]
			if fields[0] == "-" {
				sess.Username = ""
			}
			sess.SessionId = fields[1]
			sess.SessionVersion = fields[2]
			sess.NetworkType = fields[3]
			sess.AddressType = fields[4]
			sess.Address = fields[5]
		case "m":
			fields := strings.SplitN(typeval[1], " ", 2)
			//m=audio 49170 RTP/AVP 0
			if len(fields) == 0 {
				continue
			}

			switch fields[0] {
			case "audio", "video":
				media = &Media{AVType: fields[0]}

				// 49170 RTP/AVP 0
				mfields := strings.Split(fields[1], " ")
				if len(mfields) >= 3 {
					media.PayloadType, err = strconv.Atoi(mfields[2])
					if err != nil {
						continue
					}
				}

				switch media.PayloadType {
				case 0:
					media.Type = av.PCM_MULAW
				case 8:
					media.Type = av.PCM_ALAW
				case 96:
					media.Type = av.H264
				case 97:
					media.Type = av.AAC
				default:
					continue
				}

				medias = append(medias, media)
			default:
				media = nil
			}
		case "a":
			// this is a invalid media, so skip it
			if media == nil {
				continue
			}

			// control:realvideo
			// rtpmap:96 H264/90000
			// framerate:15
			// a=rtpmap:97 mpeg4-generic/24000/1
			keyval := strings.SplitN(typeval[1], ":", 2)
			if len(keyval) >= 2 {
				key := keyval[0]
				val := keyval[1]
				switch key {
				case "control":
					media.Control = val
				case "rtpmap":
					vals := strings.SplitN(val, " ", 2)

					if len(vals) == 1 {
						continue
					}

					val = vals[1]
					// TODO
					keyval = strings.Split(val, "/")
					if len(keyval) >= 2 {
						key := keyval[0]
						switch strings.ToUpper(key) {
						case "L16":
							media.Type = av.PCM
						case "OPUS", "MPEG4-GENERIC", "H264":
							media.ChannelCount = 1
							if i, err := strconv.Atoi(keyval[1]); err == nil {
								media.SampleRate = i
							}

							if len(keyval) > 2 {
								if i, err := strconv.Atoi(keyval[2]); err == nil {
									media.ChannelCount = i
								}
							}
						case "JPEG":
							media.Type = av.JPEG
						case "H265":
							media.Type = av.H265
						case "HEVC":
							media.Type = av.H265
						case "PCMA":
							media.Type = av.PCM_ALAW
						case "PCMU":
							media.Type = av.PCM_MULAW
						}

						if i, err := strconv.Atoi(keyval[1]); err == nil {
							media.TimeScale = i
						}
					}
				case "x-framerate":
					media.FPS, err = strconv.Atoi(val)
					if err != nil {
						continue
					}
				case "fmtp":
					//a=fmtp:96 profile-level-id=4D4015;sprop-parameter-sets=Z01AFZZWCwSbCEiAAAH0AAAw1DBgAHP2AOg1cABQ,aO88gA==;packetization-mode=1

					// remove fmtp:96
					fields := strings.SplitN(val, " ", 2)

					if len(fields) > 1 {
						keyval = strings.Split(fields[1], ";")

						for _, field := range keyval {
							keyval := strings.SplitN(field, "=", 2)
							if len(keyval) == 2 {
								key := strings.TrimSpace(keyval[0])
								val := keyval[1]
								switch key {
								case "config":
									media.Config, _ = hex.DecodeString(val)
								case "sizelength":
									media.SizeLength, _ = strconv.Atoi(val)
								case "indexlength":
									media.IndexLength, _ = strconv.Atoi(val)
								case "sprop-vps":
									val, err := base64.StdEncoding.DecodeString(val)
									if err == nil {
										media.SpropVPS = val
									} else {
										log.Println("SDP: decode vps error", err)
									}
								case "sprop-sps":
									val, err := base64.StdEncoding.DecodeString(val)
									if err == nil {
										media.SpropSPS = val
									} else {
										log.Println("SDP: decode sps error", err)
									}
								case "sprop-pps":
									val, err := base64.StdEncoding.DecodeString(val)
									if err == nil {
										media.SpropPPS = val
									} else {
										log.Println("SDP: decode pps error", err)
									}
								case "sprop-parameter-sets":
									fields := strings.Split(val, ",")
									for _, field := range fields {
										val, _ := base64.StdEncoding.DecodeString(field)
										media.SpropParameterSets = append(media.SpropParameterSets, val)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return
}
