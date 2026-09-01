package store

import (
	"kanban/backend/types"
	"testing"
	"time"
)

func TestCreateAndGetProject(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "My Project", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.ID != "proj-1" || got.Name != "My Project" || got.Path != "/tmp/repo" {
		t.Fatalf("unexpected project: %+v", got)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetProject("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestListProjects(t *testing.T) {
	s := testStore(t)
	// Create projects in non-alphabetical order
	for _, name := range []string{"Charlie", "Alpha", "Bravo"} {
		proj := newProject(name, name, "/tmp/"+name)
		if err := s.CreateProject(proj); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}

	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	// Should be sorted by creation time (insertion order)
	names := []string{projects[0].Name, projects[1].Name, projects[2].Name}
	expected := []string{"Charlie", "Alpha", "Bravo"}
	if names[0] != expected[0] || names[1] != expected[1] || names[2] != expected[2] {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestUpdateProject(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "Old Name", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	updated, err := s.UpdateProject("proj-1", "New Name")
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if updated.Name != "New Name" {
		t.Fatalf("unexpected name: %s", updated.Name)
	}
}

func TestGetAllProjectCounts(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create tasks in different statuses
	backlog1 := newTask("b1", "proj-1")
	backlog1.Status = types.StatusBacklog
	backlog2 := newTask("b2", "proj-1")
	backlog2.Status = types.StatusBacklog
	inProgress := newTask("ip1", "proj-1")
	inProgress.Status = types.StatusInProgress
	review := newTask("r1", "proj-1")
	review.Status = types.StatusReview
	done := newTask("d1", "proj-1")
	done.Status = types.StatusDone

	for _, task := range []*types.Task{backlog1, backlog2, inProgress, review, done} {
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	counts := s.GetAllProjectCounts()
	c, ok := counts["proj-1"]
	if !ok {
		t.Fatal("expected counts for proj-1")
	}
	if c.Backlog != 2 {
		t.Fatalf("expected 2 backlog, got %d", c.Backlog)
	}
	if c.InProgress != 1 {
		t.Fatalf("expected 1 in_progress, got %d", c.InProgress)
	}
	if c.Review != 1 {
		t.Fatalf("expected 1 review, got %d", c.Review)
	}
	// Done tasks should NOT be counted
}

func TestGetAllProjectCountsMultipleProjects(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"proj-1", "proj-2"} {
		proj := newProject(id, id, "/tmp/"+id)
		if err := s.CreateProject(proj); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}

	// proj-1: 1 backlog
	if err := s.CreateTask(newTask("t1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// proj-2: 1 in_progress
	ip := newTask("t2", "proj-2")
	ip.Status = types.StatusInProgress
	if err := s.CreateTask(ip); err != nil {
		t.Fatalf("create task: %v", err)
	}

	counts := s.GetAllProjectCounts()
	if counts["proj-1"].Backlog != 1 {
		t.Fatalf("proj-1 backlog: %d", counts["proj-1"].Backlog)
	}
	if counts["proj-2"].InProgress != 1 {
		t.Fatalf("proj-2 in_progress: %d", counts["proj-2"].InProgress)
	}
}

func TestProjectUniquePath(t *testing.T) {
	s := testStore(t)
	proj1 := newProject("proj-1", "test", "/tmp/same-path")
	if err := s.CreateProject(proj1); err != nil {
		t.Fatalf("create project 1: %v", err)
	}

	proj2 := newProject("proj-2", "test2", "/tmp/same-path")
	err := s.CreateProject(proj2)
	if err == nil {
		t.Fatal("expected error for duplicate project path")
	}
}

func TestProjectCreatedAtUpdatedAt(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be set")
	}
}

func TestDeleteProject(t *testing.T) {
	s := testStore(t)

	// Create two projects
	proj1 := newProject("proj-1", "Project One", "/tmp/repo1")
	if err := s.CreateProject(proj1); err != nil {
		t.Fatalf("create project 1: %v", err)
	}
	proj2 := newProject("proj-2", "Project Two", "/tmp/repo2")
	if err := s.CreateProject(proj2); err != nil {
		t.Fatalf("create project 2: %v", err)
	}

	// Create tasks for both projects
	task1a := newTask("task-1a", "proj-1")
	task1b := newTask("task-1b", "proj-1")
	task2a := newTask("task-2a", "proj-2")
	for _, task := range []*types.Task{task1a, task1b, task2a} {
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	// Create messages for proj-1 tasks
	for _, taskID := range []string{"task-1a", "task-1b"} {
		msg := newMessage("msg-"+taskID, taskID, "assistant", "hello")
		msg.ModelName = "test-model"
		msg.ProviderName = "test-provider"
		msg.PromptTokens = 10
		msg.PredictedTokens = 5
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}
	// Create a message for proj-2 task (should survive)
	msg2 := newMessage("msg-task-2a", "task-2a", "assistant", "world")
	msg2.ModelName = "test-model"
	msg2.ProviderName = "test-provider"
	msg2.PromptTokens = 20
	if err := s.CreateTaskMessage(msg2); err != nil {
		t.Fatalf("create message for proj-2: %v", err)
	}

	// Create threads for proj-1 tasks
	for _, taskID := range []string{"task-1a", "task-1b"} {
		th := newThread("thread-"+taskID, taskID, "Main")
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}

	// --- Delete proj-1 ---
	if err := s.DeleteProject("proj-1"); err != nil {
		t.Fatalf("delete project with data: %v", err)
	}

	// Verify proj-1 is gone
	_, err := s.GetProject("proj-1")
	if err == nil {
		t.Fatal("expected error for deleted project")
	}

	// Verify all proj-1 tasks are gone
	tasks, err := s.GetTasksByProject("proj-1")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks for proj-1, got %d", len(tasks))
	}

	// Verify messages for proj-1 tasks are gone
	messages1a, err := s.GetTaskMessages("task-1a", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages1a) != 0 {
		t.Fatalf("expected 0 messages for task-1a, got %d", len(messages1a))
	}
	messages1b, err := s.GetTaskMessages("task-1b", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages1b) != 0 {
		t.Fatalf("expected 0 messages for task-1b, got %d", len(messages1b))
	}

	// Verify threads for proj-1 tasks are gone
	threads1a, err := s.GetThreadsByTask("task-1a")
	if err != nil {
		t.Fatalf("get threads: %v", err)
	}
	if len(threads1a) != 0 {
		t.Fatalf("expected 0 threads for task-1a, got %d", len(threads1a))
	}

	// Verify token stats are gone
	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalPromptTokens != 0 || stats.TotalGeneratedTokens != 0 {
		t.Fatalf("expected zero stats for proj-1, got prompt=%d generated=%d", stats.TotalPromptTokens, stats.TotalGeneratedTokens)
	}

	// --- Verify proj-2 is untouched ---
	_, err = s.GetProject("proj-2")
	if err != nil {
		t.Fatalf("proj-2 should still exist: %v", err)
	}
	tasks2, err := s.GetTasksByProject("proj-2")
	if err != nil {
		t.Fatalf("get tasks for proj-2: %v", err)
	}
	if len(tasks2) != 1 {
		t.Fatalf("expected 1 task for proj-2, got %d", len(tasks2))
	}
	messages2, err := s.GetTaskMessages("task-2a", "")
	if err != nil {
		t.Fatalf("get messages for proj-2: %v", err)
	}
	if len(messages2) != 1 {
		t.Fatalf("expected 1 message for proj-2, got %d", len(messages2))
	}
}

func TestProjectSandboxEnabled(t *testing.T) {
	s := testStore(t)

	// Create project with sandbox disabled (default)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	got, _ := s.GetProject("proj-1")
	if got.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be false by default")
	}

	// Enable sandbox
	if err := s.UpdateProjectSandboxEnabled("proj-1", true); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	got, _ = s.GetProject("proj-1")
	if !got.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be true after update")
	}

	// Disable sandbox
	if err := s.UpdateProjectSandboxEnabled("proj-1", false); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	got, _ = s.GetProject("proj-1")
	if got.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be false after disable")
	}

	// Create project with sandbox enabled
	proj2 := newProject("proj-2", "sandboxed", "/tmp/repo2")
	proj2.SandboxEnabled = true
	if err := s.CreateProject(proj2); err != nil {
		t.Fatalf("create sandboxed project: %v", err)
	}
	got2, _ := s.GetProject("proj-2")
	if !got2.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be true for newly created project")
	}

	// Verify ListProjects includes sandbox_enabled
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}

func TestProjectSandboxNetworkIsolation(t *testing.T) {
	s := testStore(t)

	// Create project with network isolation disabled (default)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	got, _ := s.GetProject("proj-1")
	if got.SandboxNetworkIsolation {
		t.Fatal("expected sandbox_network_isolation to be false by default")
	}

	// Enable network isolation
	if err := s.UpdateProjectSandboxNetworkIsolation("proj-1", true); err != nil {
		t.Fatalf("update network isolation: %v", err)
	}
	got, _ = s.GetProject("proj-1")
	if !got.SandboxNetworkIsolation {
		t.Fatal("expected sandbox_network_isolation to be true after update")
	}

	// Disable network isolation
	if err := s.UpdateProjectSandboxNetworkIsolation("proj-1", false); err != nil {
		t.Fatalf("update network isolation: %v", err)
	}
	got, _ = s.GetProject("proj-1")
	if got.SandboxNetworkIsolation {
		t.Fatal("expected sandbox_network_isolation to be false after disable")
	}

	// Create project with both sandbox and network isolation enabled
	proj2 := newProject("proj-2", "isolated", "/tmp/repo2")
	proj2.SandboxEnabled = true
	proj2.SandboxNetworkIsolation = true
	if err := s.CreateProject(proj2); err != nil {
		t.Fatalf("create isolated project: %v", err)
	}
	got2, _ := s.GetProject("proj-2")
	if !got2.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be true")
	}
	if !got2.SandboxNetworkIsolation {
		t.Fatal("expected sandbox_network_isolation to be true")
	}

	// Verify ListProjects includes sandbox_network_isolation
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	for _, p := range projects {
		if p.ID == "proj-2" && !p.SandboxNetworkIsolation {
			t.Fatal("expected proj-2 to have network isolation in list")
		}
	}
}

