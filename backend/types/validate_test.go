package types

import "testing"

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "main", false},
		{"valid with slash", "task/frost-amber-quartz", false},
		{"valid with numbers", "feature-123", false},
		{"valid thread branch", "thread/voice-ash", false},
		{"valid starts with digit", "123-fix", false},
		{"invalid starts with hyphen", "-main", true},
		{"invalid starts with hyphen and flag", "-f", true},
		{"invalid starts with double hyphen", "--dry-run", true},
		{"invalid empty", "", true},
		{"invalid git injection attempt", "-C/tmp/evil", true},
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
