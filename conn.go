package http3

import (
	"context"
	"crypto/tls"
	"log"
	"net"

	"github.com/quic-go/quic-go"
)

func DialEarly(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (quic.EarlyConnection, error) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		log.Panic(err)
	}
	// log.Print(udpConn.LocalAddr())
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	return quic.DialEarly(ctx, udpConn, udpAddr, tlsCfg, cfg)
}
