package main

import (
	"bufio"
	"bytes"
	"log"
	"net"
	"os"
	"regexp"
)

func parseCmd(cmd []byte, dst *net.UDPAddr) {
	log.Println(string(cmd))
	log.Printf("Dest: %s", dst)
}

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("Usage: %s <server host>:<server port>\n", os.Args[0])
	}

	conn, err := net.ListenPacket("udp", ":4222")
	if err != nil {
		log.Fatalf("Could not listen for UDP packets: %v\n", err)
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp", os.Args[1])
	if err != nil {
		log.Fatalf("Could not Resolve Address: %v\n%v\n", os.Args[1], err)
	}

	EXIT := []byte("exit")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		cmd := scanner.Bytes()
		if bytes.Equal(cmd, EXIT) {
			break
		}
		m, err := regexp.Match("[^\\w\\-]", cmd)
		if err != nil {
			log.Printf("Error matching cmd: %s, err: %v", cmd, err)
		}
		if m {
			log.Printf("Invalid cmd: %s\ncmd must only contain upper-case letters, lower-case letters, digits, dashes and underscores.\n", cmd)
		} else {
			parseCmd(cmd, dst)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Println(err)
	}
}
