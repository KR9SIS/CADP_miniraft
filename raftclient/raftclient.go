package main

import (
	"log"
	"net"
)

func main() {
	addr_str := "[::1]:3007"
	addr, err := net.ResolveUDPAddr("udp", addr_str)
	if err != nil {
		log.Fatalf("Could not resolve %s to UDP address\n", addr_str)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatalf("Could not dial %v to UDP address\n", addr)
	}
	defer conn.Close()
	conn.Write([]byte("This is a test\n"))
}
