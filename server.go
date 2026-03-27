package main

import (
	"bufio"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// INFO:
// Safely changes the servers state
// Spawns extra go routines to unlock stateLock
// changeState changes the server's state. Caller must hold serv.mu.Lock().
func (serv *RaftServer) changeState(state ServerState) {
	// Stop the heartbeat ticker whenever leaving Leader state
	serv.heartbeatTicker.Stop()

	switch state {
	case Failed:
		serv.state = Failed
	case Follower:
		serv.state = Follower
		serv.votedFor = ""
		serv.resetTimeout()
	case Candidate:
		serv.state = Candidate
		serv.leaderAddr = nil
		serv.startElection()
	case Leader:
		serv.state = Leader
		serv.votedFor = ""
		for i := range serv.servers {
			serv.nextIndex[i] = len(serv.log)
			serv.matchIndex[i] = 0
		}
		serv.heartbeatTicker.Reset(time.Millisecond * heartbeatTimeout)
		// Send the first heartbeat right away so followers know we're the leader
		// instead of waiting 75ms for the ticker to fire
		serv.sendHeartBeats()
	}
	log.Printf("Changed %s state to %s\n", serv.id, serverStateStr[serv.state])
}

func (serv *RaftServer) startElection() {
	log.Printf("%s starting election\n", serv.id)
	serv.currentTerm++
	serv.votes = 1
	serv.resetTimeout()
	serv.votedFor = serv.id
	for _, s := range serv.servers {
		lLE := serv.log[len(serv.log)-1]
		rVReq := &RequestVoteRequest{
			Term:          serv.currentTerm,
			LastLogIndex:  lLE.Index,
			LastLogTerm:   lLE.Term,
			CandidateName: serv.id,
		}

		serv.sendMsg(rVReq, s)
	}
}

// sendHeartBeats sends a single round of AppendEntries (heartbeats or pending entries) to all followers.
// Called from the handler event loop on each heartbeat tick, not in a separate goroutine.
func (serv *RaftServer) sendHeartBeats() {
	for i, s := range serv.servers {
		nextIdx := serv.nextIndex[i]
		serv.sendAERequest(nextIdx, s, serv.log[nextIdx:])
	}
}

// Returns the index of the server with the given "host:port" ID in the servers slice, or -1 if not found.
func (serv *RaftServer) getServerIdx(serverID string) int {
	for i, s := range serv.servers {
		if s.String() == serverID {
			return i
		}
	}
	return -1
}

func (serv *RaftServer) sendMsg(message any, addr *net.UDPAddr) {
	rMsg := RaftMessage{
		Message: message,
	}
	bMsg, err := rMsg.MarshalRaftJson()
	if err != nil {
		log.Printf("error marshalling response to %s\nresponse: %v\nerror: %v", addr.String(), message, err)
		return
	}
	if _, err := serv.conn.WriteToUDP(bMsg, addr); err != nil {
		log.Printf("error sending to %s: %v\n", addr.String(), err)
	}
}

func (serv *RaftServer) sendAERequest(nextIndex int, addr *net.UDPAddr, entries []LogEntry) {
	// Record the last index we are sending so handleAEResponse knows what the follower confirmed.
	// Only update for actual entries, not heartbeats (empty entries), since for heartbeats
	// the math would give nextIndex-1 which could regress the value.
	i := serv.getServerIdx(addr.String())
	if i != -1 && len(entries) > 0 {
		serv.inflightIndex[i] = nextIndex + len(entries) - 1
	}
	aer := &AppendEntriesRequest{
		Term:         serv.currentTerm,
		PrevLogIndex: nextIndex - 1,
		PrevLogTerm:  serv.log[nextIndex-1].Term,
		LeaderId:     serv.id,
		LeaderCommit: serv.commitIndex,
		LogEntries:   entries,
	}
	if len(entries) != 0 {
		log.Printf("%s sending AER to %s\n", serv.id, addr.String())
	}
	serv.sendMsg(aer, addr)
}

// advanceCommitIndex checks if any new log entries can be committed.
// advanceCommitIndex checks if any new log entries can be committed.
// An entry is committed when a majority of servers have it in their log.
func (serv *RaftServer) advanceCommitIndex() {
	total := len(serv.servers) + 1 // all servers including the leader
	majority := total/2 + 1

	// Try to commit each entry starting from the one after the current commitIndex
	for n := serv.commitIndex + 1; n < len(serv.log); n++ {
		// The leader always has its own entries so we start the count at 1
		count := 1
		for j := range serv.servers {
			if serv.matchIndex[j] >= n {
				count++
			}
		}

		// We can only commit entries from our own term (Raft safety rule).
		// Old entries from previous terms get committed as a side effect when
		// we commit a newer entry (the inner loop below writes everything up to n).
		entryIsFromCurrentTerm := serv.log[n].Term == serv.currentTerm
		if count >= majority && entryIsFromCurrentTerm {
			serv.commitUpTo(n)
		} else {
			// Can't commit n, so no point checking higher indexes either
			break
		}
	}
}

func (serv *RaftServer) logEntry(entry LogEntry) (err error) {
	term := strconv.Itoa(entry.Term)
	idx := strconv.Itoa(entry.Index)
	if _, err := serv.logFile.Write([]byte(term + "," + idx + "," + entry.CommandName + "\n")); err != nil {
		return err
	}
	return nil
}

// commitUpTo writes all entries from commitIndex+1 up to n to the log file and advances commitIndex.
// Used by both the leader (advanceCommitIndex) and followers (handleAERequest).
func (serv *RaftServer) commitUpTo(n int) {
	for idx := serv.commitIndex + 1; idx <= n; idx++ {
		err := serv.logEntry(serv.log[idx])
		if err != nil {
			log.Printf("commitUpTo: error writing entry %d to log file: %v\n", idx, err)
		}
	}
	serv.commitIndex = n
	serv.lastApplied = n
	log.Printf("commitUpTo: committed up to index %d\n", n)
}

func (serv *RaftServer) getStdin(c chan<- string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		c <- scanner.Text()
	}
}

