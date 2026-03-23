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

func TestCreateServiceModalRequiresPathForPathlessProject(t *testing.T) {
	var modal formModal
	modal.openCreateService("Distributed", true)

	if !modal.fields[1].required {
		t.Fatal("expected service path field to be required for pathless projects")
	}

	modal.fields[0].value = "api"
	submit, close := modal.handleKey("enter")
	if submit || close {
		t.Fatalf("expected enter on first field to advance, got submit=%v close=%v", submit, close)
	}

	submit, close = modal.handleKey("enter")
	if submit || close {
		t.Fatalf("expected blank required path to block submission, got submit=%v close=%v", submit, close)
	}
	if modal.errMsg == "" {
		t.Fatal("expected validation error for blank required service path")
	}
}
