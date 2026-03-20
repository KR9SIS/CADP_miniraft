package main

import (
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	miniraft "github.com/KR9SIS/CADP_miniraft/msg_format"
)

// 4 Bytes for header, 1296 for data
const maxBufferSize = 1300

const (
	// 75 ms for hearbeat
	heartbeatTimeout = 75
	// Allow 3 heartbeats before min election timeout
	minElectionTimeout = heartbeatTimeout * 3
	maxElectionTimeout = minElectionTimeout * 2
)

type RaftServer struct {
	id string
	// identity:port string
	addr      *net.UDPAddr
	conn      *net.UDPConn
	logFile   *os.File
	state     ServerState
	stateLock sync.Mutex

	eTimeout *time.Ticker
	votes    atomic.Int64
	//list of other servers in the cluster, used to send messages to other servers.
	servers  []*net.UDPAddr

	// INFO: Persistent
	currentTerm atomic.Int64
	// latest term server has seen
	votedFor string
	// candidateId that recieved vote in current term (or null if none)
	log []miniraft.LogEntry
	// log entries; each entry contains command for state machine, and term when entry was recieved by leader (first index is 1)

	// INFO: Volatile
	commitIndex atomic.Int64
	// index of highest log entry known to be committed
	lastApplied atomic.Int64
	// index of highest log entry applied to state machine

	// INFO: Leader vars
	nextIndex []atomic.Int64
	// for each server, index of the next log entry to send to that server (initialized to leader last log index + 1)
	matchIndex []atomic.Int64
	// for each server, index of highest log entry known to be replicated on server
	inflightIndex []atomic.Int64
	// for each server, the last log index we sent in the most recent AppendEntries request
}

type ServerState int

const (
	Failed ServerState = iota
	Follower
	Candidate
	Leader
)

var serverStateStr = [...]string{"Failed", "Follower", "Candidate", "Leader"}

// INFO:
// Safely changes the servers state
// Spawns extra go routines to unlock stateLock
func (serv *RaftServer) changeState(state ServerState) {
	serv.stateLock.Lock()
	defer serv.stateLock.Unlock()
	switch state {
	case Failed:
		serv.state = Failed
	case Follower:
		serv.state = Follower
		serv.resetTimeout()
	case Candidate:
		serv.state = Candidate
		go serv.startElection()
	case Leader:
		serv.state = Leader
		go serv.sendHeartBeats()
	}
	log.Printf("Changed %s state to %s\n", serv.id, serverStateStr[serv.state])
}

func (serv *RaftServer) startElection() {
	log.Printf("%s starting election\n", serv.id)
	serv.currentTerm.Add(1)
	serv.votes.Store(1)
	serv.resetTimeout()
	for _, s := range serv.servers {
		lLE := serv.log[int(serv.lastApplied.Load())]
		rVReq := &miniraft.RequestVoteRequest{
			Term:         int(serv.currentTerm.Load()),
			LastLogIndex: lLE.Index,
			LastLogTerm:  lLE.Term,
			CandidateName: serv.id,
		}

		serv.sendMsg(rVReq, s)
	}
}

