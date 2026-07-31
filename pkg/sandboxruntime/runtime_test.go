package sandboxruntime

import (
	"testing"
)

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
