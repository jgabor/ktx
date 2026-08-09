//go:build !linux

package main

func filesystemIdentityFor(string) filesystemIdentity {
	return filesystemIdentity{Type: "unavailable"}
}
