package tg

import (
	"context"
	"errors"
	"testing"
	"time"
)

func withShortDeleteRetry(t *testing.T) {
	t.Helper()
	previous := deleteRetryDelay
	deleteRetryDelay = time.Millisecond
	t.Cleanup(func() { deleteRetryDelay = previous })
}

func TestDeleteRetriesATransientFailure(t *testing.T) {
	withShortDeleteRetry(t)
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"deleteMessage": {{err: errors.New("unexpected EOF")}},
	}}
	client := newTestClient(t, caller)
	client.Delete(context.Background(), -100, 42)
	waitForMethodCalls(t, caller, "deleteMessage", 2)
	waitForCleanupTimerCount(t, client, 0)
}

func TestDeleteDoesNotRetryAMessageThatIsAlreadyGone(t *testing.T) {
	withShortDeleteRetry(t)
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"deleteMessage": {{err: errors.New("Bad Request: message to delete not found")}},
	}}
	client := newTestClient(t, caller)
	client.Delete(context.Background(), -100, 42)
	time.Sleep(20 * time.Millisecond)
	if got := len(caller.methodCalls("deleteMessage")); got != 1 {
		t.Errorf("deleteMessage called %d times; a message that is already gone needs no retry", got)
	}
}

func TestDeleteGivesUpAfterItsRetries(t *testing.T) {
	withShortDeleteRetry(t)
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"deleteMessage": {
			{err: errors.New("unexpected EOF")},
			{err: errors.New("unexpected EOF")},
			{err: errors.New("unexpected EOF")},
			{err: errors.New("unexpected EOF")},
		},
	}}
	client := newTestClient(t, caller)
	client.Delete(context.Background(), -100, 42)
	waitForMethodCalls(t, caller, "deleteMessage", deleteRetries+1)
	waitForCleanupTimerCount(t, client, 0)
	time.Sleep(20 * time.Millisecond)
	if got := len(caller.methodCalls("deleteMessage")); got != deleteRetries+1 {
		t.Errorf("deleteMessage called %d times; want %d attempts and no more", got, deleteRetries+1)
	}
}

func TestDeleteIgnoresAZeroMessageID(t *testing.T) {
	caller := &scriptedCaller{}
	client := newTestClient(t, caller)
	client.Delete(context.Background(), -100, 0)
	if got := len(caller.methodCalls("deleteMessage")); got != 0 {
		t.Errorf("deleteMessage called %d times for a zero message ID", got)
	}
}
