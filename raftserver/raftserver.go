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

	"github.com/KR9SIS/CADP_miniraft/msg_format"
)

// 4 Bytes for header, 1296 for data
const maxBufferSize = 1300

type RaftServer struct {
	id string
	// identity:port string
	addr      *net.UDPAddr
	logFile   *os.File
	state     ServerState
	stateLock sync.Mutex

	eTimeout *time.Ticker
	votes    atomic.Int64
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

func (serv *RaftServer) getServerIdx(port int) int {
	s := strconv.Itoa(port)
	i := s[len(s)-1]
	return int(i)
}

func (serv *RaftServer) sendMsg(message any, addr *net.UDPAddr) {
	rMsg := miniraft.RaftMessage{
		Message: message,
	}
	bMsg, err := rMsg.MarshalRaftJson()
	if err != nil {
		log.Printf("error marshalling response to %s\nresponse: %v\nerror: %v", addr.String(), message, err)
	}
	conn, err := net.DialUDP("udp", serv.addr, addr)
	if err != nil {
		log.Printf("Could not dial %v to UDP address\n", addr)
	}
	defer conn.Close()
	conn.Write(bMsg)
}

func (serv *RaftServer) sendAERequest(nextIndex int, addr *net.UDPAddr, entries []miniraft.LogEntry) {
	aer := &miniraft.AppendEntriesRequest{
		Term:         int(serv.currentTerm.Load()),
		PrevLogIndex: nextIndex - 1,
		PrevLogTerm:  serv.log[nextIndex-1].Term,
		LeaderId:     serv.id,
		LeaderCommit: nextIndex,
		LogEntries:   entries,
	}
	log.Printf("%s sending AER to %s\n", serv.id, addr.String())
	serv.sendMsg(aer, addr)
}

// INFO:
// 1. If request was successful, update followers nextIndex
// 2. If not, decrease followers nextIndex and try again
func (serv *RaftServer) handleAEResponse(res miniraft.AppendEntriesResponse, addr *net.UDPAddr) {
	// WARN: Maybe not completely done
	i := serv.getServerIdx(addr.Port)
	if res.Success {
		log.Printf("AER to %s successful\n", addr.String())
		serv.nextIndex[i].Store(serv.commitIndex.Load()) // 1.
		return
	}
	if serv.nextIndex[i].Load() != 0 {
		serv.nextIndex[i].Add(-1)
	}
	log.Printf("AER to %s failed, retrying\n", addr.String())
	nextIndex := int(serv.nextIndex[i].Load())
	serv.sendAERequest(nextIndex, addr, serv.log[nextIndex:]) // 2.
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
	if serv.state == Candidate && int(serv.currentTerm.Load()) <= req.Term {
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
	// generate timeout in the range 150 to 300
	timeout := strconv.Itoa(rand.Intn(300-150)+150) + "ms"
	d, err := time.ParseDuration(timeout)
	if err != nil {
		serv.resetTimeout() // Try again
	}
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

	serv.log = make([]miniraft.LogEntry, 0, 16)
	serv.nextIndex = make([]atomic.Int64, len(servers))
	serv.matchIndex = make([]atomic.Int64, len(servers))
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
