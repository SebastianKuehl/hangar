package main

import "testing"

func TestCreateProjectModalAllowsEmptyPath(t *testing.T) {
	var modal formModal
	modal.openCreateProject()

	if modal.fields[1].required {
		t.Fatal("expected project path field to be optional")
	}

	modal.fields[0].value = "Distributed"
	submit, close := modal.handleKey("enter")
	if submit || close {
		t.Fatalf("expected enter on first field to advance, got submit=%v close=%v", submit, close)
	}

	submit, close = modal.handleKey("enter")
	if !submit || close {
		t.Fatalf("expected blank project path to submit, got submit=%v close=%v", submit, close)
	}
}