func TestProjectSandboxImage(t *testing.T) {
	s := testStore(t)

	// Create project with custom sandbox image
	proj := newProject("proj-image", "image-test", "/tmp/repo-image")
	proj.SandboxEnabled = true
	proj.SandboxImage = "docker.io/library/ubuntu:22.04"
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Verify it was saved
	got, err := s.GetProject("proj-image")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.SandboxImage != "docker.io/library/ubuntu:22.04" {
		t.Fatalf("expected ubuntu:22.04, got %q", got.SandboxImage)
	}

	// Update the sandbox image
	if err := s.UpdateProjectSandboxImage("proj-image", "docker.io/library/alpine:3.19"); err != nil {
		t.Fatalf("update sandbox image: %v", err)
	}
	got, _ = s.GetProject("proj-image")
	if got.SandboxImage != "docker.io/library/alpine:3.19" {
		t.Fatalf("expected alpine:3.19, got %q", got.SandboxImage)
	}

	// Clear the sandbox image
	if err := s.UpdateProjectSandboxImage("proj-image", ""); err != nil {
		t.Fatalf("clear sandbox image: %v", err)
	}
	got, _ = s.GetProject("proj-image")
	if got.SandboxImage != "" {
		t.Fatalf("expected empty sandbox image, got %q", got.SandboxImage)
	}

	// Verify ListProjects includes sandbox_image
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-image" {
			found = true
			if p.SandboxImage != "" {
				t.Fatalf("expected empty sandbox image in list, got %q", p.SandboxImage)
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-image in list")
	}
}

