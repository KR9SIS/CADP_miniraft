package main

import (
	"fmt"
	"log"
	"net"
)

// handleAERequest processes an incoming AppendEntries request.
// Implements the 5 rules from Figure 2 of the Raft paper:
//  1. Reply false if term < currentTerm
//  2. Reply false if log doesn't have an entry at PrevLogIndex matching PrevLogTerm
//  3. If an existing entry conflicts with a new one (same index, different term), delete it and all after
//  4. Append any new entries not already in the log
//  5. If leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry)
func (serv *RaftServer) handleAERequest(req AppendEntriesRequest, addr *net.UDPAddr) AppendEntriesResponse {
	resp := AppendEntriesResponse{
		Term: serv.currentTerm,
	}

	// If the sender's term is higher, we update ours and become a follower (Figure 2)
	if req.Term > serv.currentTerm {
		serv.currentTerm = req.Term
		if serv.state != Follower {
			serv.changeState(Follower)
		} else {
			serv.votedFor = "" // new term, clear our vote
		}
	}

	// If we're a candidate and get an AE with our term, someone else won the election
	if serv.state == Candidate && req.Term >= serv.currentTerm {
		serv.changeState(Follower)
	}

	resp.Term = serv.currentTerm

	// 1. Reject if the request comes from an old term
	if req.Term < serv.currentTerm {
		log.Printf("handleAERequest: rejected from %s, their term %d is less than ours %d\n", addr.String(), req.Term, serv.currentTerm)
		resp.Success = false
		return resp
	}

	serv.resetTimeout()
	serv.leaderAddr = addr

	// 2. Reject if our log doesn't have a matching entry at PrevLogIndex
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

	// 3 & 4. Cut off any conflicting entries and append the new ones (skip if no entries)
	if len(req.LogEntries) > 0 {
		serv.log = append(serv.log[:req.PrevLogIndex+1], req.LogEntries...)
		log.Printf("handleAERequest: appended %d entries from %s\n", len(req.LogEntries), addr.String())
	}

	resp.Success = true

	// 5. Advance our commitIndex to match the leader's and write new commits to the log file
	if req.LeaderCommit > serv.commitIndex {
		newCommit := min(req.LeaderCommit, len(serv.log)-1)
		serv.commitUpTo(newCommit)
	}
	return resp
}

// handleAEResponse handles a follower's reply to our AppendEntries.
// On success we advance their nextIndex/matchIndex and try to commit.
// On failure we back up nextIndex by one and retry.
func (serv *RaftServer) handleAEResponse(res AppendEntriesResponse, addr *net.UDPAddr) {
	i := serv.getServerIdx(addr.String())
	if i == -1 {
		log.Printf("handleAEResponse: unknown server %s\n", addr.String())
		return
	}

	// Higher term means we're outdated, step down
	if res.Term > serv.currentTerm {
		log.Printf("handleAEResponse: response from %s has higher term %d, stepping down\n", addr.String(), res.Term)
		serv.currentTerm = res.Term
		serv.changeState(Follower)
		return
	}

	if res.Success {
		// Follower accepted, update what we know about their log
		serv.matchIndex[i] = max(serv.matchIndex[i], serv.nextIndex[i]-1)
		serv.nextIndex[i] = max(serv.nextIndex[i], len(serv.log))

		// See if we can commit more entries now
		serv.advanceCommitIndex()
		return
	}

	// Follower rejected, back up nextIndex and retry until we find where our logs match
	log.Printf("AEResponse from %s: failed, backing up and retrying\n", addr.String())
	if serv.nextIndex[i] > 1 {
		serv.nextIndex[i]--
	}
	nextIndex := serv.nextIndex[i]
	serv.sendAERequest(nextIndex, addr, serv.log[nextIndex:])
}

