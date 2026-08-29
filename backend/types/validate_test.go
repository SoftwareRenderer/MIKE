package types

import "testing"

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid branch names
		{"valid simple", "main", false},
		{"valid with slash", "task/frost-amber-quartz", false},
		{"valid with numbers", "feature-123", false},
		{"valid thread branch", "thread/voice-ash", false},
		{"valid starts with digit", "123-fix", false},
		{"valid with plus", "feature+bugfix", false},
		{"valid with underscore", "feature_branch", false},
		{"valid with dot", "release.1.2", false},
		// Invalid: leading hyphen (git argument injection)
		{"invalid starts with hyphen", "-main", true},
		{"invalid starts with hyphen and flag", "-f", true},
		{"invalid starts with double hyphen", "--dry-run", true},
		{"invalid empty", "", true},
		{"invalid git injection attempt", "-C/tmp/evil", true},
		// Invalid: disallowed characters (git argument injection / invalid ref)
		{"invalid tilde", "feat~1", true},
		{"invalid caret", "feat^head", true},
		{"invalid colon", "feat:branch", true},
		{"invalid question", "feat?x", true},
		{"invalid asterisk", "feat*branch", true},
		{"invalid bracket", "feat[x]", true},
		{"invalid backslash", "feat\\branch", true},
		{"invalid space", "feat branch", true},
		{"invalid double dot", "feat..branch", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBranchName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranchName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateGitRef(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid refs
		{"HEAD", "HEAD", false},
		{"branch", "main", false},
		{"task branch", "task/frost-amber-quartz", false},
		{"thread branch", "thread/voice-ash", false},
		{"tag with dots", "release.1.2", false},
		{"hex sha", "5371a137e968ed74535187eae88a832668af49e7", false},
		{"short sha", "5371a13", false},
		{"starts with digit", "123-fix", false},
		// Invalid: argument injection into git
		{"empty", "", true},
		{"single dash", "-", true},
		{"git option output", "--output=/tmp/pwn", true},
		{"git option exec path", "--exec-path=/tmp/x", true},
		{"git option C", "-C/tmp/evil", true},
		{"short flag", "-f", true},
		// Invalid: revision syntax
		{"tilde", "HEAD~1", true},
		{"caret", "HEAD^", true},
		{"colon", "HEAD:file.txt", true},
		{"range dots", "HEAD..HEAD", true},
		{"glob", "feature*", true},
		{"question mark", "feature?", true},
		{"brackets", "feature[x]", true},
		{"backslash", "feat\\branch", true},
		{"braces", "HEAD@{0}", true},
		// Invalid: shell/quoting characters
		{"space", "feat branch", true},
		{"tab", "feat\tbranch", true},
		{"newline", "feat\nbranch", true},
		{"nul", "feat\x00branch", true},
		{"double quote", `feat"x`, true},
		{"single quote", "feat'x", true},
		{"dollar", "feat$(id)", true},
		{"backtick", "feat`id`", true},
		{"semicolon", "feat;id", true},
		{"plus", "feature+bugfix", true},
		{"at sign", "HEAD@origin", true},
		{"non-ascii", "feat\u00e9", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitRef(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitRef(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateTaskID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid normal ID", "frost-amber-quartz", false},
		{"valid starts with digit", "123-abc-def", false},
		{"valid single word", "ocean", false},
		{"invalid starts with hyphen", "-frost-amber-quartz", true},
		{"invalid just hyphen", "-", true},
		{"invalid empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTaskID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTaskID(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