func TestProjectSandboxDockerfilePath(t *testing.T) {
	s := testStore(t)

	// Create project with Dockerfile path
	proj := newProject("proj-df", "dockerfile-test", "/tmp/repo-df")
	proj.SandboxEnabled = true
	proj.SandboxDockerfilePath = "/tmp/repo-df/Dockerfile"
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Verify it was saved
	got, err := s.GetProject("proj-df")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.SandboxDockerfilePath != "/tmp/repo-df/Dockerfile" {
		t.Fatalf("expected /tmp/repo-df/Dockerfile, got %q", got.SandboxDockerfilePath)
	}

	// Update the Dockerfile path
	if err := s.UpdateProjectSandboxDockerfilePath("proj-df", "/tmp/repo-df/Dockerfile.prod"); err != nil {
		t.Fatalf("update dockerfile path: %v", err)
	}
	got, _ = s.GetProject("proj-df")
	if got.SandboxDockerfilePath != "/tmp/repo-df/Dockerfile.prod" {
		t.Fatalf("expected Dockerfile.prod, got %q", got.SandboxDockerfilePath)
	}

	// Clear the Dockerfile path
	if err := s.UpdateProjectSandboxDockerfilePath("proj-df", ""); err != nil {
		t.Fatalf("clear dockerfile path: %v", err)
	}
	got, _ = s.GetProject("proj-df")
	if got.SandboxDockerfilePath != "" {
		t.Fatalf("expected empty dockerfile path, got %q", got.SandboxDockerfilePath)
	}

	// Verify ListProjects includes sandbox_dockerfile_path
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-df" {
			found = true
			if p.SandboxDockerfilePath != "" {
				t.Fatalf("expected empty dockerfile path in list, got %q", p.SandboxDockerfilePath)
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-df in list")
	}
}

