package main

import (
	"net"
	"os"
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
	addr    *net.UDPAddr
	conn    *net.UDPConn
	logFile *os.File
	state   ServerState

	eTimeout   *time.Timer
	votes      int
	leaderAddr *net.UDPAddr
	// list of other servers in the cluster, used to send messages to other servers.
	servers []*net.UDPAddr

	// INFO: Persistent
	currentTerm int
	// latest term server has seen
	votedFor string
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
	inflightIndex []int
	// for each server, the last log index we sent in the most recent AppendEntries request
}

type ServerState int

const (
	Suspended ServerState = iota
	Follower
	Candidate
	Leader
)

var serverStateStr = [...]string{"Suspended", "Follower", "Candidate", "Leader"}

type serv_msg struct {
	addr *net.UDPAddr
	bMsg []byte
}
