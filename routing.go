package main

import "strings"

func isDirectRouting() bool {
	return strings.EqualFold(*routing, "direct")
}

func publicScheme() string {
	if *prod || *dev {
		return "https"
	}
	return "http"
}