func (serv *RaftServer) serve(c chan<- serv_msg) (err error) {
	serverConn, err := net.ListenUDP("udp", serv.addr)
	if err != nil {
		log.Fatalf("failed to listen on port %d: %v\n", serv.addr.Port, err)
	}
	defer serverConn.Close()
	serv.conn = serverConn
	buffer := make([]byte, maxBufferSize)
	log.Printf("%s listening", serv.id)
	for {
		n, addr, err := serverConn.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("error recvieving %d bytes from %s: %v\n", n, addr, err)
			continue
		}
		sMsg := serv_msg{
			addr: addr,
			bMsg: make([]byte, n),
		}
		copy(sMsg.bMsg, buffer[:n])
		c <- sMsg
	}
}

func (serv *RaftServer) resetTimeout() {
	timeout := rand.Intn(maxElectionTimeout-minElectionTimeout) + minElectionTimeout
	d := time.Duration(timeout) * time.Millisecond
	serv.eTimeout.Reset(d)
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

	servers := make([]*net.UDPAddr, 0, 3)
	hbTicker := time.NewTicker(time.Millisecond * heartbeatTimeout)
	hbTicker.Stop() // stopped initially; started when becoming Leader
	serv := &RaftServer{
		id:              id,
		state:           Follower,
		eTimeout:        time.NewTimer(time.Second),
		heartbeatTicker: hbTicker,
	}
	defer serv.eTimeout.Stop()
	defer serv.heartbeatTicker.Stop()
	serv.resetTimeout()
	valid_id := false

	dataStr := strings.TrimRight(string(data), "\n")
	for s := range strings.SplitSeq(dataStr, "\n") {
		addr, err := net.ResolveUDPAddr("udp", s)
		if err != nil {
			log.Fatalf("error resolving %s: %v\n", s, err)
		}
		if s == id {
			valid_id = true
			serv.addr = addr
		} else {
			servers = append(servers, addr)
		}
	}
	if !valid_id {
		log.Fatalf("\"%s\" must be in %s\nContents of %s:\n%s\n", id, file, file, data)
	}

	// The log starts with a dummy entry at index 0 so we can always safely access log[nextIndex-1]
	serv.log = make([]LogEntry, 1, 16)
	serv.nextIndex = make([]int, len(servers))
	serv.matchIndex = make([]int, len(servers))
	serv.inflightIndex = make([]int, len(servers))
	serv.servers = servers

	logDir := "log/"
	err = os.MkdirAll(logDir, 0o755)
	// filename = host-port.log
	filename := logDir + serv.addr.IP.String() + "-" + strconv.Itoa(serv.addr.Port) + ".log"
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("error reading %s file: %v", filename, err)
	}
	defer f.Close()
	serv.logFile = f

	log.Printf("%+v\n", serv)

	sMsgChan := make(chan serv_msg, 100)
	stdinChan := make(chan string)

	go serv.handler(sMsgChan, stdinChan)
	go serv.getStdin(stdinChan)
	serv.serve(sMsgChan)
}
