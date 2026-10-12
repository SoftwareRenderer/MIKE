package store

import (
	"testing"
	"time"
)

// One identity answers its first result to every later arrival of itself; a different identity for the same
// words stays a different write.

func TestClaimAnswersTheFirstResult(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("claim-proj", "claim-proj", "/tmp/claim-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("claim-task", "claim-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if _, claimed := s.ClaimedWrite("token-1", "claim-task"); claimed {
		t.Fatal("an identity nobody has written with is not claimed")
	}
	if err := s.ClaimWrite("token-1", "claim-proj", "claim-task", ClaimSend, "msg-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if id, claimed := s.ClaimedWrite("token-1", "claim-task"); !claimed || id != "msg-1" {
		t.Fatalf("claimed write answered (%q, %v), want (msg-1, true)", id, claimed)
	}
	if err := s.ClaimWrite("token-1", "claim-proj", "claim-task", ClaimSend, "msg-2"); err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if id, _ := s.ClaimedWrite("token-1", "claim-task"); id != "msg-1" {
		t.Fatalf("a re-claim moved the answer to %q", id)
	}
}

func TestClaimOfAnotherIdentityIsItsOwn(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("claim-proj", "claim-proj", "/tmp/claim-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("claim-task", "claim-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.ClaimWrite("token-1", "claim-proj", "claim-task", ClaimSend, "msg-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Two identities for the same words are two writes: the guard never decides that a repeat is unwanted.
	if err := s.ClaimWrite("token-2", "claim-proj", "claim-task", ClaimSend, "msg-2"); err != nil {
		t.Fatalf("claim for the second identity: %v", err)
	}
	if id, claimed := s.ClaimedWrite("token-2", "claim-task"); !claimed || id != "msg-2" {
		t.Fatalf("second identity answered (%q, %v), want (msg-2, true)", id, claimed)
	}
}

func TestNoIdentityClaimsNothing(t *testing.T) {
	s := testStore(t)
	if err := s.ClaimWrite("", "claim-proj", "claim-task", ClaimSend, "msg-1"); err != nil {
		t.Fatalf("an empty identity must claim nothing and answer no error, got %v", err)
	}
	if _, claimed := s.ClaimedWrite("", "claim-task"); claimed {
		t.Fatal("the empty identity answered as claimed")
	}
}

func TestDeletingTheTaskDropsItsClaim(t *testing.T) {
	s := testStore(t)
	proj := newProject("claim-proj", "claim-proj", "/tmp/claim-proj")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("claim-task", "claim-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.ClaimWrite("token-1", "claim-proj", "claim-task", ClaimCreateTask, "claim-task"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := s.deleteRows(t.Context(), `DELETE FROM tasks WHERE id = ?`, "delete task", "claim-task"); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	// A claim outliving its task would answer a row no read can reach.
	if _, claimed := s.ClaimedWrite("token-1", "claim-task"); claimed {
		t.Fatal("the claim outlived the task it wrote")
	}
}

// The lookup carries the scope the request named, so one task's identity never answers for another.
func TestClaimAnswersOnlyTheTaskItWasWrittenIn(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("claim-proj", "claim-proj", "/tmp/claim-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("claim-task", "claim-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateTask(newTask("other-task", "claim-proj")); err != nil {
		t.Fatalf("create the other task: %v", err)
	}
	if err := s.ClaimWrite("token-1", "claim-proj", "claim-task", ClaimSend, "msg-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if id, claimed := s.ClaimedWrite("token-1", "other-task"); claimed {
		t.Fatalf("another task's identity answered %q", id)
	}
	if id, claimed := s.ClaimedWrite("token-1", "claim-task"); !claimed || id != "msg-1" {
		t.Fatalf("its own task answered (%q, %v), want (msg-1, true)", id, claimed)
	}

	if err := s.ClaimWrite("create-1", "claim-proj", "claim-task", ClaimCreateTask, "claim-task"); err != nil {
		t.Fatalf("claim the create: %v", err)
	}
	if id, claimed := s.ClaimedCreate("create-1", "claim-proj"); !claimed || id != "claim-task" {
		t.Fatalf("ClaimedCreate answered (%q, %v), want (claim-task, true)", id, claimed)
	}
	if id, claimed := s.ClaimedCreate("create-1", "other-proj"); claimed {
		t.Fatalf("another project's identity answered %q", id)
	}
}

// The table only shrinks on its own while the write that grows it also prunes it; a horizon nobody enforces
// leaves one row per message forever.
func TestAClaimPastTheHorizonStopsAnswering(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("claim-proj", "claim-proj", "/tmp/claim-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("claim-task", "claim-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Age a claim through the real writer, so its row keeps the shape the writer gives it.
	age := func(token string, since time.Duration) {
		if err := s.ClaimWrite(token, "claim-proj", "claim-task", ClaimSend, "msg-"+token); err != nil {
			t.Fatalf("claim %s: %v", token, err)
		}
		if _, err := s.db.Exec(`UPDATE client_claims SET created_at = ? WHERE token = ?`,
			time.Now().UTC().Add(-since).Format(time.RFC3339), token); err != nil {
			t.Fatalf("age %s: %v", token, err)
		}
	}
	age("spent", claimRetention+time.Hour)
	age("recent", claimRetention-time.Hour)

	if err := s.ClaimWrite("fresh", "claim-proj", "claim-task", ClaimSend, "msg-fresh"); err != nil {
		t.Fatalf("claim the fresh write: %v", err)
	}

	if _, claimed := s.ClaimedWrite("spent", "claim-task"); claimed {
		t.Fatal("a claim past the horizon still answers")
	}
	// Inside the horizon the guard still holds: a queue can sit unread for weeks.
	if id, claimed := s.ClaimedWrite("recent", "claim-task"); !claimed || id != "msg-recent" {
		t.Fatalf("a claim inside the horizon answered (%q, %v), want (msg-recent, true)", id, claimed)
	}
	if id, claimed := s.ClaimedWrite("fresh", "claim-task"); !claimed || id != "msg-fresh" {
		t.Fatalf("the write that pruned lost its own claim: (%q, %v)", id, claimed)
	}
}
