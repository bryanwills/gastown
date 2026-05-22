package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

type fakeInboxLister struct {
	calls    int
	messages []*mail.Message
	err      error
}

func (f *fakeInboxLister) List() ([]*mail.Message, error) {
	f.calls++
	return f.messages, f.err
}

type clearCall struct {
	address  string
	threadID string
}

type fakeSatisfiedClearer struct {
	calls []clearCall
	err   error
}

func (f *fakeSatisfiedClearer) ClearSatisfiedNotifications(address, threadID string) error {
	f.calls = append(f.calls, clearCall{address: address, threadID: threadID})
	return f.err
}

func TestLoadInboxSnapshotListsOnceAndCounts(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "msg-1", Read: false},
			{ID: "msg-2", Read: true},
			{ID: "msg-3", Read: false},
		},
	}

	messages, total, unread, err := loadInboxSnapshot(box, false)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
	if total != 3 || unread != 2 {
		t.Fatalf("counts = (%d total, %d unread), want (3, 2)", total, unread)
	}
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}
}

func TestLoadInboxSnapshotUnreadOnlyFiltersAfterSingleList(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "msg-1", Read: false},
			{ID: "msg-2", Read: true},
			{ID: "msg-3", Read: false},
		},
	}

	messages, total, unread, err := loadInboxSnapshot(box, true)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
	if total != 3 || unread != 2 {
		t.Fatalf("counts = (%d total, %d unread), want (3, 2)", total, unread)
	}
	if len(messages) != 2 {
		t.Fatalf("filtered messages len = %d, want 2", len(messages))
	}
	if messages[0].ID != "msg-1" || messages[1].ID != "msg-3" {
		t.Fatalf("filtered messages = [%s %s], want [msg-1 msg-3]", messages[0].ID, messages[1].ID)
	}
}

func TestLoadInboxSnapshotPropagatesListError(t *testing.T) {
	wantErr := errors.New("list failed")
	box := &fakeInboxLister{err: wantErr}

	_, _, _, err := loadInboxSnapshot(box, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
}

func TestMailMarkReadSingleClearsSatisfiedNudges(t *testing.T) {
	clearer := &fakeSatisfiedClearer{}
	msg := &mail.Message{ID: "hq-msg1", ThreadID: "thread-1"}

	clearSatisfiedMailNudges(clearer, "gastown/crew/bob", msg)

	if len(clearer.calls) != 1 {
		t.Fatalf("clear calls = %d, want 1", len(clearer.calls))
	}
	if clearer.calls[0].address != "gastown/crew/bob" {
		t.Fatalf("clear address = %q, want %q", clearer.calls[0].address, "gastown/crew/bob")
	}
	if clearer.calls[0].threadID != "thread-1" {
		t.Fatalf("clear thread = %q, want %q", clearer.calls[0].threadID, "thread-1")
	}
}

func TestMailMarkReadAllClearsSatisfiedNudgesForEachMessage(t *testing.T) {
	clearer := &fakeSatisfiedClearer{}
	messages := []*mail.Message{
		{ID: "hq-msg1", ThreadID: "thread-1"},
		{ID: "hq-msg2", ThreadID: "thread-2"},
		{ID: "hq-msg3"},
	}

	for _, msg := range messages {
		clearSatisfiedMailNudges(clearer, "gastown/crew/bob", msg)
	}

	if len(clearer.calls) != 2 {
		t.Fatalf("clear calls = %d, want 2", len(clearer.calls))
	}
	if clearer.calls[0].threadID != "thread-1" {
		t.Fatalf("first clear thread = %q, want %q", clearer.calls[0].threadID, "thread-1")
	}
	if clearer.calls[1].threadID != "thread-2" {
		t.Fatalf("second clear thread = %q, want %q", clearer.calls[1].threadID, "thread-2")
	}
}