func (serv *RaftServer) sendHeartBeats() {
	log.Printf("%s sending heartbeats\n", serv.id)
	ticker := time.NewTicker(time.Millisecond * 75)
	defer ticker.Stop()
	for serv.state == Leader {
		<-ticker.C
		for i, s := range serv.servers {
			serv.sendAERequest(int(serv.nextIndex[i].Load()), s, []miniraft.LogEntry{})
		}
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
	rMsg := miniraft.RaftMessage{
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

func (serv *RaftServer) sendAERequest(nextIndex int, addr *net.UDPAddr, entries []miniraft.LogEntry) {
	// Record the last index we are sending so handleAEResponse knows what the follower confirmed
	i := serv.getServerIdx(addr.String())
	if i != -1 {
		serv.inflightIndex[i].Store(int64(nextIndex + len(entries) - 1))
	}
	aer := &miniraft.AppendEntriesRequest{
		Term:         int(serv.currentTerm.Load()),
		PrevLogIndex: nextIndex - 1,
		PrevLogTerm:  serv.log[nextIndex-1].Term,
		LeaderId:     serv.id,
		LeaderCommit: int(serv.commitIndex.Load()),
		LogEntries:   entries,
	}
	log.Printf("%s sending AER to %s\n", serv.id, addr.String())
	serv.sendMsg(aer, addr)
}

// handleAEResponse is called when we receive a response to an AppendEntries request we sent.
// On success: update the follower's nextIndex and matchIndex, then check if we can commit new entries.
// On failure: back up nextIndex by one and retry with a longer suffix of the log.
func (serv *RaftServer) handleAEResponse(res miniraft.AppendEntriesResponse, addr *net.UDPAddr) {
	i := serv.getServerIdx(addr.String())
	if i == -1 {
		log.Printf("handleAEResponse: unknown server %s\n", addr.String())
		return
	}

	// If the response has a higher term, we are a stale leader and must step down
	if res.Term > int(serv.currentTerm.Load()) {
		log.Printf("handleAEResponse: response from %s has higher term %d, stepping down\n", addr.String(), res.Term)
		serv.currentTerm.Store(int64(res.Term))
		serv.changeState(Follower)
		return
	}

	if res.Success {
		log.Printf("AEResponse from %s: success\n", addr.String())

		// Update nextIndex and matchIndex based on what we actually sent (inflightIndex)
		// We can't just use len(log) here because new entries might have been added since we sent the request
		lastSent := int(serv.inflightIndex[i].Load())
		serv.nextIndex[i].Store(int64(lastSent + 1))
		serv.matchIndex[i].Store(int64(lastSent))

		// Check if we can now commit more entries
		serv.advanceCommitIndex()
		return
	}

	// The follower rejected our entries, meaning its log doesn't match ours at nextIndex-1.
	// Back up nextIndex by one and retry with a longer suffix so we find where the logs are the same.
	log.Printf("AEResponse from %s: failed, backing up and retrying\n", addr.String())
	if serv.nextIndex[i].Load() > 1 {
		serv.nextIndex[i].Add(-1)
	}
	nextIndex := int(serv.nextIndex[i].Load())
	serv.sendAERequest(nextIndex, addr, serv.log[nextIndex:])
}

// advanceCommitIndex checks if any new log entries can be committed.
// advanceCommitIndex checks if any new log entries can be committed.
// An entry is committed when a majority of servers have it in their log.
func (serv *RaftServer) advanceCommitIndex() {
	total := len(serv.servers) + 1 // all servers including the leader
	majority := total/2 + 1

	// Try to commit each entry starting from the one after the current commitIndex
	for n := int(serv.commitIndex.Load()) + 1; n < len(serv.log); n++ {
		// The leader always has its own entries so we start the count at 1
		count := 1
		for j := range serv.servers {
			if int(serv.matchIndex[j].Load()) >= n {
				count++
			}
		}

		// We can only commit entries from our own term (Raft safety rule).
		// Old entries from previous terms get committed as a side effect when
		// we commit a newer entry (the inner loop below writes everything up to n).
		entryIsFromCurrentTerm := serv.log[n].Term == int(serv.currentTerm.Load())
		if count >= majority && entryIsFromCurrentTerm {
			// Write everything from the old commitIndex up to n to the log file
			for idx := int(serv.commitIndex.Load()) + 1; idx <= n; idx++ {
				err := serv.logEntry(serv.log[idx])
				if err != nil {
					log.Printf("advanceCommitIndex: error writing entry %d to log file: %v\n", idx, err)
				}
			}
			serv.commitIndex.Store(int64(n))
			log.Printf("advanceCommitIndex: committed up to index %d\n", n)
		} else {
			// Can't commit n, so no point checking higher indexes either
			break
		}
	}
}

// INFO:
// 1. Reply false if term < currentTerm
// 2. Reply false if log doesn't contain an entry
// at PrevLogIndex whose term matches PrevLogTerm
// 3. If an existing entry conflicts with a new one
// (same index different terms), delete the entry and
// all that follow it.
// 4. Append any new entries not already in the log
// 5. If leaderCommit > commitIndex, set
// commitIndex = min(leaderCommit, index of last new entry)
func (serv *RaftServer) handleAERequest(req miniraft.AppendEntriesRequest, addr *net.UDPAddr) miniraft.AppendEntriesResponse {
	resp := miniraft.AppendEntriesResponse{
		Term: int(serv.currentTerm.Load()),
	}
	if serv.state != Follower && int(serv.currentTerm.Load()) <= req.Term {
		serv.changeState(Follower)
	}
	if len(req.LogEntries) == 0 {
		log.Printf("Heartbeat recieved from %s\n", addr.String())
		resp.Success = true // Heartbeat
		serv.resetTimeout()
		return resp
	}
	if req.Term < int(serv.currentTerm.Load()) {
		log.Printf("%s term less than currentTerm, AER failed\n", addr.String())
		resp.Success = false // 1.
		return resp
	}
	if req.PrevLogIndex <= len(serv.log)-1 {
		pLE := serv.log[req.PrevLogIndex]
		if pLE.Term != req.PrevLogTerm {
			log.Printf("%s prevLogIndex term != %s prevLogIndex term, AER failed\n", serv.id, addr.String())
			resp.Success = false // 2.
			return resp
		}
	}
	if req.LeaderCommit <= len(serv.log)-1 {
		serv.log = append(serv.log[req.LeaderCommit:], req.LogEntries...) // 3. & 4.
	} else {
		serv.log = append(serv.log, req.LogEntries...) // 4.
	}
	resp.Success = true
	log.Printf("AER from %s successful", addr.String())
	if int(serv.commitIndex.Load()) < req.LeaderCommit {
		serv.commitIndex.Store(int64(min(req.LeaderCommit, len(serv.log)-1))) // 5.
	}
	return resp
}

// INFO:
// 1. Reply false if term < currentTerm
// 2. If (votedFor is null or candidateId) and
// Candidate's log is at least as up-to-date as reciver's log, grant vote
func (serv *RaftServer) handleRVRequest(req miniraft.RequestVoteRequest, addr *net.UDPAddr) miniraft.RequestVoteResponse {
	resp := miniraft.RequestVoteResponse{
		Term: int(serv.currentTerm.Load()),
	}
	if serv.state != Follower && int(serv.currentTerm.Load()) < req.Term {
		serv.changeState(Follower)
	}
	if req.Term < int(serv.currentTerm.Load()) {
		log.Printf("Vote request from %s denied, term < currentTerm\n", addr.String())
		resp.VoteGranted = false
	} else if (serv.votedFor != "") && (serv.votedFor != addr.String()) {
		resp.VoteGranted = false
	} else if req.LastLogIndex < int(serv.lastApplied.Load()) {
		resp.VoteGranted = false
	} else {
		serv.resetTimeout()
		resp.VoteGranted = true
	}
	return resp
}

// TODO: change to follower if response term is higher than own.
func (serv *RaftServer) handleRVResponse(res miniraft.RequestVoteResponse) {
	if !res.VoteGranted {
		return
	}
	serv.votes.Add(1)
	if int(serv.votes.Load()) >= (len(serv.servers)/2)+1 {
		serv.changeState(Leader)
	}
}

func (serv *RaftServer) logEntry(entry miniraft.LogEntry) (err error) {
	term := strconv.Itoa(entry.Term)
	idx := strconv.Itoa(entry.Index)
	if _, err := serv.logFile.Write([]byte(term + "," + idx + "," + entry.CommandName)); err != nil {
		return err
	}
	return nil
}

func (serv *RaftServer) handleMsg(bMsg []byte, addr *net.UDPAddr) {
	log.Printf("Recv msg from: %v\nmsg: %v", addr, bMsg)

	msg := &miniraft.RaftMessage{}
	msgType, err := msg.UnmarshalRaftJSON(bMsg)
	if err != nil {
		log.Printf("error unmarshalling json msg.\nmsg: %v\nerror: %v\n", bMsg, err)
	}

	switch msgType {
	case miniraft.AppendEntriesRequestMessage:
		resp := serv.handleAERequest(msg.Message.(miniraft.AppendEntriesRequest), addr)
		if serv.state != Failed {
			serv.sendMsg(resp, addr)
		}

	case miniraft.AppendEntriesResponseMessage:
		serv.handleAEResponse(msg.Message.(miniraft.AppendEntriesResponse), addr)

	case miniraft.RequestVoteRequestMessage:
		resp := serv.handleRVRequest(msg.Message.(miniraft.RequestVoteRequest), addr)
		if serv.state != Failed {
			serv.sendMsg(resp, addr)
		}

	case miniraft.RequestVoteResponseMessage:
		serv.handleRVResponse(msg.Message.(miniraft.RequestVoteResponse))

	default:
		log.Printf("error unmarshalling json msg, no such message type.\nmsg: %v\ntype: %v\n", bMsg, msgType)
	}
}

func (serv *RaftServer) serve() (err error) {
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
		log.Printf("recieved \"%s\" from %s", buffer[0:n], addr) // WARN: Maybe remove
		go serv.handleMsg(buffer[0:n], addr)
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
	serv := &RaftServer{
		id:       id,
		state:    Follower,
		eTimeout: time.NewTicker(999999),
	}
	defer serv.eTimeout.Stop()
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
	serv.log = make([]miniraft.LogEntry, 1, 16)
	serv.nextIndex = make([]atomic.Int64, len(servers))
	serv.matchIndex = make([]atomic.Int64, len(servers))
	serv.inflightIndex = make([]atomic.Int64, len(servers))
	serv.servers = servers

	// filename = server-host-port.log
	filename := "server-" + serv.addr.IP.String() + "-" + strconv.Itoa(serv.addr.Port) + ".log"
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("error reading %s file: %v", filename, err)
	}
	defer f.Close()
	serv.logFile = f

	log.Printf("%+v\n", serv)

	serv.serve()
}
