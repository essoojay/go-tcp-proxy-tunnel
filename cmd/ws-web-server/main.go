package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	httpAddress    = flag.String("l", "0.0.0.0:80", "http listen address")
	httpsAddress   = flag.String("ln", "0.0.0.0:443", "https listen address")
	backendAddress = flag.String("b", "127.0.0.1:8082", "backend proxy address")
	domain         = flag.String("d", "*", "server host address")
)

type serverConfig struct {
	secure  bool
	address string
	cer     []tls.Certificate
}

func main() {
	flag.Parse()

	var tcpWg sync.WaitGroup

	webConfig := &serverConfig{
		secure:  false,
		address: *httpAddress,
	}

	serverTLSConf, _, err := tlsCertSetup()
	if err != nil {
		fmt.Printf("Setup tls certificate error '%s'", err)
		return
	}
	webTlsConfig := &serverConfig{
		secure:  true,
		address: *httpsAddress,
		cer:     serverTLSConf.Certificates,
	}

	fmt.Printf("Websocket web server running on %s, %s\n\n", *httpAddress, *httpsAddress)

	tcpWg.Add(1)
	go startServer(&tcpWg, webConfig)
	tcpWg.Add(1)
	go startServer(&tcpWg, webTlsConfig)

	tcpWg.Wait()
}

func startServer(wg *sync.WaitGroup, config *serverConfig) {
	defer wg.Done()

	mux := http.NewServeMux()

	mux.Handle("/", http.HandlerFunc(mainHandler))

	srv := &http.Server{
		Addr:    config.address,
		Handler: mux,
	}

	if config.secure {
		srv.TLSConfig = &tls.Config{Certificates: config.cer}
	}

	err := srv.ListenAndServe()
	if err != nil {
		fmt.Println(err)
		return
	}
}

func mainHandler(w http.ResponseWriter, r *http.Request) {
	if !isWebsocket(r) {
		http.Error(w, "Not valid websocket request", http.StatusInternalServerError)
		fmt.Printf("Not valid websocket request (%s >> %s)\n", r.RemoteAddr, *backendAddress)
		return
	}

	domainInvalid := true
	if *domain != "*" {
		for _, allowedDomain := range strings.Split(*domain, ",") {
			if r.Host == allowedDomain {
				domainInvalid = false
				break
			}
		}
	}
	if domainInvalid {
		http.Error(w, "Domain not allowed", http.StatusForbidden)
		fmt.Printf("Domain not allowed (%s >> %s)\n", r.RemoteAddr, *backendAddress)
		return
	}

	fmt.Printf("Serving websocket proxy (%s >> %s)\n", r.RemoteAddr, *backendAddress)
	p := websocketProxy(*backendAddress)
	p.ServeHTTP(w, r)

	return
}

func isWebsocket(r *http.Request) bool {
	connUpgrade := r.Header.Get("Upgrade")

	if strings.ToLower(connUpgrade) == "websocket" {
		return true
	}

	return false
}

func websocketProxy(target string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := net.Dial("tcp", target)
		if err != nil {
			http.Error(w, "Error contacting backend server", http.StatusInternalServerError)
			fmt.Printf("Error dialing websocket backend %s: %v", target, err)
			return
		}
		defer closeConnection(d)

		hj, err := createHijack(w)
		if err != nil {
			http.Error(w, "Hijack error", http.StatusInternalServerError)
			fmt.Println(err)
			return
		}

		err = r.Write(d)
		if err != nil {
			fmt.Printf("Error copying request to target: %v", err)
			return
		}

		errCh := make(chan error, 2)
		cp := func(dst io.Writer, src io.Reader) {
			_, err := io.Copy(dst, src)
			errCh <- err
		}
		go cp(d, hj)
		go cp(hj, d)
		<-errCh
	})
}

func createHijack(w http.ResponseWriter) (net.Conn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Not a hijacker", 500)
		return nil, errors.New("not a hijacker")
	}

	nc, _, err := hj.Hijack()
	if err != nil {
		log.Printf("Hijack error: %v", err)
		return nil, err
	}

	return nc, nil
}

func closeConnection(conn net.Conn) {
	err := conn.Close()
	if err != nil {
		fmt.Printf("Cannot close connection '%s'", err)
		return
	}
}

func tlsCertSetup() (serverTLSConf *tls.Config, clientTLSConf *tls.Config, err error) {
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization:  []string{"WS"},
			Country:       []string{"WS"},
			Province:      []string{"WS"},
			Locality:      []string{"WS"},
			StreetAddress: []string{"WS"},
			PostalCode:    []string{"00000"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}

	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return nil, nil, err
	}

	caPEM := new(bytes.Buffer)
	pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})

	caPrivateKeyPEM := new(bytes.Buffer)
	pem.Encode(caPrivateKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caPrivateKey),
	})

	cert := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization:  []string{"WS"},
			Country:       []string{"WS"},
			Province:      []string{"WS"},
			Locality:      []string{"WS"},
			StreetAddress: []string{"WS"},
			PostalCode:    []string{"00000"},
		},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	certPrivateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &certPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := new(bytes.Buffer)
	pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})

	certPrivateKeyPEM := new(bytes.Buffer)
	pem.Encode(certPrivateKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivateKey),
	})

	serverCert, err := tls.X509KeyPair(certPEM.Bytes(), certPrivateKeyPEM.Bytes())
	if err != nil {
		return nil, nil, err
	}

	serverTLSConf = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}

	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(caPEM.Bytes())
	clientTLSConf = &tls.Config{
		RootCAs: certPool,
	}

	return
}
