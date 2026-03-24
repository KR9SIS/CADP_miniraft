package miniraft

import (
	"bytes"
	"encoding/json"
	"errors"
)

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
	// if empty then its a heartbeat message.
	LogEntries []LogEntry
}

// The response to an AppendEntriesRequest sent by the leader.
type AppendEntriesResponse struct {
	// followers current term. If larger than the leaders term, then the leader is outdated and should step down.
	Term    int
	Success bool
}

// A message a candidate sends when starting an election.
type RequestVoteRequest struct {
	Term          int
	LastLogIndex  int
	LastLogTerm   int
	CandidateName string
}

// The reply to a vote request
type RequestVoteResponse struct {
	Term        int
	VoteGranted bool
}

type ClientCommand struct {
	Command string
}

type MessageType int

const (
	AppendEntriesRequestMessage MessageType = iota
	AppendEntriesResponseMessage
	RequestVoteRequestMessage
	RequestVoteResponseMessage
	ClientCommandMessage
)

type RaftMessage struct {
	Message any
}

// marshals the RaftMessage into JSON format.
func (message *RaftMessage) MarshalRaftJson() (result []byte, err error) {
	result, err = json.Marshal(message.Message)
	return
}

// unmarshals the JSON into a RaftMessage.
func (message *RaftMessage) UnmarshalRaftJSON(b []byte) (msg MessageType, err error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	aereq := &AppendEntriesRequest{}
	err = d.Decode(aereq)
	if err == nil {
		msg = AppendEntriesRequestMessage
		message.Message = *aereq
		return
	}

	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	aeres := &AppendEntriesResponse{}
	err = d.Decode(aeres)
	if err == nil {
		msg = AppendEntriesResponseMessage
		message.Message = *aeres
		return
	}

	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	rvreq := &RequestVoteRequest{}
	err = d.Decode(rvreq)
	if err == nil {
		msg = RequestVoteRequestMessage
		message.Message = *rvreq
		return
	}

	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	rvres := &RequestVoteResponse{}
	err = d.Decode(rvres)
	if err == nil {
		msg = RequestVoteResponseMessage
		message.Message = *rvres
		return
	}

	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	cc := &ClientCommand{}
	err = d.Decode(cc)
	if err == nil {
		msg = ClientCommandMessage
		message.Message = *cc
		return
	}

	msg = -1
	err = errors.New("Unknown Message Type")

	return
}