// handleRVRequest processes an incoming RequestVote request.
// From Figure 2 of the Raft paper:
//  1. Reply false if term < currentTerm
//  2. If votedFor is null or candidateId, and candidate's log is at least as
//     up-to-date as receiver's log, grant vote
func (serv *RaftServer) handleRVRequest(req RequestVoteRequest) RequestVoteResponse {
	resp := RequestVoteResponse{
		Term: serv.currentTerm,
	}

	// If the sender's term is higher, we update ours and become a follower (Figure 2)
	if req.Term > serv.currentTerm {
		serv.currentTerm = req.Term
		if serv.state != Follower {
			serv.changeState(Follower)
		} else {
			serv.votedFor = "" // new term, clear our vote
		}
	}

	resp.Term = serv.currentTerm

	// 1. Deny if the candidate's term is behind ours
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

	// 2. Only grant the vote if the candidate's log is at least as up-to-date as ours
	// (compare last term first, then length)
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

	// All checks passed, grant the vote
	serv.votedFor = req.CandidateName
	serv.resetTimeout()
	resp.VoteGranted = true
	log.Printf("handleRVRequest: granted vote to %s\n", req.CandidateName)
	return resp
}

// handleRVResponse processes a vote response. If we got enough votes, become leader.
func (serv *RaftServer) handleRVResponse(res RequestVoteResponse) {
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
	total := len(serv.servers) + 1 // all servers including self
	majority := total/2 + 1
	if serv.votes >= majority && serv.state != Leader {
		serv.changeState(Leader)
	}
}

// handleClientCommand processes a client command based on the server's role.
// Leader: appends the command to the log and sends AppendEntries to all followers.
// Follower: forwards the command to the known leader.
// Candidate: drops the command (no leader to forward to).
func (serv *RaftServer) handleClientCommand(cmd ClientCommand) {
	switch serv.state {
	case Leader:
		// Add the command to our log and replicate to followers
		entry := LogEntry{
			Index:       len(serv.log),
			Term:        serv.currentTerm,
			CommandName: cmd.Command,
		}
		serv.log = append(serv.log, entry)
		log.Printf("Leader appended client command entry %d: %s\n", entry.Index, entry.CommandName)

		// Send to all followers right away
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

// handleStdin processes debug commands typed into the server's terminal.
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
		log.Printf("Command '%s' not recognized, valid commands are: 'log', 'print', 'resume', & 'suspend'", str)
	}

	return oldState
}

// handleMsg unmarshals an incoming UDP message and dispatches it to the right handler.
func (serv *RaftServer) handleMsg(sMsg ServMsg) {
	bMsg := sMsg.bMsg
	addr := sMsg.addr
	msg := &RaftMessage{}
	msgType, err := msg.UnmarshalRaftJSON(bMsg)
	if err != nil {
		log.Printf("error unmarshalling json msg.\tmsg: %s\terror: %v\n", bMsg, err)
		return
	}

	// Failed servers ignore everything except client commands, which they forward to the leader
	if serv.state == Failed {
		if msgType == AppendEntriesRequestMessage {
			if serv.leaderAddr != nil && (serv.leaderAddr.IP.Equal(addr.IP) && serv.leaderAddr.Port == addr.Port) {
				return
			}
			// Remember this leader so we can forward client commands to it
			log.Printf("Failed server storing new leader %s to forward client commands", addr.String())
			serv.leaderAddr = addr
		}
		if msgType == ClientCommandMessage {
			cmd := msg.Message.(ClientCommand)
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
	case AppendEntriesRequestMessage:
		resp := serv.handleAERequest(msg.Message.(AppendEntriesRequest), addr)
		serv.sendMsg(resp, addr)

	case AppendEntriesResponseMessage:
		serv.handleAEResponse(msg.Message.(AppendEntriesResponse), addr)

	case RequestVoteRequestMessage:
		resp := serv.handleRVRequest(msg.Message.(RequestVoteRequest))
		serv.sendMsg(resp, addr)

	case RequestVoteResponseMessage:
		serv.handleRVResponse(msg.Message.(RequestVoteResponse))

	case ClientCommandMessage:
		cmd := msg.Message.(ClientCommand)
		serv.handleClientCommand(cmd)

	default:
		log.Printf("error unmarshalling json msg, no such message type.\nmsg: %v\ntype: %v\n", bMsg, msgType)
	}
}

// handler is the main event loop. All state changes happen here in a single goroutine,
// so we don't need any locks.
func (serv *RaftServer) handler(c <-chan ServMsg, strChan <-chan string) {
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
				// Leaders and failed servers just reset the timer, they don't start elections
				serv.resetTimeout()
			}
		}
	}
}
