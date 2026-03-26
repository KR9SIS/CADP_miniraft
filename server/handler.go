package main

import (
	"log"
	"net"

	miniraft "github.com/KR9SIS/CADP_miniraft/msg_format"
)

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

	// If the incoming term is higher than ours, update our term and step down to follower.
	// Per Raft: "If RPC request or response contains term T > currentTerm:
	// set currentTerm = T, convert to follower." Applies to ALL server roles.
	if req.Term > int(serv.currentTerm.Load()) {
		serv.currentTerm.Store(int64(req.Term))
		if serv.state != Follower {
			serv.changeState(Follower)
		} else {
			// Already a follower — just clear votedFor for the new term
			serv.votedFor = ""
		}
	}

	// Update the response term after potential update above
	resp.Term = int(serv.currentTerm.Load())

	// 1. Reject if the request is from a stale leader
	if req.Term < int(serv.currentTerm.Load()) {
		log.Printf("handleAERequest: rejected from %s, their term %d is less than ours %d\n", addr.String(), req.Term, int(serv.currentTerm.Load()))
		resp.Success = false
		return resp
	}

	serv.resetTimeout()
	serv.leaderAddr = addr

	// 2. Reject if our log doesn't have an entry at PrevLogIndex whose term matches PrevLogTerm.
	// This check runs for both heartbeats and real entries — a heartbeat is just an
	// AppendEntries with an empty LogEntries array.
	if req.PrevLogIndex > len(serv.log)-1 {
		log.Printf("handleAERequest: rejected from %s, missing entry at PrevLogIndex %d\n", addr.String(), req.PrevLogIndex)
		resp.Success = false
		return resp
	}
	if serv.log[req.PrevLogIndex].Term != req.PrevLogTerm {
		log.Printf("handleAERequest: rejected from %s, term mismatch at PrevLogIndex %d\n", addr.String(), req.PrevLogIndex)
		resp.Success = false
		return resp
	}

	// 3. & 4. Truncate any conflicting entries and append the new ones.
	// When LogEntries is empty, a heartbeat, we can skip appending.
	if len(req.LogEntries) > 0 {
		serv.log = append(serv.log[:req.PrevLogIndex+1], req.LogEntries...)
		log.Printf("handleAERequest: appended %d entries from %s\n", len(req.LogEntries), addr.String())
	}

	resp.Success = true

	// 5. Advance commitIndex to match the leader's, then write newly committed entries to the log file
	if req.LeaderCommit > int(serv.commitIndex.Load()) {
		newCommit := min(req.LeaderCommit, len(serv.log)-1)
		serv.commitUpTo(newCommit)
	}
	return resp
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

// INFO:
// 1. Reply false if term < currentTerm
// 2. If (votedFor is null or candidateId) and
// Candidate's log is at least as up-to-date as reciver's log, grant vote
func (serv *RaftServer) handleRVRequest(req miniraft.RequestVoteRequest) miniraft.RequestVoteResponse {
	resp := miniraft.RequestVoteResponse{
		Term: int(serv.currentTerm.Load()),
	}

	// If the incoming term is higher than ours, update our term and step down to follower.
	// Per Raft: "If RPC request or response contains term T > currentTerm:
	// set currentTerm = T, convert to follower." Applies to ALL server roles.
	if req.Term > int(serv.currentTerm.Load()) {
		serv.currentTerm.Store(int64(req.Term))
		if serv.state != Follower {
			serv.changeState(Follower)
		} else {
			// Already a follower — just clear votedFor for the new term
			serv.votedFor = ""
		}
	}

	// Update the response term after potential update above
	resp.Term = int(serv.currentTerm.Load())

	// 1. Deny if the candidate's term is less than ours
	if req.Term < int(serv.currentTerm.Load()) {
		log.Printf("handleRVRequest: denied %s, their term %d is less than ours %d\n", req.CandidateName, req.Term, int(serv.currentTerm.Load()))
		resp.VoteGranted = false
		return resp
	}

	// Deny if we already voted for someone else this term
	if serv.votedFor != "" && serv.votedFor != req.CandidateName {
		log.Printf("handleRVRequest: denied %s, already voted for %s\n", req.CandidateName, serv.votedFor)
		resp.VoteGranted = false
		return resp
	}

	// 2. Check if the candidate's log is at least as up-to-date as ours.
	// First compare the last log term — higher term wins.
	// If terms are equal, the longer log wins.
	ourLastIndex := len(serv.log) - 1
	ourLastTerm := serv.log[ourLastIndex].Term
	if req.LastLogTerm < ourLastTerm {
		log.Printf("handleRVRequest: denied %s, their last log term %d is less than ours %d\n", req.CandidateName, req.LastLogTerm, ourLastTerm)
		resp.VoteGranted = false
		return resp
	}
	if req.LastLogTerm == ourLastTerm && req.LastLogIndex < ourLastIndex {
		log.Printf("handleRVRequest: denied %s, their log is shorter than ours\n", req.CandidateName)
		resp.VoteGranted = false
		return resp
	}

	// Grant the vote and record it so we don't vote for someone else this term
	serv.votedFor = req.CandidateName
	serv.resetTimeout()
	resp.VoteGranted = true
	log.Printf("handleRVRequest: granted vote to %s\n", req.CandidateName)
	return resp
}

