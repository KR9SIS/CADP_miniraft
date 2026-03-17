package miniraft

import "encoding/json"

type LogEntry struct {
	Index       int
	Term        int
	CommandName string
}
// A message sent by the leader to replicate log entries and to provide a heartbeat.
type AppendEntriesRequest struct {
	Term         int
	PrevLogIndex int
	PrevLogTerm  int
	LeaderCommit int
	LeaderId     string
	//if empty then its a heartbeat message.
	LogEntries   []LogEntry
}
//The response to an AppendEntriesRequest sent by the leader.
type AppendEntriesResponse struct {
	//followers current term. If larger than the leaders term, then the leader is outdated and should step down.
	Term    int
	Success bool
}

//A message a candidate sends when starting an election. 
type RequestVoteRequest struct {
	Term          int
	LastLogIndex  int
	LastLogTerm   int
	CandidateName string
}

//The reply to a vote request
type RequestVoteResponse struct {
	Term        int
	VoteGranted bool
}

type MessageType int

const (
	AppendEntriesRequestMessage MessageType = iota
	AppendEntriesResponseMessage
	RequestVoteRequestMessage
	RequestVoteResponseMessage
)


type RaftMessage struct {
	Message any
}


//marshals the RaftMessage into JSON format. 
func (message *RaftMessage) MarshalRaftJson() (result []byte, err error) {
	result, err = json.Marshal(message.Message)
	return
}

//unmarshals the JSON into a RaftMessage.  
func (message *RaftMessage) UnmarshalRaftJSON(b []byte) (msg MessageType, err error) {
	aer := &AppendEntriesRequest{}
	err = json.Unmarshal(b, aer)
	//if the unmarshal was successful and the LeaderId is not empty, then we can assume this is an AppendEntriesRequest message.
	if err == nil && aer.LeaderId != "" {
		msg = AppendEntriesRequestMessage
		message.Message = aer
		return
	}

	if _, ok := err.(*json.UnmarshalTypeError); err != nil && !ok {
		return
	}

	aeres := &AppendEntriesResponse{}
	//if the unmarshal was successful and the term is greater than 0, then we can assume this is an AppendEntriesResponse message.
	if err = json.Unmarshal(b, aeres); err != nil {
		return
	}
	if aeres.Term > 0 {
		msg = AppendEntriesResponseMessage
		message.Message = aeres
		return
	}

	rvr := &RequestVoteRequest{}
	//if the unmarshal was successful and the CandidateName is not empty, then we can assume this is a RequestVoteRequest message.
	if err = json.Unmarshal(b, rvr); err != nil {
		return
	}
	if rvr.Term > 0 && rvr.CandidateName != "" {
		msg = RequestVoteRequestMessage
		message.Message = rvr
		return
	}

	rvres := &RequestVoteResponse{}
	//if the unmarshal was successful and the term is greater than 0, then we can assume this is a RequestVoteResponse message.
	if err = json.Unmarshal(b, rvres); err != nil {
		return
	}
	if rvres.Term > 0 {
		msg = RequestVoteResponseMessage
		message.Message = rvres
	}
	return
}
