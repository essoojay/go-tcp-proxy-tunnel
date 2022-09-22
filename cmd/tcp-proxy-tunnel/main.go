package main

import (
	"flag"
	"fmt"
	proxy "github.com/lutfailham96/go-tcp-proxy-tunnel"
	"net"
	"os"
)

var (
	localAddr        = flag.String("l", "127.0.0.1:8082", "local address")
	remoteAddr       = flag.String("r", "127.0.0.1:443", "remote address")
	serverHost       = flag.String("s", "", "server host address")
	reverseProxyMode = flag.Bool("rp", false, "enable reverse proxy mode")
	localPayload     = flag.String("ip", "", "local TCP payload replacer")
	remotePayload    = flag.String("op", "", "remote TCP payload replacer")
)

func main() {
	flag.Parse()

	lAddr := resolveAddr(*localAddr)
	rAddr := resolveAddr(*remoteAddr)

	if *serverHost != "" {
		_, err := net.ResolveTCPAddr("tcp", *serverHost)
		if err != nil {
			fmt.Printf("Failed to resolve remote address: %s", err)
			return
		}
	}

	listener, err := net.Listen("tcp", lAddr.String())
	if err != nil {
		fmt.Printf("Failed to open local port to listen: %s", err)
		return
	}
	if *reverseProxyMode {
		fmt.Println("Mode: reverse proxy")
	} else {
		fmt.Println("Mode: client proxy")
	}
	fmt.Printf("go-tcp-proxy-tunnel proxing from %v to %v\n", lAddr, rAddr)

	loopListener(listener, lAddr, rAddr)
}

func resolveAddr(addr string) *net.TCPAddr {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		fmt.Printf("Failed to resolve local address: %s", err)
		os.Exit(1)
	}
	return tcpAddr
}

func loopListener(listener net.Listener, lAddr, rAddr *net.TCPAddr) {
	var connId = uint64(0)
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Failed to accept connection '%s'", err)
			return
		}
		connId += 1

		var p *proxy.Proxy
		p = p.New(connId, conn, lAddr, rAddr)
		if *serverHost != "" {
			p.SetServerHost(*serverHost)
		}
		p.SetlPayload(*localPayload)
		p.SetrPayload(*remotePayload)
		p.SetReverseProxy(*reverseProxyMode)
		go p.Start()
	}
}
