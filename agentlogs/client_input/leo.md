# Client Input

Asked Leo a query, Claude Haiku was the LLM that answered.

## Prompt

```go
 msg := make(map[string]string)
 // resp := make([]byte, maxBufferSize)

 scanner := bufio.NewScanner(os.Stdin)
 log.Printf("Command: ")
 for scanner.Scan() {
  cmd := scanner.Text()
  if cmd == "exit" {
   break
  }

  m, err := regexp.Match("[^\\w\\-]", []byte(cmd))
  if err != nil {
   log.Printf("Error matching cmd: %s, err: %v", cmd, err)
  }
  if m {
   log.Printf("Invalid cmd: %s\ncmd must only contain upper-case letters, lower-case letters, digits, dashes and underscores.\n", cmd)
   continue
  }

  msg["Command"] = cmd
  bMsg, err := json.Marshal(msg)
  if err != nil {
   log.Printf("Failed to marshal cmd: %s\nerr: %v", cmd, err)
   continue
  }

  _, err = conn.WriteTo(bMsg, dst)
  if err != nil {
   log.Printf("Failed to send '%s' to %v", bMsg, dst)
   continue
  }

  // _, src, _ := conn.ReadFrom(resp)
  // log.Printf("Response from %s\n", src.String())

  log.Printf("Command: ")
 }

 if err := scanner.Err(); err != nil {
  log.Println(err)
 }

```

Is there a good way to make Scan() print out the "Command: " string before taking user input? Currently the input cursor shows up on a newline.

## Response

Based on your user preferences, I'll search for a reference on this Go I/O behavior.

The issue you're describing is a common one with `bufio.Scanner` — it doesn't flush output before reading. Here's the standard solution:

Use `fmt.Print()` instead of `log.Printf()` for the prompt, and ensure it's flushed before scanning:

```go
fmt.Print("Command: ")
for scanner.Scan() {
    cmd := scanner.Text()
    // ... your logic ...
    fmt.Print("Command: ")
}
```

The key difference: `fmt.Print()` writes to stdout directly and flushes immediately, while `log.Printf()` may buffer output. This ensures "Command: " appears before the input cursor.

Alternatively, if you need logging functionality, you can explicitly flush after logging:

```go
log.Printf("Command: ")
os.Stdout.Sync()
```

However, `fmt.Print()` is the cleaner approach for interactive prompts.
