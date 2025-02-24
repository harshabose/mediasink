package mediasink

type StreamOption = func(*Stream) error
type HostOption = func(Host) error

func WithHost(host Host) StreamOption {
	return func(stream *Stream) error {
		stream.host = host
		return nil
	}
}
