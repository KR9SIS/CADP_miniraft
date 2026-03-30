package main

import (
	"bufio"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// changeState transitions the server to a new state and handles any setup needed.
func (serv *RaftServer) changeState(state ServerState) {
	// Always stop heartbeats first, only restart them if we become leader
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
		serv.leaderAddr = nil
		serv.votedFor = ""
		for i := range serv.servers {
			serv.nextIndex[i] = len(serv.log)
			serv.matchIndex[i] = 0
		}
		serv.heartbeatTicker.Reset(time.Millisecond * heartbeatTimeout)
		// Send a heartbeat right away so followers know we're the new leader
		serv.sendHeartBeats()
	}
	log.Printf("Changed %s state to %s\n", serv.id, serverStateStr[serv.state])
}

// startElection increments our term, votes for ourselves, and asks all other servers for their vote.
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

// sendHeartBeats sends AppendEntries to all followers. If there are pending entries they
// get included, otherwise it's just an empty heartbeat.
func (serv *RaftServer) sendHeartBeats() {
	for i, s := range serv.servers {
		nextIdx := serv.nextIndex[i]
		serv.sendAERequest(nextIdx, s, serv.log[nextIdx:])
	}
}

// getServerIdx finds a server's index in our servers list by its "host:port" string, returns -1 if not found.
func (serv *RaftServer) getServerIdx(serverID string) int {
	for i, s := range serv.servers {
		if s.String() == serverID {
			return i
		}
	}
	return -1
}

// sendMsg marshals a message to JSON and sends it over UDP.
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

// sendAERequest builds and sends an AppendEntries request to a single follower.
func (serv *RaftServer) sendAERequest(nextIndex int, addr *net.UDPAddr, entries []LogEntry) {
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

// advanceCommitIndex checks if any new entries can be committed.
// An entry is committed when a majority of servers have it.
func (serv *RaftServer) advanceCommitIndex() {
	total := len(serv.servers) + 1 // all servers including the leader
	majority := total/2 + 1

	// Find the highest N from the current term that a majority has replicated (Section 5.4.2).
	// Only entries from the leader's current term can trigger a commit by counting replicas.
	// Once such an entry is committed, all prior entries are committed indirectly
	// (commitUpTo writes everything from commitIndex+1 through N, including old-term entries).
	// We search top-down so that one current-term entry with majority commits everything below it.
	for n := len(serv.log) - 1; n > serv.commitIndex; n-- {
		if serv.log[n].Term != serv.currentTerm {
			continue
		}
		count := 1
		for j := range serv.servers {
			if serv.matchIndex[j] >= n {
				count++
			}
		}
		if count >= majority {
			serv.commitUpTo(n)
			break
		}
	}
}

// logEntry writes a single entry to the log file in "term,index,command" format.
func (serv *RaftServer) logEntry(entry LogEntry) (err error) {
	term := strconv.Itoa(entry.Term)
	idx := strconv.Itoa(entry.Index)
	if _, err := serv.logFile.Write([]byte(term + "," + idx + "," + entry.CommandName + "\n")); err != nil {
		return err
	}
	return nil
}

// commitUpTo writes entries from commitIndex+1 up to n to the log file.
// Used by the leader (via advanceCommitIndex) and followers (via handleAERequest).
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

// getStdin reads lines from stdin and sends them to the handler through a channel.
func (serv *RaftServer) getStdin(c chan<- string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		c <- scanner.Text()
	}
}

// serve listens for incoming UDP messages and forwards them to the handler through a channel.
func (serv *RaftServer) serve(c chan<- ServMsg) (err error) {
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
			log.Printf("error receiving %d bytes from %s: %v\n", n, addr, err)
			continue
		}
		sMsg := ServMsg{
			addr: addr,
			bMsg: make([]byte, n),
		}
		copy(sMsg.bMsg, buffer[:n])
		c <- sMsg
	}
}

// resetTimeout sets the election timer to a random duration between min and max election timeout.
func (serv *RaftServer) resetTimeout() {
	timeout := rand.Intn(maxElectionTimeout-minElectionTimeout) + minElectionTimeout
	d := time.Duration(timeout) * time.Millisecond
	serv.eTimeout.Reset(d)
}

func main() {
	if len(os.Args) < 3 {
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

	// Start with a dummy entry at index 0 so log[prevLogIndex] never goes out of bounds
	serv.log = make([]LogEntry, 1, 16)
	serv.nextIndex = make([]int, len(servers))
	serv.matchIndex = make([]int, len(servers))
	serv.servers = servers

	logDir := "log/"
	err = os.MkdirAll(logDir, 0o755)
	// Log file named host-port.log, e.g. "127.0.0.1-4000.log"
	filename := logDir + serv.addr.IP.String() + "-" + strconv.Itoa(serv.addr.Port) + ".log"
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("error reading %s file: %v", filename, err)
	}
	defer f.Close()
	serv.logFile = f

	if len(os.Args) != 4 {
		log.SetOutput(io.Discard)
	} else if len(os.Args) == 4 && os.Args[3] != "debug" {
		log.SetOutput(os.Stdout)
	}
	log.Printf("%+v\n", serv)

	sMsgChan := make(chan ServMsg, 100)
	stdinChan := make(chan string)

	go serv.handler(sMsgChan, stdinChan)
	go serv.getStdin(stdinChan)
	serv.serve(sMsgChan)
}
