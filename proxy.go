package proxy

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Host struct {
	hostName string
	port     uint64
}

type Config struct {
	BufferSize          uint64
	ServerProxyMode     bool
	ProxyInfo           string
	LocalAddress        string
	RemoteAddress       string
	LocalAddressTCP     *net.TCPAddr
	RemoteAddressTCP    *net.TCPAddr
	ServerHost          string
	DisableServerResolv bool
	ConnectionInfo      string
	TLSEnabled          bool
	SNIHost             string
	LocalPayload        string
	RemotePayload       string
}

type Proxy struct {
	conn                 net.Conn
	lConn                net.Conn
	rConn                net.Conn
	lAddr                *net.TCPAddr
	rAddr                *net.TCPAddr
	sHost                Host
	tlsEnabled           bool
	sniHost              string
	lPayload             []byte
	rPayload             []byte
	buffSize             uint64
	lInitialized         bool
	rInitialized         bool
	bytesReceived        uint64
	bytesSent            uint64
	erred                bool
	errSig               chan bool
	connId               uint64
	serverProxyMode      bool
	wsUpgradeInitialized bool
}

func (p *Proxy) New(connId uint64, conn net.Conn, lAddr, rAddr *net.TCPAddr) *Proxy {
	return &Proxy{
		conn:                 conn,
		lConn:                conn,
		lAddr:                lAddr,
		rAddr:                rAddr,
		lPayload:             make([]byte, 0),
		rPayload:             make([]byte, 0),
		buffSize:             uint64(0xffff),
		lInitialized:         false,
		rInitialized:         false,
		erred:                false,
		errSig:               make(chan bool, 2),
		connId:               connId,
		serverProxyMode:      false,
		wsUpgradeInitialized: false,
	}
}

func (p *Proxy) SetlPayload(lPayload string) {
	if p.sHost.hostName != "" {
		lPayload = strings.Replace(lPayload, "[host]", p.sHost.hostName, -1)
		lPayload = strings.Replace(lPayload, "[host_port]", fmt.Sprintf("%s:%d", p.sHost.hostName, p.sHost.port), -1)
	}
	if p.sniHost != "" {
		lPayload = strings.Replace(lPayload, "[sni]", p.sniHost, -1)
	}
	lPayload = strings.Replace(lPayload, "[crlf]", "\r\n", -1)
	p.lPayload = []byte(lPayload)
}

func (p *Proxy) SetrPayload(rPayload string) {
	if rPayload == "" {
		rPayload = "HTTP/1.1 200 Connection Established[crlf][crlf]"
	}
	rPayload = strings.Replace(rPayload, "[crlf]", "\r\n", -1)
	p.rPayload = []byte(rPayload)
}

func (p *Proxy) SetServerProxyMode(enabled bool) {
	p.serverProxyMode = enabled
}

func (p *Proxy) SetServerHost(server string) {
	sHost, sPort, err := net.SplitHostPort(server)
	if err != nil {
		fmt.Printf("Cannot parse server host port '%s'", err)
		return
	}
	sPortParsed, err := strconv.ParseUint(sPort, 10, 64)
	if err != nil {
		fmt.Printf("Cannot parse server port '%s'", err)
		return
	}
	p.sHost = Host{
		hostName: sHost,
		port:     sPortParsed,
	}
}

func (p *Proxy) SetBufferSize(buffSize uint64) {
	p.buffSize = buffSize
}

func (p *Proxy) SetEnableTLS(enabled bool) {
	p.tlsEnabled = enabled
}

func (p *Proxy) SetSNIHost(hostname string) {
	p.sniHost = hostname
}

func (p *Proxy) Start() {
	defer p.closeConnection(p.lConn)

	var err error
	if p.tlsEnabled {
		p.rConn, err = tls.Dial("tcp", p.rAddr.String(), &tls.Config{
			ServerName:         p.sniHost,
			InsecureSkipVerify: true,
		})
	} else {
		p.rConn, err = net.DialTCP("tcp", nil, p.rAddr)
	}
	if err != nil {
		fmt.Printf("Cannot dial remote connection '%s'", err)
		return
	}
	defer p.closeConnection(p.rConn)

	fmt.Printf("CONN #%d opened %s >> %s\n", p.connId, p.lAddr, p.rAddr)

	go p.handleForwardData(p.lConn, p.rConn)
	if !p.serverProxyMode {
		go p.handleForwardData(p.rConn, p.lConn)
	}
	<-p.errSig
	fmt.Printf("CONN #%d closed (%d bytes sent, %d bytes received)\n", p.connId, p.bytesSent, p.bytesReceived)
}

func (p *Proxy) err() {
	if p.erred {
		return
	}
	p.errSig <- true
	p.erred = true
}

func (p *Proxy) handleForwardData(src, dst net.Conn) {
	isLocal := src == p.lConn
	buffer := make([]byte, p.buffSize)

	for {
		n, err := src.Read(buffer)
		if err != nil {
			//fmt.Printf("Cannot read buffer from source '%s'", err)
			p.err()
			return
		}
		connBuff := buffer[:n]
		if isLocal {
			p.handleOutboundData(src, dst, &connBuff)
		} else {
			p.handleInboundData(src, dst, &connBuff)
		}
		if p.serverProxyMode && p.wsUpgradeInitialized {
			n, err = src.Write(connBuff)
			p.wsUpgradeInitialized = false
			go p.handleForwardData(dst, src)
		} else {
			n, err = dst.Write(connBuff)
		}
		if err != nil {
			//fmt.Printf("Cannot write buffer to destination '%s'", err)
			p.err()
			return
		}

		if isLocal {
			p.bytesSent += uint64(n)
		} else {
			p.bytesReceived += uint64(n)
		}
	}
}

func (p *Proxy) handleOutboundData(src, dst net.Conn, connBuff *[]byte) {
	if p.lInitialized {
		return
	}

	fmt.Printf("CONN #%d %s >> %s >> %s\n", p.connId, src.RemoteAddr(), p.conn.LocalAddr(), dst.RemoteAddr())
	if p.serverProxyMode {
		if strings.Contains(strings.ToLower(string(*connBuff)), "upgrade: websocket") {
			fmt.Printf("CONN #%d connection upgrade to Websocket\n", p.connId)
			*connBuff = []byte("HTTP/1.1 101 Switching Protocols\r\n\r\n")
			p.wsUpgradeInitialized = true
		}
	} else {
		if bytes.Contains(*connBuff, []byte("CONNECT ")) {
			*connBuff = p.lPayload
			fmt.Println(string(*connBuff))
		}
	}

	p.lInitialized = true
}

func (p *Proxy) handleInboundData(src, dst net.Conn, connBuff *[]byte) {
	if p.rInitialized {
		return
	}

	fmt.Printf("CONN #%d %s << %s << %s\n", p.connId, dst.RemoteAddr(), p.conn.LocalAddr(), src.RemoteAddr())
	if bytes.Contains(*connBuff, []byte("HTTP/1.")) && !p.serverProxyMode {
		*connBuff = p.rPayload
		fmt.Println(string(*connBuff))
	}

	p.rInitialized = true
}

func (p *Proxy) closeConnection(conn net.Conn) {
	err := conn.Close()
	if err != nil {
		fmt.Printf("Cannot close connection '%s'", err)
		return
	}
}
