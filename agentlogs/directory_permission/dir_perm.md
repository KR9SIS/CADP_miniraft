# Go log directory permission issue in Go server

**Session ID:** ses_2d10ca50affeoA6Vm0I1iaX3Pu
**Created:** 3/27/2026, 10:59:53 AM
**Updated:** 3/27/2026, 11:00:14 AM

---

## User

read server.go I'm trying to create the log/ directory with enough read and write permissions so that I can then create or append to log files within that directory. But I'm getting this error
2026/03/27 10:58:41 error reading log/127.0.0.1-3000.log file: open log/127.0.0.1-3000.log: permission denied

Even though I've given MkdirAll read and write permissions with 666

---

## Assistant (Plan · big-pickle · 4.2s)

_Thinking:_

The user is asking about a permission issue with creating a log directory and log files. They're getting a "permission denied" error even though they used MkdirAll with 666 permissions.

Let me first read the server.go file to understand the code and the issue.

**Tool: read**

**Input:**

```json
{
  "filePath": "/home/kr9sis/drive/Code/RU_CADP/Prog/P3_Distributed_Consensus/server.go"
}
```

**Output:**

```
<path>/home/kr9sis/drive/Code/RU_CADP/Prog/P3_Distributed_Consensus/server.go</path>
<type>file</type>
<content>1: package main
2: 
3: import (
4:  "bufio"
5:  "log"
6:  "math/rand"
7:  "net"
8:  "os"
9:  "strconv"
10:  "strings"
11:  "time"
12: )
13: 
14: // INFO:
15: // Safely changes the servers state
16: // Spawns extra go routines to unlock stateLock
17: // changeState changes the server's state. Caller must hold serv.mu.Lock().
18: func (serv *RaftServer) changeState(state ServerState) {
19:  // Stop the heartbeat ticker whenever leaving Leader state
20:  serv.heartbeatTicker.Stop()
21: 
22:  switch state {
23:  case Failed:
24:   serv.state = Failed
25:  case Follower:
26:   serv.state = Follower
27:   serv.votedFor = ""
28:   serv.resetTimeout()
29:  case Candidate:
30:   serv.state = Candidate
31:   serv.leaderAddr = nil
32:   serv.startElection()
33:  case Leader:
34:   serv.state = Leader
35:   serv.votedFor = ""
36:   for i := range serv.servers {
37:    serv.nextIndex[i] = len(serv.log)
38:    serv.matchIndex[i] = 0
39:   }
40:   serv.heartbeatTicker.Reset(time.Millisecond * heartbeatTimeout)
41:   // Send the first heartbeat right away so followers know we're the leader
42:   // instead of waiting 75ms for the ticker to fire
43:   serv.sendHeartBeats()
44:  }
45:  log.Printf("Changed %s state to %s\n", serv.id, serverStateStr[serv.state])
46: }
47: 
48: func (serv *RaftServer) startElection() {
49:  log.Printf("%s starting election\n", serv.id)
50:  serv.currentTerm++
51:  serv.votes = 1
52:  serv.resetTimeout()
53:  serv.votedFor = serv.id
54:  for _, s := range serv.servers {
55:   lLE := serv.log[len(serv.log)-1]
56:   rVReq := &RequestVoteRequest{
57:    Term:          serv.currentTerm,
58:    LastLogIndex:  lLE.Index,
59:    LastLogTerm:   lLE.Term,
60:    CandidateName: serv.id,
61:   }
62: 
63:   serv.sendMsg(rVReq, s)
64:  }
65: }
66: 
67: // sendHeartBeats sends a single round of AppendEntries (heartbeats or pending entries) to all followers.
68: // Called from the handler event loop on each heartbeat tick, not in a separate goroutine.
69: func (serv *RaftServer) sendHeartBeats() {
70:  for i, s := range serv.servers {
71:   nextIdx := serv.nextIndex[i]
72:   serv.sendAERequest(nextIdx, s, serv.log[nextIdx:])
73:  }
74: }
75: 
76: // Returns the index of the server with the given "host:port" ID in the servers slice, or -1 if not found.
77: func (serv *RaftServer) getServerIdx(serverID string) int {
78:  for i, s := range serv.servers {
79:   if s.String() == serverID {
80:    return i
81:   }
82:  }
83:  return -1
84: }
85: 
86: func (serv *RaftServer) sendMsg(message any, addr *net.UDPAddr) {
87:  rMsg := RaftMessage{
88:   Message: message,
89:  }
90:  bMsg, err := rMsg.MarshalRaftJson()
91:  if err != nil {
92:   log.Printf("error marshalling response to %s\nresponse: %v\nerror: %v", addr.String(), message, err)
93:   return
94:  }
95:  if _, err := serv.conn.WriteToUDP(bMsg, addr); err != nil {
96:   log.Printf("error sending to %s: %v\n", addr.String(), err)
97:  }
98: }
99: 
100: func (serv *RaftServer) sendAERequest(nextIndex int, addr *net.UDPAddr, entries []LogEntry) {
101:  // Record the last index we are sending so handleAEResponse knows what the follower confirmed.
102:  // Only update for actual entries, not heartbeats (empty entries), since for heartbeats
103:  // the math would give nextIndex-1 which could regress the value.
104:  i := serv.getServerIdx(addr.String())
105:  if i != -1 && len(entries) > 0 {
106:   serv.inflightIndex[i] = nextIndex + len(entries) - 1
107:  }
108:  aer := &AppendEntriesRequest{
109:   Term:         serv.currentTerm,
110:   PrevLogIndex: nextIndex - 1,
111:   PrevLogTerm:  serv.log[nextIndex-1].Term,
112:   LeaderId:     serv.id,
113:   LeaderCommit: serv.commitIndex,
114:   LogEntries:   entries,
115:  }
116:  if len(entries) != 0 {
117:   log.Printf("%s sending AER to %s\n", serv.id, addr.String())
118:  }
119:  serv.sendMsg(aer, addr)
120: }
121: 
122: // advanceCommitIndex checks if any new log entries can be committed.
123: // advanceCommitIndex checks if any new log entries can be committed.
124: // An entry is committed when a majority of servers have it in their log.
125: func (serv *RaftServer) advanceCommitIndex() {
126:  total := len(serv.servers) + 1 // all servers including the leader
127:  majority := total/2 + 1
128: 
129:  // Try to commit each entry starting from the one after the current commitIndex
130:  for n := serv.commitIndex + 1; n < len(serv.log); n++ {
131:   // The leader always has its own entries so we start the count at 1
132:   count := 1
133:   for j := range serv.servers {
134:    if serv.matchIndex[j] >= n {
135:     count++
136:    }
137:   }
138: 
139:   // We can only commit entries from our own term (Raft safety rule).
140:   // Old entries from previous terms get committed as a side effect when
141:   // we commit a newer entry (the inner loop below writes everything up to n).
142:   entryIsFromCurrentTerm := serv.log[n].Term == serv.currentTerm
143:   if count >= majority && entryIsFromCurrentTerm {
144:    serv.commitUpTo(n)
145:   } else {
146:    // Can't commit n, so no point checking higher indexes either
147:    break
148:   }
149:  }
150: }
151: 
152: func (serv *RaftServer) logEntry(entry LogEntry) (err error) {
153:  term := strconv.Itoa(entry.Term)
154:  idx := strconv.Itoa(entry.Index)
155:  if _, err := serv.logFile.Write([]byte(term + "," + idx + "," + entry.CommandName + "\n")); err != nil {
156:   return err
157:  }
158:  return nil
159: }
160: 
161: // commitUpTo writes all entries from commitIndex+1 up to n to the log file and advances commitIndex.
162: // Used by both the leader (advanceCommitIndex) and followers (handleAERequest).
163: func (serv *RaftServer) commitUpTo(n int) {
164:  for idx := serv.commitIndex + 1; idx <= n; idx++ {
165:   err := serv.logEntry(serv.log[idx])
166:   if err != nil {
167:    log.Printf("commitUpTo: error writing entry %d to log file: %v\n", idx, err)
168:   }
169:  }
170:  serv.commitIndex = n
171:  serv.lastApplied = n
172:  log.Printf("commitUpTo: committed up to index %d\n", n)
173: }
174: 
175: func (serv *RaftServer) getStdin(c chan<- string) {
176:  scanner := bufio.NewScanner(os.Stdin)
177:  for scanner.Scan() {
178:   c <- scanner.Text()
179:  }
180: }
181: 
182: func (serv *RaftServer) serve(c chan<- serv_msg) (err error) {
183:  serverConn, err := net.ListenUDP("udp", serv.addr)
184:  if err != nil {
185:   log.Fatalf("failed to listen on port %d: %v\n", serv.addr.Port, err)
186:  }
187:  defer serverConn.Close()
188:  serv.conn = serverConn
189:  buffer := make([]byte, maxBufferSize)
190:  log.Printf("%s listening", serv.id)
191:  for {
192:   n, addr, err := serverConn.ReadFromUDP(buffer)
193:   if err != nil {
194:    log.Printf("error recvieving %d bytes from %s: %v\n", n, addr, err)
195:    continue
196:   }
197:   sMsg := serv_msg{
198:    addr: addr,
199:    bMsg: make([]byte, n),
200:   }
201:   copy(sMsg.bMsg, buffer[:n])
202:   c <- sMsg
203:  }
204: }
205: 
206: func (serv *RaftServer) resetTimeout() {
207:  timeout := rand.Intn(maxElectionTimeout-minElectionTimeout) + minElectionTimeout
208:  d := time.Duration(timeout) * time.Millisecond
209:  serv.eTimeout.Reset(d)
210: }
211: 
212: func main() {
213:  if len(os.Args) != 3 {
214:   log.Fatalf("Usage: %s <host>:<port> <server id file>\n", os.Args[0])
215:  }
216:  id := os.Args[1]
217:  file := os.Args[2]
218: 
219:  data, err := os.ReadFile(file)
220:  if err != nil {
221:   log.Fatalf("Error reading %s: %v", id, err)
222:  }
223: 
224:  servers := make([]*net.UDPAddr, 0, 3)
225:  hbTicker := time.NewTicker(time.Millisecond * heartbeatTimeout)
226:  hbTicker.Stop() // stopped initially; started when becoming Leader
227:  serv := &RaftServer{
228:   id:              id,
229:   state:           Follower,
230:   eTimeout:        time.NewTimer(time.Second),
231:   heartbeatTicker: hbTicker,
232:  }
233:  defer serv.eTimeout.Stop()
234:  defer serv.heartbeatTicker.Stop()
235:  serv.resetTimeout()
236:  valid_id := false
237: 
238:  dataStr := strings.TrimRight(string(data), "\n")
239:  for s := range strings.SplitSeq(dataStr, "\n") {
240:   addr, err := net.ResolveUDPAddr("udp", s)
241:   if err != nil {
242:    log.Fatalf("error resolving %s: %v\n", s, err)
243:   }
244:   if s == id {
245:    valid_id = true
246:    serv.addr = addr
247:   } else {
248:    servers = append(servers, addr)
249:   }
250:  }
251:  if !valid_id {
252:   log.Fatalf("\"%s\" must be in %s\nContents of %s:\n%s\n", id, file, file, data)
253:  }
254: 
255:  // The log starts with a dummy entry at index 0 so we can always safely access log[nextIndex-1]
256:  serv.log = make([]LogEntry, 1, 16)
257:  serv.nextIndex = make([]int, len(servers))
258:  serv.matchIndex = make([]int, len(servers))
259:  serv.inflightIndex = make([]int, len(servers))
260:  serv.servers = servers
261: 
262:  logDir := "log/"
263:  err = os.MkdirAll(logDir, 0o666)
264:  // filename = host-port.log
265:  filename := logDir + serv.addr.IP.String() + "-" + strconv.Itoa(serv.addr.Port) + ".log"
266:  f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o644)
267:  if err != nil {
268:   log.Fatalf("error reading %s file: %v", filename, err)
269:  }
270:  defer f.Close()
271:  serv.logFile = f
272: 
273:  log.Printf("%+v\n", serv)
274: 
275:  sMsgChan := make(chan serv_msg, 100)
276:  stdinChan := make(chan string)
277: 
278:  go serv.handler(sMsgChan, stdinChan)
279:  go serv.getStdin(stdinChan)
280:  serv.serve(sMsgChan)
281: }

(End of file - total 281 lines)
</content>
```

