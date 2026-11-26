package main

import (
	"os/user"
	"testing"
)

// Task Scheduler reports the task's user as a SID; the selftest must still see it as hers.
func TestTaskUserAsSIDIsCurrentUser(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	if !isCurrentUser(me.Uid) {
		t.Errorf("%s (the current user's Uid) not recognised as the current user", me.Uid)
	}
	if isCurrentUser("S-1-5-21-1-2-3-999999") {
		t.Error("another user's SID recognised as the current user")
	}
}
