package main

import (
	"log"
	"net"
	"os"
	"strings"
)

// 4 Bytes for header, 1296 for data
const maxBufferSize = 1300

type RaftServer struct {
	id string
	// identity:port string

	// INFO: Persistent
	currentTerm int
	// latest term server has seen
	votedFor bool
	// candidateId that recieved vote in current term (or null if none)
	log []string
	// log entries; each entry contains command for state machine, and term when entry was recieved by leader (first index is 1)

	// INFO: Volatile
	commitIndex int
	// index of highest log entry known to be committed
	lastApplied int
	// index of highest log entry applied to state machine

	// INFO: Leader vars
	nextIndex []int
	// for each server, index of the next log entry to send to that server (initialized to leader last log index + 1)
	matchIndex []int
	// for each server, index of highest log entry known to be replicated on server
}

func (serv *RaftServer) serve() (err error) {
	addr, err := net.ResolveUDPAddr("udp", serv.id)
	if err != nil {
		log.Fatalf("error resolving %s: %v\n", serv.id, err)
	}
	serverConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("failed to listen on port %d: %v\n", addr.Port, err)
	}
	buffer := make([]byte, maxBufferSize)
	log.Printf("%s listening", serv.id)
	for {
		n, addr, err := serverConn.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("error recvieving %d bytes from %s: %v\n", n, addr, err)
			continue
		}
		log.Printf("recieved \"%s\" from %s", buffer[0:n], addr)
	}
}

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("Usage: %s <host>:<port> <server id file>\n", os.Args[0])
	}
	id := os.Args[1]
	file := os.Args[2]

	data, err := os.ReadFile(file)
	if err != nil {
		log.Fatalf("Error reading %s: %v", id, err)
	}

	dataStr := string(data)
	if !strings.Contains(dataStr, id) {
		log.Fatalf("\"%s\" must be in %s\nContents of %s:\n%s\n", id, file, file, data)
	}

	sCount := strings.Count(dataStr, "\n")

	serv := &RaftServer{
		id:         id,
		log:        make([]string, 16),
		nextIndex:  make([]int, sCount),
		matchIndex: make([]int, sCount),
	}

	serv.serve()
}
