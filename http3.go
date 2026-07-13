package main

import (
	"crypto/tls"
	"net/http"
	"strconv"

	"github.com/quic-go/quic-go/http3"
)

type publicIngress struct {
	http  *http.Server
	http3 *http3.Server
}

func productionHTTP3Server(addr string, handler http.Handler, tlsConfig *tls.Config, port int) *http3.Server {
	return &http3.Server{
		Addr:           addr,
		Port:           port,
		Handler:        handler,
		TLSConfig:      http3.ConfigureTLSConfig(tlsConfig),
		MaxHeaderBytes: 1 << 20,
	}
}

func withQUICAltSvc(h3 *http3.Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor < 3 {
			_ = h3.SetQUICHeaders(w.Header())
		}
		next.ServeHTTP(w, r)
	})
}

func publicPortNumber() int {
	port, err := strconv.Atoi(*publicPort)
	if err != nil || port <= 0 {
		return 443
	}
	return port
}
