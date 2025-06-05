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

type HostOption = func(host *NewHost) error

type PacketisationMode uint8

const (
	PacketisationMode0 PacketisationMode = 0
	PacketisationMode1 PacketisationMode = 1
	PacketisationMode2 PacketisationMode = 2
)

func WithH264Options(packetisationMode PacketisationMode, sps, pps []byte) HostOption {
	return func(host *NewHost) error {
		media := &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.H264{
				PayloadTyp:        102, // NOTE: FOLLOWING PION'S CONVENTION
				PacketizationMode: int(packetisationMode),
				SPS:               sps,
				PPS:               pps,
			}},
		}

		host.AppendRTSPMediaDescription(media)

		return nil
	}
}

func WithVP8Option() HostOption {
	return func(host *NewHost) error {
		media := &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.VP8{
				PayloadTyp: 96,
				MaxFR:      nil,
				MaxFS:      nil,
			}},
		}

		host.AppendRTSPMediaDescription(media)

		return nil
	}
}

func WithOpusOptions(channelCount int) HostOption {
	return func(host *NewHost) error {
		media := &description.Media{
			Type: description.MediaTypeAudio,
			Formats: []format.Format{&format.Opus{
				PayloadTyp:   111,
				ChannelCount: channelCount,
			}},
		}

		host.AppendRTSPMediaDescription(media)

		return nil
	}
}

func WithOptionsFromRemote(remote *webrtc.TrackRemote) HostOption {
	if remote.Codec().MimeType == webrtc.MimeTypeH264 {
		return withH264OptionsFromRemote(remote)
	}
	if remote.Codec().MimeType == webrtc.MimeTypeVP8 {
		return withVP8OptionsFromRemote(remote)
	}
	if remote.Codec().MimeType == webrtc.MimeTypeOpus {
		return withOpusOptionsFromRemote(remote)
	}
	return func(host *NewHost) error {
		return errors.New("unknown media codec")
	}
}

func withH264OptionsFromRemote(remote *webrtc.TrackRemote) HostOption {
	return func(host *NewHost) error {
		sps, pps, err := parseSPSPPS(remote.Codec().SDPFmtpLine)

		if err != nil {
			return err
		}
		packetisationMode, err := parsePacketisationMode(remote.Codec().SDPFmtpLine)
		if err != nil {
			return err
		}

		if remote.Codec().PayloadType != 102 {
			return fmt.Errorf("expected payload type to be 102; but got %d", remote.Codec().PayloadType)
		}

		return WithH264Options(PacketisationMode(packetisationMode), sps, pps)(host)
	}
}

func withVP8OptionsFromRemote(remote *webrtc.TrackRemote) HostOption {
	return func(host *NewHost) error {
		if remote.Codec().PayloadType != 96 { // NOTE: FOLLOWING PION'S CONVENTION
			return fmt.Errorf("expected payload type to be 96; but got %d", remote.Codec().PayloadType)
		}

		return WithVP8Option()(host)
	}
}

func withOpusOptionsFromRemote(remote *webrtc.TrackRemote) HostOption {
	return func(host *NewHost) error {
		if remote.Codec().PayloadType != 111 { // NOTE: FOLLOWING PION'S CONVENTION
			return fmt.Errorf("expected payload type to be 111; but got %d", remote.Codec().PayloadType)
		}

		return WithOpusOptions(int(remote.Codec().Channels))(host)
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

	fmt.Println("SPS for remote: ", sps)
	fmt.Println("PPS for remote: ", pps)

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
