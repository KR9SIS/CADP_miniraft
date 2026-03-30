package main

import (
	"net"
	"os"
	"time"
)

// Max size for UDP messages, kept under 1400 to avoid fragmentation
const maxBufferSize = 1300

const (
	heartbeatTimeout   = 75                    // 75ms between heartbeats
	minElectionTimeout = heartbeatTimeout * 3   // 225ms, allows 3 heartbeats before timing out
	maxElectionTimeout = minElectionTimeout * 2 // 450ms
)

type RaftServer struct {
	id      string
	addr    *net.UDPAddr
	conn    *net.UDPConn
	logFile *os.File
	state   ServerState

	eTimeout        *time.Timer
	heartbeatTicker *time.Ticker
	votes           int
	leaderAddr      *net.UDPAddr
	servers         []*net.UDPAddr // other servers in the cluster

	// Persistent state (Figure 2 of Raft paper)
	currentTerm int        // latest term this server has seen
	votedFor    string     // candidateId that received vote in current term (or empty if none)
	log         []LogEntry // log entries; each has a command and the term it was received

	// Volatile state on all servers
	commitIndex int // index of highest log entry known to be committed
	lastApplied int // index of highest log entry applied to state machine

	// Volatile state on leaders (reset after each election)
	nextIndex  []int // for each server, index of the next log entry to send
	matchIndex []int // for each server, index of highest log entry known to be replicated
}

type ServerState int

const (
	Failed ServerState = iota
	Follower
	Candidate
	Leader
)

var serverStateStr = [...]string{"Failed", "Follower", "Candidate", "Leader"}

// ServMsg wraps a received UDP message together with the sender's address
type ServMsg struct {
	addr *net.UDPAddr
	bMsg []byte
}
