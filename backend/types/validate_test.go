package types

import (
	"strings"
	"testing"
)

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

func TestNormalizeSandboxHostIP(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty means every interface", "", "", false},
		{"whitespace only", "   ", "", false},
		{"IPv4 wildcard", "0.0.0.0", "", false},
		{"IPv6 wildcard", "::", "", false},
		{"LAN address", "192.168.1.100", "192.168.1.100", false},
		{"loopback passes through", "127.0.0.1", "127.0.0.1", false},
		{"IPv6 loopback passes through", "::1", "::1", false},
		{"global IPv6", "fdd4:9b13:e8c4:62bc::1", "fdd4:9b13:e8c4:62bc::1", false},
		{"IPv4-mapped IPv6 becomes IPv4", "::ffff:10.0.0.5", "10.0.0.5", false},
		{"surrounding space trimmed", " 10.0.0.5 ", "10.0.0.5", false},
		{"hostname rejected", "localhost", "", true},
		{"partial address rejected", "192.168.1", "", true},
		{"compose-style triple rejected", "192.168.1.100:8080", "", true},
		{"CIDR rejected", "10.0.0.0/24", "", true},
		{"interface zone rejected", "fe80::1%enp1s0", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSandboxHostIP(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeSandboxHostIP(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("NormalizeSandboxHostIP(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateSandboxForwardPorts(t *testing.T) {
	tests := []struct {
		name    string
		ports   []PortForward
		wantErr bool
	}{
		{"none configured", nil, false},
		{"container port only", []PortForward{{ContainerPort: 3000}}, false},
		{"with bind address", []PortForward{{ContainerPort: 80, HostIP: "192.168.1.100"}}, false},
		{"wildcard spellings", []PortForward{{ContainerPort: 80, HostIP: "0.0.0.0"}, {ContainerPort: 81, HostIP: "::"}}, false},
		{"loopback is the user's choice", []PortForward{{ContainerPort: 3000, HostIP: "127.0.0.1"}}, false},
		{"port range edges", []PortForward{{ContainerPort: 1}, {ContainerPort: 65535}}, false},
		{"zero container port", []PortForward{{}}, true},
		{"negative container port", []PortForward{{ContainerPort: -1}}, true},
		{"container port above range", []PortForward{{ContainerPort: 65536}}, true},
		{"bind address is not an IP", []PortForward{{ContainerPort: 3000, HostIP: "saturn.lan"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSandboxForwardPorts(tt.ports)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSandboxForwardPorts(%+v) error = %v, wantErr %v", tt.ports, err, tt.wantErr)
			}
		})
	}
}

// The error must name the port the user configured, since the Settings table shows no
// row numbers.
func TestValidateSandboxForwardPortsNamesThePort(t *testing.T) {
	err := ValidateSandboxForwardPorts([]PortForward{{ContainerPort: 3000}, {ContainerPort: 8080, HostIP: "nope"}})
	if err == nil {
		t.Fatal("expected an error for a bind address that is not an IP address")
	}
	if !strings.Contains(err.Error(), "8080") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not name the port and the value", err.Error())
	}
}
