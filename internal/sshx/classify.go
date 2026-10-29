package sshx

import "strings"

// unreachableMarkers are the ssh stderr substrings that mean the network path to the
// host is down. The "connect to host ... port ...: " prefix ssh puts before them is
// covered by matching the trailing text.
var unreachableMarkers = []string{
	"Could not resolve hostname",
	"Connection timed out",
	"Operation timed out",
	"No route to host",
	"Connection refused",
	"Network is unreachable",
}

// fatalMarkers are failures that must never fall back to a local run, even when the
// same stderr also contains an unreachable marker.
var fatalMarkers = []string{
	"Permission denied",
	"Host key verification failed",
	"REMOTE HOST IDENTIFICATION HAS CHANGED",
}

// ClassifyConnectError inspects the stderr of an ssh that ended before the remote
// hello. It reports fallback=true only for network-unreachable failures, with the
// matched marker as reason. Everything else is fatal.
func ClassifyConnectError(stderr string) (reason string, fallback bool) {
	for _, m := range fatalMarkers {
		if strings.Contains(stderr, m) {
			return "", false
		}
	}
	for _, m := range unreachableMarkers {
		if strings.Contains(stderr, m) {
			return m, true
		}
	}
	return "", false
}
