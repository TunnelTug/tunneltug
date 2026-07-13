package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

type serverRuntime struct {
	control *quic.Listener
	ingress *publicIngress
}

func notifyShutdownContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func gracefulShutdown(name string, rt *serverRuntime) {
	log.Printf("Shutting down %s...", name)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if rt.control != nil {
		if err := rt.control.Close(); err != nil {
			log.Printf("%s control listener close error: %v", name, err)
		}
	}
	if rt.ingress != nil {
		if rt.ingress.http3 != nil {
			if err := rt.ingress.http3.Shutdown(ctx); err != nil {
				log.Printf("%s HTTP/3 shutdown error: %v", name, err)
			}
		}
		if err := rt.ingress.http.Shutdown(ctx); err != nil {
			log.Printf("%s HTTP shutdown error: %v", name, err)
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
