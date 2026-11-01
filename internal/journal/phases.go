// Package journal defines the rewind operation phase state machine.
package journal

// Phase is one durable stage of a rewind operation.
type Phase string

const (
	Planned         Phase = "planned"
	SafetyCaptured  Phase = "safety_captured"
	ThreadForked    Phase = "thread_forked"
	FilesRestoring  Phase = "files_restoring"
	Verified        Phase = "verified"
	Completed       Phase = "completed"
	FailedRetryable Phase = "failed_retryable"
	FailedTerminal  Phase = "failed_terminal"
)

// All lists every valid phase.
var All = []Phase{Planned, SafetyCaptured, ThreadForked, FilesRestoring, Verified, Completed, FailedRetryable, FailedTerminal}

func (p Phase) Valid() bool {
	for _, v := range All {
		if p == v {
			return true
		}
	}
	return false
}

// Incomplete reports whether the operation still needs work or review.
func (p Phase) Incomplete() bool {
	switch p {
	case Planned, SafetyCaptured, ThreadForked, FilesRestoring, Verified, FailedRetryable:
		return true
	}
	return false
}

// Failed reports whether the operation ended in an error state.
func (p Phase) Failed() bool {
	return p == FailedRetryable || p == FailedTerminal
}

// CanTransition validates the normal forward path, failures, and failure retry.
func CanTransition(from, to Phase) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if to == FailedRetryable || to == FailedTerminal {
		return from.Incomplete()
	}
	switch from {
	case Planned:
		return to == SafetyCaptured || to == FailedRetryable
	case SafetyCaptured:
		return to == ThreadForked || to == FailedRetryable
	case ThreadForked:
		return to == FilesRestoring || to == FailedRetryable
	case FilesRestoring:
		return to == Verified || to == FailedRetryable
	case Verified:
		return to == Completed || to == FailedRetryable
	case FailedRetryable:
		return to == Planned || to == SafetyCaptured || to == ThreadForked || to == FilesRestoring || to == Verified || to == Completed || to == FailedTerminal
	}
	return false
}