// TODO: change to follower if response term is higher than own.
func (serv *RaftServer) handleRVResponse(res miniraft.RequestVoteResponse) {
	if res.Term > int(serv.currentTerm.Load()) {
		log.Printf("handleRVResponse: response has higher term %d, stepping down\n", res.Term)
		serv.currentTerm.Store(int64(res.Term))
		serv.changeState(Follower)
		return
	}
	if !res.VoteGranted {
		return
	}
	serv.votes.Add(1)
	if int(serv.votes.Load()) >= (len(serv.servers)/2)+1 {
		serv.changeState(Leader)
	}
}

// handleClientCommand processes a client command based on the server's role.
// Leader: appends the command to the log and sends AppendEntries to all followers.
// Follower: forwards the command to the known leader.
// Candidate: drops the command (no leader to forward to).
// Caller must hold serv.mu.Lock().
func (serv *RaftServer) handleClientCommand(cmd miniraft.ClientCommand) {
	switch serv.state {
	case Leader:
		// Append the new entry to the leader's own log
		entry := miniraft.LogEntry{
			Index:       len(serv.log),
			Term:        int(serv.currentTerm.Load()),
			CommandName: cmd.Command,
		}
		serv.log = append(serv.log, entry)
		log.Printf("Leader appended entry %d: %s\n", entry.Index, entry.CommandName)

		// Immediately send AppendEntries to all followers with the new entry
		for i, s := range serv.servers {
			nextIdx := int(serv.nextIndex[i].Load())
			serv.sendAERequest(nextIdx, s, serv.log[nextIdx:])
		}

	case Follower:
		if serv.leaderAddr != nil {
			log.Printf("Forwarding client command %q to leader %s\n", cmd.Command, serv.leaderAddr.String())
			serv.sendMsg(&cmd, serv.leaderAddr)
		} else {
			log.Printf("No known leader, dropping client command %q\n", cmd.Command)
		}

	case Candidate:
		log.Printf("Candidate, dropping client command %q\n", cmd.Command)
	}
}

func (serv *RaftServer) handleMsg(bMsg []byte, addr *net.UDPAddr) {
	msg := &miniraft.RaftMessage{}
	msgType, err := msg.UnmarshalRaftJSON(bMsg)
	if err != nil {
		log.Printf("error unmarshalling json msg.\tmsg: %s\terror: %v\n", bMsg, err)
		return
	}

	serv.mu.Lock()
	defer serv.mu.Unlock()

	// Suspended servers receive messages but do not process or respond to them.
	if serv.state == Suspended {
		return
	}

	switch msgType {
	case miniraft.AppendEntriesRequestMessage:
		resp := serv.handleAERequest(msg.Message.(miniraft.AppendEntriesRequest), addr)
		serv.sendMsg(resp, addr)

	case miniraft.AppendEntriesResponseMessage:
		serv.handleAEResponse(msg.Message.(miniraft.AppendEntriesResponse), addr)

	case miniraft.RequestVoteRequestMessage:
		resp := serv.handleRVRequest(msg.Message.(miniraft.RequestVoteRequest))
		serv.sendMsg(resp, addr)

	case miniraft.RequestVoteResponseMessage:
		serv.handleRVResponse(msg.Message.(miniraft.RequestVoteResponse))

	case miniraft.ClientCommandMessage:
		cmd := msg.Message.(miniraft.ClientCommand)
		log.Printf("Client Command: %s", cmd.Command)
		serv.handleClientCommand(cmd)

	default:
		log.Printf("error unmarshalling json msg, no such message type.\nmsg: %v\ntype: %v\n", bMsg, msgType)
	}
}
