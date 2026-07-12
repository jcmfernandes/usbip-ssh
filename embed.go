//go:build !payload

package main

import _ "embed"

//go:embed embed/payload_amd64
var payloadAmd64 []byte

//go:embed embed/payload_arm64
var payloadArm64 []byte

// payloadBytes returns the embedded payload matching a remote `uname -m`.
func payloadBytes(arch string) []byte {
	switch arch {
	case "x86_64", "amd64":
		return payloadAmd64
	case "aarch64", "arm64":
		return payloadArm64
	}
	return nil
}
