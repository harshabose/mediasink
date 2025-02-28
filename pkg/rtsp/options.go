package rtsp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/webrtc/v4"
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

func WithH264OptionsFromRemote(remote *webrtc.TrackRemote) Option {
	return func(host *Host) error {
		sps, pps, err := parseSPSPPS(remote.Codec().SDPFmtpLine)
		if err != nil {
			return err
		}
		packetisationMode, err := parsePacketisationMode(remote.Codec().SDPFmtpLine)
		if err != nil {
			return err
		}

		host.description.Medias = append(host.description.Medias, &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.H264{
				PayloadTyp:        uint8(remote.Codec().PayloadType),
				PacketizationMode: packetisationMode,
				SPS:               sps,
				PPS:               pps,
			}},
		})
		return nil
	}
}

func parseSPSPPS(sdpFmtpLine string) (sps, pps []byte, err error) {
	params := strings.Split(sdpFmtpLine, ";")
	var spropParameterSets string

	for _, param := range params {
		if strings.HasPrefix(param, "sprop-parameter-sets=") {
			spropParameterSets = strings.TrimPrefix(param, "sprop-parameter-sets=")
			break
		}
	}

	if spropParameterSets == "" {
		return nil, nil, errors.New("sprop-parameter-sets not found in SDP fmtp line")
	}

	parameterSets := strings.Split(spropParameterSets, ",")
	if len(parameterSets) != 2 {
		return nil, nil, fmt.Errorf("expected 2 parameter sets (SPS, PPS), but got %d", len(parameterSets))
	}

	spsBase64 := parameterSets[0]
	ppsBase64 := parameterSets[1]

	sps, err = base64.StdEncoding.DecodeString(spsBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode SPS base64: %w", err)
	}

	pps, err = base64.StdEncoding.DecodeString(ppsBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode PPS base64: %w", err)
	}

	return sps, pps, nil
}

func parsePacketisationMode(sdpFmtpLine string) (int, error) {
	params := strings.Split(sdpFmtpLine, ";")
	var packetisationMode string

	for _, param := range params {
		if strings.HasPrefix(param, "packetization-mode=") {
			packetisationMode = strings.TrimPrefix(param, "packetization-mode=")
			break
		}
	}

	if packetisationMode == "" {
		return 0, errors.New("packetization-mode not found in SDP fmtp line")
	}

	mode, err := strconv.Atoi(packetisationMode)
	if err != nil {
		return 0, errors.New("error converting string to int")
	}

	return mode, nil
}
