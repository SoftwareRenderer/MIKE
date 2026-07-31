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
