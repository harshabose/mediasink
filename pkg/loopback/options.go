package loopback

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
)

type Option = func(*LoopBack) error

func WithRandomBindPort(loopback *LoopBack) error {
	var err error
	if loopback.bindPort, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}); err != nil {
		return err
	}

	return nil
}

func WithBindPort(port int) Option {
	return func(loopback *LoopBack) error {
		var err error
		if loopback.bindPort, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}); err != nil {
			return err
		}
		return nil
	}
}

func WithLoopBackPort(port int) Option {
	return func(loopback *LoopBack) error {
		loopback.remote = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
		return nil
	}
}

func WithCallback(f OnMessage) Option {
	return func(loopback *LoopBack) error {
		loopback.SetOnPayloadCallback(f)
		return nil
	}
}

func WithMAVProxy(path string, deviceStr string) Option {
	return func(loopback *LoopBack) error {
		if loopback.bindPort == nil {
			return errors.New("bindPortConn not initialized, call WithBindPort or WithRandomBindPort first")
		}

		port := loopback.bindPort.LocalAddr().(*net.UDPAddr).Port

		// MAVProxy uses --master for the connection string, and --out for output connections
		// Format depends on the device: could be a serial port or network address
		args := []string{
			"--master", deviceStr,
			"--out", fmt.Sprintf("udpout:127.0.0.1:%d", port),
			"--daemon",
		}

		cmd := exec.Command(path, args...)

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		loopback.cmd = cmd

		return nil
	}
}
