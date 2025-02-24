package rtsp

import (
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
)

type Option = func(*Host) error

type PacketisationMode uint8

const (
	PacketisationMode0 PacketisationMode = 0
	PacketisationMode1 PacketisationMode = 1
	PacketisationMode2 PacketisationMode = 2
)

func WithH264Options(packetisationMode PacketisationMode, sps, pps []byte) Option {
	return func(host *Host) error {
		host.description.Medias = append(host.description.Medias, &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.H264{
				PayloadTyp:        96,
				PacketizationMode: int(packetisationMode),
				SPS:               sps,
				PPS:               pps,
			}},
		})

		return nil
	}
}

func WithOpusOptions(channelCount int) Option {
	return func(host *Host) error {
		host.description.Medias = append(host.description.Medias, &description.Media{
			Type: description.MediaTypeAudio,
			Formats: []format.Format{&format.Opus{
				PayloadTyp:   111,
				ChannelCount: channelCount,
			}},
		})
		return nil
	}
}
