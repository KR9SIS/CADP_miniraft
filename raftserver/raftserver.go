package main

import (
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/KR9SIS/CADP_miniraft/msg_format"
)

// 4 Bytes for header, 1296 for data
const maxBufferSize = 1300

type RaftServer struct {
	id string
	// identity:port string
	addr *net.UDPAddr
	logFile *os.File

	// INFO: Persistent
	currentTerm int
	// latest term server has seen
	votedFor bool
	// candidateId that recieved vote in current term (or null if none)
	log []miniraft.LogEntry
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

func (serv *RaftServer) logEntry(entry miniraft.LogEntry) (err error) {
	term := strconv.Itoa(entry.Term)
	idx := strconv.Itoa(entry.Index)
	if _, err := serv.logFile.Write([]byte(term + "," + idx + "," + entry.CommandName)); err != nil {
		return err
	}
	return nil
}

func (serv *RaftServer) serve() (err error) {
	serverConn, err := net.ListenUDP("udp", serv.addr)
	if err != nil {
		log.Fatalf("failed to listen on port %d: %v\n", serv.addr.Port, err)
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
	addr, err := net.ResolveUDPAddr("udp", id)
	if err != nil {
		log.Fatalf("error resolving %s: %v\n", id, err)
	}

	// filename = server-host-port.log
	filename := "server-" + addr.IP.String() + "-" + strconv.Itoa(addr.Port) + ".log"
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("error reading %s file: %v", filename, err)
	}
	defer f.Close()
	serv := &RaftServer{
		id:         id,
		addr:       addr,
		logFile:    f,
		log:        make([]miniraft.LogEntry, 16),
		nextIndex:  make([]int, sCount),
		matchIndex: make([]int, sCount),
	}

	serv.serve()
}
