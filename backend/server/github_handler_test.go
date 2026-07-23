package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseGitHubIssueURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		owner   string
		repo    string
		issue   string
		wantErr bool
	}{
		{"valid url", "https://github.com/owner/repo/issues/123", "owner", "repo", "123", false},
		{"valid url with trailing slash", "https://github.com/owner/repo/issues/123/", "owner", "repo", "123", false},
		{"invalid url - not github", "https://gitlab.com/owner/repo/issues/123", "", "", "", true},
		{"invalid url - wrong path", "https://github.com/owner/repo/pulls/123", "", "", "", true},
		{"invalid url - too short", "https://github.com/owner/repo", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, issue, err := parseGitHubIssueURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseGitHubIssueURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if owner != tt.owner || repo != tt.repo || issue != tt.issue {
					t.Errorf("parseGitHubIssueURL() = %v, %v, %v; want %v, %v, %v", owner, repo, issue, tt.owner, tt.repo, tt.issue)
				}
			}
		})
	}
}

func TestFetchGitHubIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/repos/owner/repo/issues/123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"title": "Issue Title", "body": "Issue Body content"}`))
	}))
	defer ts.Close()

	s := &Server{}
	formatted, err := s.fetchGitHubIssueFromBase(ts.URL, "owner", "repo", "123", "secret-token")
	if err != nil {
		t.Fatalf("fetchGitHubIssueFromBase failed: %v", err)
	}

	expected := "# Issue Title\n\nIssue Body content"
	if formatted != expected {
		t.Errorf("expected %q, got %q", expected, formatted)
	}

	// Test unauthorized
	_, err = s.fetchGitHubIssueFromBase(ts.URL, "owner", "repo", "123", "")
	if err == nil {
		t.Error("expected error for unauthorized request, got nil")
	}
}

func TestHandleGitHubImport(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"title": "Issue Title", "body": "Issue Body content"}`))
	}))
	defer ts.Close()

	s := &Server{
		githubBaseURL: ts.URL,
	}

	reqBody, _ := json.Marshal(GitHubImportRequest{
		URL: "https://github.com/owner/repo/issues/123",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/github/import-issue", bytes.NewBuffer(reqBody))
	w := httptest.NewRecorder()

	s.handleGitHubImport(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	expected := "# Issue Title\n\nIssue Body content"
	if w.Body.String() != expected {
		t.Errorf("expected %q, got %q", expected, w.Body.String())
	}
}

