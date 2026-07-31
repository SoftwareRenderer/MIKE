package sandboxruntime

import (
	"strings"
	"testing"
)

func TestValidateProjectName(t *testing.T) {
	tests := []struct {
		name    string
		project string
		wantErr bool
		errMsg  string
	}{
		{"simple name", "my-project", false, ""},
		{"with spaces", "My Project Name", false, ""},
		{"with underscores", "my_project_v2", false, ""},
		{"with dots", "my.project.name", false, ""},
		{"with slashes", "my/project", false, ""},
		{"mixed valid chars", "My.Project_Name-2.0", false, ""},
		{"single char", "a", false, ""},
		{"numeric", "project123", false, ""},
		{"empty string", "", true, "must not be empty"},
		{"leading dash", "-my-project", true, "must not start with a dash"},
		{"double dash", "--verbose", true, "must not start with a dash"},
		{"control char newline", "my\nproject", true, "control characters"},
		{"control char tab", "my\tproject", true, "control characters"},
		{"control char null", "my\x00project", true, "control characters"},
		{"control char del", "my\x7fproject", true, "control characters"},
		{"shell injection", "my; rm -rf /", true, "invalid character"},
		{"shell injection pipe", "my | cat", true, "invalid character"},
		{"shell injection backtick", "my`whoami`project", true, "invalid character"},
		{"shell injection dollar", "my$(rm /)project", true, "invalid character"},
		{"parentheses", "my(project)", true, "invalid character"},
		{"angle brackets", "my<project>", true, "invalid character"},
		{"ampersand", "my&project", true, "invalid character"},
		{"exclamation", "my!project", true, "invalid character"},
		{"question mark", "my?project", true, "invalid character"},
		{"asterisk", "my*project", true, "invalid character"},
		{"hash", "my#project", true, "invalid character"},
		{"percent", "my%project", true, "invalid character"},
		{"caret", "my^project", true, "invalid character"},
		{"backslash", "my\\project", true, "invalid character"},
		{"quotes", `my"project"`, true, "invalid character"},
		{"single quote", "my'project", true, "invalid character"},
		{"equals", "my=project", true, "invalid character"},
		{"plus", "my+project", true, "invalid character"},
		{"comma", "my,project", true, "invalid character"},
		{"colon", "my:project", true, "invalid character"},
		{"semicolon", "my;project", true, "invalid character"},
		{"at sign", "my@project", true, "invalid character"},
		{"very long name", "a" + strings.Repeat("b", 256), true, "exceed 256"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectName(tt.project)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProjectName(%q) error = %v, wantErr %v", tt.project, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateProjectName(%q) error = %q, want error containing %q", tt.project, err.Error(), tt.errMsg)
			}
		})
	}
}

func TestValidateImageRef(t *testing.T) {
	r := NewRuntime()
	tests := []struct {
		name    string
		image   string
		wantErr bool
	}{
		{"valid ubuntu", "docker.io/library/ubuntu:latest", false},
		{"valid golang", "golang:1.21", false},
		{"valid with digest", "ubuntu@sha256:abc123", false},
		{"valid localhost", "localhost:5000/myimage:latest", false},
		{"valid kanban prefix", "kanban-ext-gopls:1.0.0", false},
		{"valid project image", "sandbox-my-project", false},
		{"valid simple", "alpine", false},
		{"dash prefix", "-v", true},
		{"dash prefix long", "--format=json", true},
		{"dash prefix space", "-t something", true},
		{"empty string", "", true},
		{"space injection", "ubuntu:latest -v", true},
		{"shell injection", "$(rm -rf /)", true},
		{"semicolon injection", "ubuntu; rm -rf /", true},
		{"pipe injection", "ubuntu | cat", true},
		{"backtick injection", "ubun`whoami`t", true},
		{"newline injection", "ubuntu\nrm", true},
		{"tab character", "ubun\ttu", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.ValidateImageRef(tt.image)
			if (err != nil) != tt.wantErr {
				t.Errorf("r.ValidateImageRef(%q) error = %v, wantErr %v", tt.image, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDockerfilePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid absolute", "/home/user/Dockerfile", false},
		{"valid relative", "Dockerfile", false},
		{"valid subpath", "path/to/Dockerfile", false},
		{"dash prefix", "-f", true},
		{"dash prefix path", "--file=/tmp/Dockerfile", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDockerfilePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDockerfilePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}