func TestProjectSandboxMemoryLimit(t *testing.T) {
	s := testStore(t)

	// Create project with default memory limit
	proj := &types.Project{
		ID:        "proj-mem",
		Name:      "Memory Limit Project",
		Path:      "/tmp/test-mem-repo",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-mem")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.SandboxMemoryLimit != 1024 {
		t.Fatalf("expected default memory limit 1024, got %d", got.SandboxMemoryLimit)
	}

	// Update to 2GB
	if err := s.UpdateProjectSandboxMemoryLimit("proj-mem", 2048); err != nil {
		t.Fatalf("update memory limit: %v", err)
	}
	got, err = s.GetProject("proj-mem")
	if err != nil {
		t.Fatalf("get project after update: %v", err)
	}
	if got.SandboxMemoryLimit != 2048 {
		t.Fatalf("expected memory limit 2048, got %d", got.SandboxMemoryLimit)
	}

	// Update to 0 (no limit)
	if err := s.UpdateProjectSandboxMemoryLimit("proj-mem", 0); err != nil {
		t.Fatalf("update memory limit to 0: %v", err)
	}
	got, err = s.GetProject("proj-mem")
	if err != nil {
		t.Fatalf("get project after zero update: %v", err)
	}
	if got.SandboxMemoryLimit != 0 {
		t.Fatalf("expected memory limit 0, got %d", got.SandboxMemoryLimit)
	}

	// Verify in list
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-mem" {
			found = true
			if p.SandboxMemoryLimit != 0 {
				t.Fatalf("expected memory limit 0 in list, got %d", p.SandboxMemoryLimit)
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-mem in list")
	}
}

