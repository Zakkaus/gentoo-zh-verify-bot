package tg

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
)

const sampleToken = "8684199281:AAFpLshbWf1GR6DqiZo2S0mxU8J_wfT0cqk"

func TestRedactTokenRemovesItFromAnAPIError(t *testing.T) {
	err := fmt.Errorf(`telego: getUpdates: request call: http do request: Post "http://127.0.0.1:8081/bot%s/getUpdates": connection refused`, sampleToken)
	got := RedactToken(err.Error())
	if strings.Contains(got, sampleToken) {
		t.Errorf("the token survived redaction: %q", got)
	}
	if !strings.Contains(got, "/bot<redacted>") || !strings.Contains(got, "connection refused") {
		t.Errorf("redaction lost the rest of the message: %q", got)
	}
}

// The writer is the guard that does not depend on any call site remembering.
func TestRedactingWriterCatchesWhatALogCallForgot(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(RedactingWriter(&buf), "", 0)
	err := errors.New(`Post "http://127.0.0.1:8081/bot` + sampleToken + `/editMessageText": context deadline exceeded`)
	logger.Printf("feed: edit tracked bug 981623 in -1004447700480: %v", err)
	out := buf.String()
	if strings.Contains(out, sampleToken) {
		t.Errorf("the token reached the log: %q", out)
	}
	for _, keep := range []string{"feed: edit tracked bug 981623", "context deadline exceeded", "/bot<redacted>"} {
		if !strings.Contains(out, keep) {
			t.Errorf("log line lost %q: %q", keep, out)
		}
	}
}

func TestRedactingWriterReportsTheCallersLength(t *testing.T) {
	var buf bytes.Buffer
	w := RedactingWriter(&buf)
	p := []byte("/bot" + sampleToken + "/getMe\n")
	n, err := w.Write(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(p) {
		t.Errorf("Write reported %d of %d bytes; a short count looks like a partial write to log", n, len(p))
	}
}

func TestRedactionLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"join 8745264859 in group -1001114146292: pending",
		"telego: getUpdates: Post \"http://127.0.0.1:8081/botBOT_TOKEN/getUpdates\": timeout",
		"/bot123",
	} {
		if got := RedactToken(s); got != s {
			t.Errorf("RedactToken(%q) = %q, want it unchanged", s, got)
		}
	}
}
