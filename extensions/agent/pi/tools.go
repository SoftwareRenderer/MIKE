package pi

import "strings"

// Tool Definition
type toolDef struct {
	piName      string
	kanbanName  string
	displayName string
}

var piTools = []toolDef{
	{"read", "read_file", "Read File"},
	{"write", "write_file", "Write File"},
	{"edit", "edit_file", "Edit File"},
	{"bash", "bash", "Bash"},
	{"request_review", "request_review", "Request Review"},
	{"final_answer", "request_review", "Request Review"},
	{"ask_question", "ask_question", "Ask Question"},
	{"create_task", "create_task", "Create Task"},
}

func mapPiToolName(piName string) string {
	for _, t := range piTools {
		if t.piName == piName {
			return t.kanbanName
		}
	}
	return piName
}

func getToolDisplayName(name string) string {
	for _, t := range piTools {
		if t.kanbanName == name {
			return t.displayName
		}
	}
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
