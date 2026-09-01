package types

// Status is a task's board column. A typo ("baklog", "in-progress") is invisible to JSON,
// SQLite, the WebSocket layer and the board until a card silently fails to appear, so it is
// a defined type and the string values are the wire format - they must not change.
type Status string

// The board's four columns, in board order, with their wire values.
const (
	StatusBacklog    Status = "backlog"
	StatusInProgress Status = "in_progress"
	StatusReview     Status = "review"
	StatusDone       Status = "done"
)

// ParseStatus maps a client-supplied column name onto a Status.
//
// This is the only place an unvalidated column name enters the program; past it a Status
// names a real column. Otherwise a client could write "parking_lot" into the row, leaving a
// task that exists in the database on no column - unshowable, and so unstuckable.
func ParseStatus(s string) (Status, bool) {
	switch Status(s) {
	case StatusBacklog, StatusInProgress, StatusReview, StatusDone:
		return Status(s), true
	default:
		return "", false
	}
}
