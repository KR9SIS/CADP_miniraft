package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
)

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

	msg := make(map[string]string)

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Printf("Command: ")
	for scanner.Scan() {
		cmd := scanner.Text()
		if cmd == "exit" {
			break
		}

		m, err := regexp.Match("[^\\w\\-]", []byte(cmd))
		if err != nil {
			log.Printf("Error matching cmd: %s, err: %v", cmd, err)
		}
		if m || err != nil {
			log.Printf("Invalid cmd: %s\ncmd must only contain upper-case letters, lower-case letters, digits, dashes and underscores.\n", cmd)
			fmt.Printf("Command: ")
			continue
		}

		msg["Command"] = cmd
		bMsg, err := json.Marshal(msg)
		if err != nil {
			log.Printf("Failed to marshal cmd: %s\nerr: %v", cmd, err)
			fmt.Printf("Command: ")
			continue
		}

		_, err = conn.WriteTo(bMsg, dst)
		if err != nil {
			log.Printf("Failed to send '%s' to %v", bMsg, dst)
		}

		fmt.Printf("Command: ")
	}

	if err := scanner.Err(); err != nil {
		log.Println(err)
	}
}