func TestProjectSandboxForwardPorts(t *testing.T) {
	s := testStore(t)

	// Create project with forward ports
	proj := newProject("proj-ports", "port-test", "/tmp/repo-ports")
	proj.ForwardPorts = []types.PortForward{
		{ContainerPort: 80},
		{ContainerPort: 3000},
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Verify it was saved
	got, err := s.GetProject("proj-ports")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(got.ForwardPorts) != 2 {
		t.Fatalf("expected 2 forward ports, got %d", len(got.ForwardPorts))
	}
	if got.ForwardPorts[0].ContainerPort != 80 {
		t.Fatalf("expected container port 80, got %d", got.ForwardPorts[0].ContainerPort)
	}
	if got.ForwardPorts[1].ContainerPort != 3000 {
		t.Fatalf("expected container port 3000, got %d", got.ForwardPorts[1].ContainerPort)
	}

	// Update forward ports
	newPorts := []types.PortForward{
		{ContainerPort: 8080},
	}
	if err := s.UpdateProjectSandboxForwardPorts("proj-ports", newPorts); err != nil {
		t.Fatalf("update forward ports: %v", err)
	}
	got, err = s.GetProject("proj-ports")
	if err != nil {
		t.Fatalf("get project after update: %v", err)
	}
	if len(got.ForwardPorts) != 1 {
		t.Fatalf("expected 1 forward port, got %d", len(got.ForwardPorts))
	}
	if got.ForwardPorts[0].ContainerPort != 8080 {
		t.Fatalf("expected container port 8080, got %d", got.ForwardPorts[0].ContainerPort)
	}

	// Clear forward ports
	if err := s.UpdateProjectSandboxForwardPorts("proj-ports", nil); err != nil {
		t.Fatalf("clear forward ports: %v", err)
	}
	got, err = s.GetProject("proj-ports")
	if err != nil {
		t.Fatalf("get project after clear: %v", err)
	}
	if len(got.ForwardPorts) != 0 {
		t.Fatalf("expected 0 forward ports, got %d", len(got.ForwardPorts))
	}

	// Verify in list
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-ports" {
			found = true
			if len(p.ForwardPorts) != 0 {
				t.Fatalf("expected 0 forward ports in list, got %d", len(p.ForwardPorts))
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-ports in list")
	}
}

func TestProjectSandboxMounts(t *testing.T) {
	s := testStore(t)

	// Create project with sandbox mounts
	proj := newProject("proj-mounts", "mount-test", "/tmp/repo-mounts")
	proj.SandboxMounts = []types.SandboxMount{
		{HostPath: "/data/cache", ContainerPath: "/mnt/cache", Mode: "rw"},
		{HostPath: "/etc/config", ContainerPath: "/etc/config", Mode: "ro"},
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Verify it was saved
	got, err := s.GetProject("proj-mounts")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(got.SandboxMounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(got.SandboxMounts))
	}
	if got.SandboxMounts[0].HostPath != "/data/cache" {
		t.Fatalf("expected host path /data/cache, got %s", got.SandboxMounts[0].HostPath)
	}
	if got.SandboxMounts[0].ContainerPath != "/mnt/cache" {
		t.Fatalf("expected container path /mnt/cache, got %s", got.SandboxMounts[0].ContainerPath)
	}
	if got.SandboxMounts[0].Mode != "rw" {
		t.Fatalf("expected mode rw, got %s", got.SandboxMounts[0].Mode)
	}
	if got.SandboxMounts[1].Mode != "ro" {
		t.Fatalf("expected mode ro, got %s", got.SandboxMounts[1].Mode)
	}

	// Update mounts
	newMounts := []types.SandboxMount{
		{HostPath: "/logs", ContainerPath: "/var/log/app", Mode: "rw"},
	}
	if err := s.UpdateProjectSandboxMounts("proj-mounts", newMounts); err != nil {
		t.Fatalf("update mounts: %v", err)
	}
	got, err = s.GetProject("proj-mounts")
	if err != nil {
		t.Fatalf("get project after update: %v", err)
	}
	if len(got.SandboxMounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got.SandboxMounts))
	}
	if got.SandboxMounts[0].HostPath != "/logs" {
		t.Fatalf("expected host path /logs, got %s", got.SandboxMounts[0].HostPath)
	}

	// Clear mounts
	if err := s.UpdateProjectSandboxMounts("proj-mounts", nil); err != nil {
		t.Fatalf("clear mounts: %v", err)
	}
	got, err = s.GetProject("proj-mounts")
	if err != nil {
		t.Fatalf("get project after clear: %v", err)
	}
	if len(got.SandboxMounts) != 0 {
		t.Fatalf("expected 0 mounts, got %d", len(got.SandboxMounts))
	}

	// Verify in list
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-mounts" {
			found = true
			if len(p.SandboxMounts) != 0 {
				t.Fatalf("expected 0 mounts in list, got %d", len(p.SandboxMounts))
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-mounts in list")
	}
}

