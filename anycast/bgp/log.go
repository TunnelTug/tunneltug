package bgp

import (
	"encoding/hex"
	"log"
)

// LogBackend logs announce/withdraw actions (safe default / dry-run).
type LogBackend struct{}

func NewLogBackend() *LogBackend { return &LogBackend{} }

func (b *LogBackend) Name() string { return "log" }

func (b *LogBackend) Announce(routes []Route) error {
	for _, r := range routes {
		if r.BGPsec != nil {
			log.Printf("[bgp/log] ANNOUNCE %s next-hop %s asn=%d communities=%v rov=%s bgpsec=signed ski=%s sig=%s…",
				r.Prefix, r.NextHop, r.LocalASN, r.Communities, r.ROVState,
				hex.EncodeToString(r.BGPsec.SKI),
				hex.EncodeToString(r.BGPsec.Signature[:8]))
		} else {
			log.Printf("[bgp/log] ANNOUNCE %s next-hop %s asn=%d communities=%v rov=%s bgpsec=unsigned",
				r.Prefix, r.NextHop, r.LocalASN, r.Communities, r.ROVState)
		}
	}
	return nil
}

func (b *LogBackend) Withdraw(routes []Route) error {
	for _, r := range routes {
		log.Printf("[bgp/log] WITHDRAW %s next-hop %s", r.Prefix, r.NextHop)
	}
	return nil
}

func (b *LogBackend) Close() error { return nil }
