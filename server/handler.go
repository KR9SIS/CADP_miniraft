package main

import (
	"fmt"
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
		Term: serv.currentTerm,
	}

	// If the incoming term is higher than ours, update our term and step down to follower.
	// Per Raft: "If RPC request or response contains term T > currentTerm:
	// set currentTerm = T, convert to follower." Applies to ALL server roles.
	if req.Term > serv.currentTerm {
		serv.currentTerm = req.Term
		if serv.state != Follower {
			serv.changeState(Follower)
		} else {
			// Already a follower — just clear votedFor for the new term
			serv.votedFor = ""
		}
	}

	// If we're a candidate and get an AE with our same term, that means another
	// server already won the election so we step down.
	if serv.state == Candidate && req.Term >= serv.currentTerm {
		serv.changeState(Follower)
	}

	// Update the response term after potential update above
	resp.Term = serv.currentTerm

	// 1. Reject if the request is from a stale leader
	if req.Term < serv.currentTerm {
		log.Printf("handleAERequest: rejected from %s, their term %d is less than ours %d\n", addr.String(), req.Term, serv.currentTerm)
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
	if req.LeaderCommit > serv.commitIndex {
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
	if res.Term > serv.currentTerm {
		log.Printf("handleAEResponse: response from %s has higher term %d, stepping down\n", addr.String(), res.Term)
		serv.currentTerm = res.Term
		serv.changeState(Follower)
		return
	}

	if res.Success {
		// Update nextIndex and matchIndex based on what we actually sent (inflightIndex).
		// Use max() because UDP doesn't guarantee ordering, so a stale response
		// arriving late shouldn't overwrite a higher value from a newer response.
		lastSent := serv.inflightIndex[i]
		serv.nextIndex[i] = max(serv.nextIndex[i], lastSent+1)
		serv.matchIndex[i] = max(serv.matchIndex[i], lastSent)

		// Check if we can now commit more entries
		serv.advanceCommitIndex()
		return
	}

	// The follower rejected our entries, meaning its log doesn't match ours at nextIndex-1.
	// Back up nextIndex by one and retry with a longer suffix so we find where the logs are the same.
	log.Printf("AEResponse from %s: failed, backing up and retrying\n", addr.String())
	if serv.nextIndex[i] > 1 {
		serv.nextIndex[i]--
	}
	nextIndex := serv.nextIndex[i]
	serv.sendAERequest(nextIndex, addr, serv.log[nextIndex:])
}

// INFO:
// 1. Reply false if term < currentTerm
// 2. If (votedFor is null or candidateId) and
// Candidate's log is at least as up-to-date as reciver's log, grant vote
func (serv *RaftServer) handleRVRequest(req miniraft.RequestVoteRequest) miniraft.RequestVoteResponse {
	resp := miniraft.RequestVoteResponse{
		Term: serv.currentTerm,
	}

	// If the incoming term is higher than ours, update our term and step down to follower.
	// Per Raft: "If RPC request or response contains term T > currentTerm:
	// set currentTerm = T, convert to follower." Applies to ALL server roles.
	if req.Term > serv.currentTerm {
		serv.currentTerm = req.Term
		if serv.state != Follower {
			serv.changeState(Follower)
		} else {
			// Already a follower — just clear votedFor for the new term
			serv.votedFor = ""
		}
	}

	// Update the response term after potential update above
	resp.Term = serv.currentTerm

	// 1. Deny if the candidate's term is less than ours
	if req.Term < serv.currentTerm {
		log.Printf("handleRVRequest: denied %s, their term %d is less than ours %d\n", req.CandidateName, req.Term, serv.currentTerm)
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
	if res.Term > serv.currentTerm {
		log.Printf("handleRVResponse: response has higher term %d, stepping down\n", res.Term)
		serv.currentTerm = res.Term
		serv.changeState(Follower)
		return
	}
	if !res.VoteGranted {
		return
	}
	serv.votes++
	if (serv.votes >= (len(serv.servers)/2)+1) && serv.state != Leader {
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
			Term:        serv.currentTerm,
			CommandName: cmd.Command,
		}
		serv.log = append(serv.log, entry)
		log.Printf("Leader appended client command entry %d: %s\n", entry.Index, entry.CommandName)

		// Immediately send AppendEntries to all followers with the new entry
		for i, s := range serv.servers {
			nextIdx := serv.nextIndex[i]
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

func (serv *RaftServer) handleStdin(str string, oldState ServerState) ServerState {
	switch str {
	case "log":
		for _, entry := range serv.log {
			fmt.Printf("%+v\n", entry)
		}
	case "print":
		var state string
		if serv.state == Failed {
			state = fmt.Sprintf("%s, oldState: %s", serverStateStr[serv.state], serverStateStr[oldState])
		} else {
			state = serverStateStr[serv.state]
		}
		fmt.Printf("currentTerm: %d, votedFor: %s, state: %s, commitIndex: %d, lastApplied: %d, nextIndex:", serv.currentTerm, serv.votedFor, state, serv.commitIndex, serv.lastApplied)
		for i := range serv.nextIndex {
			fmt.Printf(" %d", serv.nextIndex[i])
		}
		fmt.Printf(", matchIndex:")
		for i := range serv.matchIndex {
			fmt.Printf(" %d", serv.matchIndex[i])
		}
		fmt.Printf("\n")
	case "resume":
		serv.changeState(oldState)
	case "suspend":
		oldState = serv.state
		serv.changeState(Failed)
	default:
		log.Printf("Command '%s' not regognized, valid commands are: 'log', 'print', 'resume', & 'suspend'", str)
	}

	return oldState
}

func (serv *RaftServer) handleMsg(sMsg serv_msg) {
	bMsg := sMsg.bMsg
	addr := sMsg.addr
	msg := &miniraft.RaftMessage{}
	msgType, err := msg.UnmarshalRaftJSON(bMsg)
	if err != nil {
		log.Printf("error unmarshalling json msg.\tmsg: %s\terror: %v\n", bMsg, err)
		return
	}

	// Failed servers don't respond to Raft RPCs, but
	// they should still forward client commands to the leader
	if serv.state == Failed {
		if msgType == miniraft.ClientCommandMessage {
			cmd := msg.Message.(miniraft.ClientCommand)
			if serv.leaderAddr != nil {
				log.Printf("Failed server forwarding client command %q to leader %s\n", cmd.Command, serv.leaderAddr.String())
				serv.sendMsg(&cmd, serv.leaderAddr)
			} else {
				log.Printf("Failed server has no known leader, dropping client command %q\n", cmd.Command)
			}
		}
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
		serv.handleClientCommand(cmd)

	default:
		log.Printf("error unmarshalling json msg, no such message type.\nmsg: %v\ntype: %v\n", bMsg, msgType)
	}
}

func (serv *RaftServer) handler(c <-chan serv_msg, strChan <-chan string) {
	var oldState ServerState
	for {
		select {
		case sMsg := <-c:
			serv.handleMsg(sMsg)

		case str := <-strChan:
			oldState = serv.handleStdin(str, oldState)

		case <-serv.heartbeatTicker.C:
			if serv.state == Leader {
				serv.sendHeartBeats()
			}

		case <-serv.eTimeout.C:
			switch serv.state {
			case Follower, Candidate:
				serv.changeState(Candidate)

			case Leader, Failed:
				// Leaders send heartbeats, they don't watch the election timer.
				// Failed servers don't participate in elections.
				serv.resetTimeout()
			}
		}
	}
}