func TestProjectSandboxEnvs(t *testing.T) {
	s := testStore(t)

	// Create project with sandbox envs
	proj := newProject("proj-envs", "env-test", "/tmp/repo-envs")
	proj.SandboxEnvs = []types.SandboxEnv{
		{Key: "DATABASE_URL", Value: "postgres://localhost/db"},
		{Key: "API_KEY", Value: "secret123"},
		{Key: "EMPTY_VAR", Value: ""},
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Verify it was saved
	got, err := s.GetProject("proj-envs")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(got.SandboxEnvs) != 3 {
		t.Fatalf("expected 3 envs, got %d", len(got.SandboxEnvs))
	}
	if got.SandboxEnvs[0].Key != "DATABASE_URL" {
		t.Fatalf("expected key DATABASE_URL, got %s", got.SandboxEnvs[0].Key)
	}
	if got.SandboxEnvs[0].Value != "postgres://localhost/db" {
		t.Fatalf("expected value postgres://localhost/db, got %s", got.SandboxEnvs[0].Value)
	}
	if got.SandboxEnvs[2].Key != "EMPTY_VAR" {
		t.Fatalf("expected key EMPTY_VAR, got %s", got.SandboxEnvs[2].Key)
	}
	if got.SandboxEnvs[2].Value != "" {
		t.Fatalf("expected empty value, got %s", got.SandboxEnvs[2].Value)
	}

	// Update envs
	newEnvs := []types.SandboxEnv{
		{Key: "NEW_VAR", Value: "new_value"},
	}
	if err := s.UpdateProjectSandboxEnvs("proj-envs", newEnvs); err != nil {
		t.Fatalf("update envs: %v", err)
	}
	got, err = s.GetProject("proj-envs")
	if err != nil {
		t.Fatalf("get project after update: %v", err)
	}
	if len(got.SandboxEnvs) != 1 {
		t.Fatalf("expected 1 env, got %d", len(got.SandboxEnvs))
	}
	if got.SandboxEnvs[0].Key != "NEW_VAR" {
		t.Fatalf("expected key NEW_VAR, got %s", got.SandboxEnvs[0].Key)
	}

	// Clear envs
	if err := s.UpdateProjectSandboxEnvs("proj-envs", nil); err != nil {
		t.Fatalf("clear envs: %v", err)
	}
	got, err = s.GetProject("proj-envs")
	if err != nil {
		t.Fatalf("get project after clear: %v", err)
	}
	if len(got.SandboxEnvs) != 0 {
		t.Fatalf("expected 0 envs, got %d", len(got.SandboxEnvs))
	}

	// Verify in list
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-envs" {
			found = true
			if len(p.SandboxEnvs) != 0 {
				t.Fatalf("expected 0 envs in list, got %d", len(p.SandboxEnvs))
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-envs in list")
	}
}

func TestProjectSandboxCapabilities(t *testing.T) {
	s := testStore(t)

	// Create project with sandbox capabilities
	proj := newProject("proj-caps", "caps-test", "/tmp/repo-caps")
	proj.SandboxCapabilities = []string{"CAP_CHOWN", "CAP_SETUID", "CAP_NET_RAW"}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Verify it was saved
	got, err := s.GetProject("proj-caps")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(got.SandboxCapabilities) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(got.SandboxCapabilities))
	}
	if got.SandboxCapabilities[0] != "CAP_CHOWN" {
		t.Fatalf("expected CAP_CHOWN, got %s", got.SandboxCapabilities[0])
	}
	if got.SandboxCapabilities[2] != "CAP_NET_RAW" {
		t.Fatalf("expected CAP_NET_RAW, got %s", got.SandboxCapabilities[2])
	}

	// Update capabilities
	newCaps := []string{"CAP_SYS_PTRACE"}
	if err := s.UpdateProjectSandboxCapabilities("proj-caps", newCaps); err != nil {
		t.Fatalf("update capabilities: %v", err)
	}
	got, err = s.GetProject("proj-caps")
	if err != nil {
		t.Fatalf("get project after update: %v", err)
	}
	if len(got.SandboxCapabilities) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(got.SandboxCapabilities))
	}
	if got.SandboxCapabilities[0] != "CAP_SYS_PTRACE" {
		t.Fatalf("expected CAP_SYS_PTRACE, got %s", got.SandboxCapabilities[0])
	}

	// Clear capabilities
	if err := s.UpdateProjectSandboxCapabilities("proj-caps", nil); err != nil {
		t.Fatalf("clear capabilities: %v", err)
	}
	got, err = s.GetProject("proj-caps")
	if err != nil {
		t.Fatalf("get project after clear: %v", err)
	}
	if len(got.SandboxCapabilities) != 0 {
		t.Fatalf("expected 0 capabilities, got %d", len(got.SandboxCapabilities))
	}

	// Verify in list
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == "proj-caps" {
			found = true
			if len(p.SandboxCapabilities) != 0 {
				t.Fatalf("expected 0 capabilities in list, got %d", len(p.SandboxCapabilities))
			}
		}
	}
	if !found {
		t.Fatal("expected to find proj-caps in list")
	}
}
