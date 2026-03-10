package miniraft

import (
	"fmt"
	"os"
)

type RaftServer struct {
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

func main() {
}
