package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type GitHubImportRequest struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type GitHubIssueResponse struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (s *Server) handleGitHubImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GitHubImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	owner, repo, issueNum, err := parseGitHubIssueURL(req.URL)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid github issue url: %v", err), http.StatusBadRequest)
		return
	}

	token := req.Token
	if token == "" {
		token = r.Header.Get("X-GitHub-Token")
	}

	formatted, err := s.fetchGitHubIssue(owner, repo, issueNum, token)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "404") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, fmt.Sprintf("github api error: %v", err), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(formatted))
}

func (s *Server) fetchGitHubIssue(owner, repo, issueNum, token string) (string, error) {
	return s.fetchGitHubIssueFromBase(s.githubBaseURL, owner, repo, issueNum, token)
}

func (s *Server) fetchGitHubIssueFromBase(baseURL, owner, repo, issueNum, token string) (string, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/%s/issues/%s", baseURL, owner, repo, issueNum)
	client := &http.Client{}
	httpReq, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("User-Agent", "kanban-app")
	if token != "" {
		httpReq.Header.Set("Authorization", "token "+token)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned status %d: %s", resp.StatusCode, resp.Status)
	}

	var issue GitHubIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return "", fmt.Errorf("failed to decode github response: %w", err)
	}

	return fmt.Sprintf("# %s\n\n%s", issue.Title, issue.Body), nil
}

func parseGitHubIssueURL(url string) (owner, repo, issueNum string, err error) {
	url = strings.TrimRight(url, "/")
	parts := strings.Split(url, "/")
	// Expected: https: / / github.com / owner / repo / issues / number
	if len(parts) < 7 || parts[2] != "github.com" || parts[5] != "issues" {
		return "", "", "", fmt.Errorf("url must be in format https://github.com/owner/repo/issues/number")
	}
	return parts[3], parts[4], parts[6], nil
}
