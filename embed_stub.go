//go:build payload

package main

// payload builds cannot ship themselves further.
func payloadBytes(arch string) []byte { return nil }
