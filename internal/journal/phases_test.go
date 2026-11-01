package journal

import "testing"

func TestPhasesValidAndIncomplete(t *testing.T) {
	for _, p := range All {
		if !p.Valid() {
			t.Errorf("%s not valid", p)
		}
	}
	if Phase("bogus").Valid() {
		t.Fatal("bogus phase is valid")
	}
	incomplete := []Phase{Planned, SafetyCaptured, ThreadForked, FilesRestoring, Verified, FailedRetryable}
	for _, p := range incomplete {
		if !p.Incomplete() {
			t.Errorf("%s should be incomplete", p)
		}
	}
	for _, p := range []Phase{Completed, FailedTerminal} {
		if p.Incomplete() {
			t.Errorf("%s should be complete", p)
		}
	}
	if !FailedRetryable.Failed() || !FailedTerminal.Failed() || Completed.Failed() {
		t.Fatal("Failed() wrong")
	}
}

func TestCanTransition(t *testing.T) {
	allowed := [][2]Phase{
		{Planned, SafetyCaptured},
		{SafetyCaptured, ThreadForked},
		{ThreadForked, FilesRestoring},
		{FilesRestoring, Verified},
		{Verified, Completed},
		{Planned, FailedRetryable},
		{FailedRetryable, ThreadForked},
		{FailedRetryable, FilesRestoring},
		{FailedRetryable, Completed},
		{FilesRestoring, FailedTerminal},
	}
	for _, pair := range allowed {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("expected %s -> %s allowed", pair[0], pair[1])
		}
	}
	denied := [][2]Phase{
		{Completed, Planned},
		{Completed, FilesRestoring},
		{FailedTerminal, FailedRetryable},
		{Planned, Verified},
		{ThreadForked, Completed},
		{Phase("x"), Planned},
	}
	for _, pair := range denied {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("expected %s -> %s denied", pair[0], pair[1])
		}
	}
}
