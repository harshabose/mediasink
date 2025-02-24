package udp

import (
	"errors"
	"fmt"
	"github.com/pion/rtp"
	"net"
)

type Host struct {
	laddr *net.UDPAddr
	raddr *net.UDPAddr
	conn  *net.UDPConn
}

func CreateHost(port int) (*Host, error) {
	var (
		err   error = nil
		laddr *net.UDPAddr
		raddr *net.UDPAddr
	)

	if laddr, err = net.ResolveUDPAddr("udp4", "127.0.0.1:"); err != nil {
		return nil, err
	}
	if raddr, err = net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		return nil, err
	}

	host := &Host{
		laddr: laddr,
		raddr: raddr,
	}

	if host.conn, err = net.DialUDP("udp", host.laddr, host.raddr); err != nil {
		return nil, err
	}

	return host, nil
}

func (host *Host) Write(packet *rtp.Packet) error {
	var (
		buff   []byte = make([]byte, 1500)
		nWrite int    = 0
		nRead  int    = 0
		err    error  = nil
	)

	if nRead, err = packet.MarshalTo(buff); err != nil {
		return err
	}

	if nWrite, err = host.conn.Write(buff[:nRead]); err != nil {
		return err
	}

	if nWrite != nRead {
		return errors.New("write length != read length")
	}

	return nil
}