---

## Assistant (Plan · big-pickle · 16.1s)

_Thinking:_

I can see the issue. On line 263, the code creates the `log/` directory with `os.MkdirAll(logDir, 0o666)`. Then on line 266, it opens the log file with `os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o644)`.

The problem is twofold:

1. **Directory permissions don't work the way the user thinks**: The `0o666` permission for `MkdirAll` is the _default_ permissions for newly created files within that directory, but the actual directory permissions will be affected by the system's umask. On most Linux systems, the default umask is `022`, which means:
   - If you request `0o666` for a directory, the actual permissions become `0o666 & ^0o022 = 0o644`
   - Directories need execute permission to be traversed (enter), so `0o644` means no one can actually enter the directory!

2. **The error message says "permission denied"**: This is happening because the directory was likely created with `0o666` but the system's umask stripped the execute bit, leaving something like `0o644` (or if root created it, something else). Without execute permission on the directory, you cannot read or write files _inside_ the directory.

3. **Go's os.OpenFile with os.O_CREATE and 0o644**: This file permission is fine, but the directory permissions are the real issue.

The fix would be:

- Use `0o777` for the directory (or at minimum `0o755` to allow read+execute for all and read+write+execute for owner) so that the umask can work properly.
- Actually, directories should have the execute bit set. A directory with mode `0o666` (without execute) is essentially useless because you can't traverse it.

The user should use something like `0o755` or `0o777` for the directory permission in `MkdirAll`.

The execute bit is essential for directories—you can't traverse or access files within a directory without it. Additionally, the system's umask further restricts permissions, so even with `0o666`, the actual permissions end up being `0o644` with no execute bits, which prevents any access to the directory's contents.

I see the issue. The problem is on **line 263**:

```go
err = os.MkdirAll(logDir, 0o666)
```

**The core issue:** Directory permissions work differently from file permissions. The `0o666` mode is missing the **execute bit**, which is required to _enter_ and traverse a directory. Without execute permission on the directory itself, you cannot read or write files inside it—even if the files have correct permissions.

Additionally, on most Linux systems, the shell's `umask` further masks permissions (typically `022`), so even requesting `0o666` results in something like `0o644` after umask is applied.

**Fix:** Use `0o755` (or `0o777` if you want group/others to also write) for the directory:

```go
err = os.MkdirAll(logDir, 0o755)
```

This gives:

- Owner: read + write + execute (rwx)
- Group: read + execute (r-x)
- Others: read + execute (r-x)

The execute bit is critical—it allows the directory to be traversed.

---
