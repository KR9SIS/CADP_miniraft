package main

import (
	"bufio"
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

	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		log.Fatalf("Could not listen for UDP packets: %v\n", err)
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp", os.Args[1])
	if err != nil {
		log.Fatalf("Could not Resolve Address: %v\n%v\n", os.Args[1], err)
	}
	log.Printf("Destination: %s", dst.String())

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

		cCmd := &ClientCommand{
			Command: cmd,
		}
		rMsg := RaftMessage{
			Message: cCmd,
		}
		bMsg, err := rMsg.MarshalRaftJson()
		if err != nil {
			log.Printf("error marshalling response to %s\nresponse: %v\nerror: %v", dst.String(), cCmd, err)
			return
		}
		if _, err := conn.WriteTo(bMsg, dst); err != nil {
			log.Printf("error sending to %s: %v\n", dst.String(), err)
		}

		fmt.Printf("Command: ")
	}

	if err := scanner.Err(); err != nil {
		log.Println(err)
	}
}
