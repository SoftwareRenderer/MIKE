package store

import (
	"mike/backend/types"
	"testing"
	"time"
)

func TestCreateAndGetProject(t *testing.T) {
	s := testStore(t)
	createdAt := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	proj := newProject("proj-1", "My Project", "/tmp/repo")
	proj.CreatedAt = createdAt
	proj.UpdatedAt = createdAt
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
	// The instants are the row's order in ListProjects, so they travel as text and come back as instants.
	if !got.CreatedAt.Equal(createdAt) || !got.UpdatedAt.Equal(createdAt) {
		t.Fatalf("timestamps read back (%v, %v), want both %v", got.CreatedAt, got.UpdatedAt, createdAt)
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

// The board's badges come from one grouped read: counts per project and per column, with Done left out
// because a card that reached Done is no one's badge.
func TestGetAllProjectCounts(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"proj-1", "proj-2"} {
		if err := s.CreateProject(newProject(id, id, "/tmp/"+id)); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	seed := []struct {
		id, project string
		status      types.Status
	}{
		{"b1", "proj-1", types.StatusBacklog},
		{"b2", "proj-1", types.StatusBacklog},
		{"ip1", "proj-1", types.StatusInProgress},
		{"r1", "proj-1", types.StatusReview},
		{"d1", "proj-1", types.StatusDone},
		{"t2", "proj-2", types.StatusInProgress},
	}
	for _, task := range seed {
		tk := newTask(task.id, task.project)
		tk.Status = task.status
		if err := s.CreateTask(tk); err != nil {
			t.Fatalf("create %s: %v", task.id, err)
		}
	}

	counts := s.GetAllProjectCounts()
	for _, want := range []struct {
		project string
		c       types.ProjectCounts
	}{
		{"proj-1", types.ProjectCounts{Backlog: 2, InProgress: 1, Review: 1}},
		{"proj-2", types.ProjectCounts{InProgress: 1}},
	} {
		c, ok := counts[want.project]
		if !ok {
			t.Fatalf("no counts for %s: %v", want.project, counts)
		}
		if c != want.c {
			t.Errorf("%s counts = %+v, want %+v (Done is never counted)", want.project, c, want.c)
		}
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

func TestProjectSandboxColumnsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bare   func(*testing.T, *types.Project)
		seed   func(*types.Project)
		want   func(*testing.T, *types.Project)
		writes []struct {
			apply func(*Store) error
			want  func(*testing.T, *types.Project)
		}
	}{
		{
			name: "sandbox_enabled",
			bare: func(t *testing.T, p *types.Project) {
				if p.SandboxEnabled {
					t.Errorf("sandbox_enabled reads on for a project nobody enabled: %v", p.SandboxEnabled)
				}
			},
			seed: func(p *types.Project) { p.SandboxEnabled = true },
			want: func(t *testing.T, p *types.Project) {
				if !p.SandboxEnabled {
					t.Errorf("sandbox_enabled = false, want true — where a task runs is this one column")
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxEnabled("proj-col", false) },
					want: func(t *testing.T, p *types.Project) {
						if p.SandboxEnabled {
							t.Errorf("sandbox_enabled stayed on through the write that turned it off")
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxEnabled("proj-col", true) },
					want: func(t *testing.T, p *types.Project) {
						if !p.SandboxEnabled {
							t.Errorf("sandbox_enabled stayed off through the write that turned it on")
						}
					},
				},
			},
		},
		{
			name: "sandbox_network_isolation",
			bare: func(t *testing.T, p *types.Project) {
				if p.SandboxNetworkIsolation {
					t.Errorf("sandbox_network_isolation reads on for a project nobody enabled")
				}
			},
			seed: func(p *types.Project) { p.SandboxNetworkIsolation = true },
			want: func(t *testing.T, p *types.Project) {
				if !p.SandboxNetworkIsolation {
					t.Errorf("sandbox_network_isolation = false, want true")
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxNetworkIsolation("proj-col", false) },
					want: func(t *testing.T, p *types.Project) {
						if p.SandboxNetworkIsolation {
							t.Errorf("sandbox_network_isolation stayed on through the write that turned it off")
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxNetworkIsolation("proj-col", true) },
					want: func(t *testing.T, p *types.Project) {
						if !p.SandboxNetworkIsolation {
							t.Errorf("sandbox_network_isolation stayed off through the write that turned it on")
						}
					},
				},
			},
		},
		{
			// The empty string is a reset: the column goes NULL, which means "no project image",
			// not "the image this row held before".
			name: "sandbox_image",
			bare: func(t *testing.T, p *types.Project) {
				wantText(t, "sandbox_image", p.SandboxImage, "")
			},
			seed: func(p *types.Project) { p.SandboxImage = "docker.io/library/ubuntu:22.04" },
			want: func(t *testing.T, p *types.Project) {
				wantText(t, "sandbox_image", p.SandboxImage, "docker.io/library/ubuntu:22.04")
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error {
						return s.UpdateProjectSandboxImage("proj-col", "docker.io/library/alpine:3.19")
					},
					want: func(t *testing.T, p *types.Project) {
						wantText(t, "sandbox_image", p.SandboxImage, "docker.io/library/alpine:3.19")
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxImage("proj-col", "") },
					want: func(t *testing.T, p *types.Project) {
						wantText(t, "sandbox_image", p.SandboxImage, "")
					},
				},
			},
		},
		{
			name: "sandbox_dockerfile_path",
			bare: func(t *testing.T, p *types.Project) {
				wantText(t, "sandbox_dockerfile_path", p.SandboxDockerfilePath, "")
			},
			seed: func(p *types.Project) { p.SandboxDockerfilePath = "/tmp/repo/Dockerfile" },
			want: func(t *testing.T, p *types.Project) {
				wantText(t, "sandbox_dockerfile_path", p.SandboxDockerfilePath, "/tmp/repo/Dockerfile")
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error {
						return s.UpdateProjectSandboxDockerfilePath("proj-col", "/tmp/repo/Dockerfile.prod")
					},
					want: func(t *testing.T, p *types.Project) {
						wantText(t, "sandbox_dockerfile_path", p.SandboxDockerfilePath, "/tmp/repo/Dockerfile.prod")
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxDockerfilePath("proj-col", "") },
					want: func(t *testing.T, p *types.Project) {
						wantText(t, "sandbox_dockerfile_path", p.SandboxDockerfilePath, "")
					},
				},
			},
		},
		{
			// 0 is a decision here, not an absence: it means no cap, so the accessor must not
			// re-fill the default the insert fills.
			name: "sandbox_memory_limit",
			bare: func(t *testing.T, p *types.Project) {
				if p.SandboxMemoryLimit != types.DefaultSandboxMemoryLimitMB {
					t.Errorf("sandbox_memory_limit = %d on a project created without one, want the default %d",
						p.SandboxMemoryLimit, types.DefaultSandboxMemoryLimitMB)
				}
			},
			seed: func(p *types.Project) { p.SandboxMemoryLimit = 2048 },
			want: func(t *testing.T, p *types.Project) {
				if p.SandboxMemoryLimit != 2048 {
					t.Errorf("sandbox_memory_limit = %d, want the 2048 it was created with", p.SandboxMemoryLimit)
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxMemoryLimit("proj-col", 0) },
					want: func(t *testing.T, p *types.Project) {
						if p.SandboxMemoryLimit != 0 {
							t.Errorf("sandbox_memory_limit = %d after the write that lifted the cap, want 0", p.SandboxMemoryLimit)
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxMemoryLimit("proj-col", 512) },
					want: func(t *testing.T, p *types.Project) {
						if p.SandboxMemoryLimit != 512 {
							t.Errorf("sandbox_memory_limit = %d, want 512", p.SandboxMemoryLimit)
						}
					},
				},
			},
		},
		{
			name: "sandbox_forward_ports",
			bare: func(t *testing.T, p *types.Project) {
				if len(p.ForwardPorts) != 0 {
					t.Errorf("ForwardPorts = %+v on a project created without any", p.ForwardPorts)
				}
			},
			seed: func(p *types.Project) {
				p.ForwardPorts = []types.PortForward{{ContainerPort: 80}, {ContainerPort: 3000}}
			},
			want: func(t *testing.T, p *types.Project) {
				if len(p.ForwardPorts) != 2 || p.ForwardPorts[0].ContainerPort != 80 || p.ForwardPorts[1].ContainerPort != 3000 {
					t.Errorf("ForwardPorts = %+v, want 80 and 3000", p.ForwardPorts)
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error {
						return s.UpdateProjectSandboxForwardPorts("proj-col", []types.PortForward{{ContainerPort: 8080}})
					},
					want: func(t *testing.T, p *types.Project) {
						if len(p.ForwardPorts) != 1 || p.ForwardPorts[0].ContainerPort != 8080 {
							t.Errorf("ForwardPorts = %+v, want the single 8080 the write stored", p.ForwardPorts)
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxForwardPorts("proj-col", nil) },
					want: func(t *testing.T, p *types.Project) {
						if len(p.ForwardPorts) != 0 {
							t.Errorf("ForwardPorts = %+v after the clear, want none", p.ForwardPorts)
						}
					},
				},
			},
		},
		{
			name: "sandbox_mounts",
			bare: func(t *testing.T, p *types.Project) {
				if len(p.SandboxMounts) != 0 {
					t.Errorf("SandboxMounts = %+v on a project created without any", p.SandboxMounts)
				}
			},
			seed: func(p *types.Project) {
				p.SandboxMounts = []types.SandboxMount{
					{HostPath: "/data/cache", ContainerPath: "/mnt/cache", Mode: "rw"},
					{HostPath: "/etc/config", ContainerPath: "/etc/config", Mode: "ro"},
				}
			},
			want: func(t *testing.T, p *types.Project) {
				if len(p.SandboxMounts) != 2 {
					t.Fatalf("SandboxMounts = %+v, want the two it was created with", p.SandboxMounts)
				}
				if p.SandboxMounts[0] != (types.SandboxMount{HostPath: "/data/cache", ContainerPath: "/mnt/cache", Mode: "rw"}) {
					t.Errorf("mount 0 = %+v, want /data/cache to /mnt/cache rw", p.SandboxMounts[0])
				}
				if p.SandboxMounts[1] != (types.SandboxMount{HostPath: "/etc/config", ContainerPath: "/etc/config", Mode: "ro"}) {
					t.Errorf("mount 1 = %+v, want the read-only one it was created with", p.SandboxMounts[1])
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error {
						return s.UpdateProjectSandboxMounts("proj-col", []types.SandboxMount{
							{HostPath: "/logs", ContainerPath: "/var/log/app", Mode: "rw"},
						})
					},
					want: func(t *testing.T, p *types.Project) {
						if len(p.SandboxMounts) != 1 || p.SandboxMounts[0].HostPath != "/logs" {
							t.Errorf("SandboxMounts = %+v, want the one /logs row the write stored", p.SandboxMounts)
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxMounts("proj-col", nil) },
					want: func(t *testing.T, p *types.Project) {
						if len(p.SandboxMounts) != 0 {
							t.Errorf("SandboxMounts = %+v after the clear, want none", p.SandboxMounts)
						}
					},
				},
			},
		},
		{
			name: "sandbox_envs",
			bare: func(t *testing.T, p *types.Project) {
				if len(p.SandboxEnvs) != 0 {
					t.Errorf("SandboxEnvs = %+v on a project created without any", p.SandboxEnvs)
				}
			},
			seed: func(p *types.Project) {
				p.SandboxEnvs = []types.SandboxEnv{
					{Key: "DATABASE_URL", Value: "postgres://localhost/db"},
					{Key: "API_KEY", Value: "secret123"},
					{Key: "EMPTY_VAR", Value: ""},
				}
			},
			want: func(t *testing.T, p *types.Project) {
				if len(p.SandboxEnvs) != 3 {
					t.Fatalf("SandboxEnvs = %+v, want the three it was created with", p.SandboxEnvs)
				}
				if p.SandboxEnvs[0] != (types.SandboxEnv{Key: "DATABASE_URL", Value: "postgres://localhost/db"}) {
					t.Errorf("env 0 = %+v, want DATABASE_URL with its value", p.SandboxEnvs[0])
				}
				// An empty value is what the container is told to unset, so it is not the same as no row.
				if p.SandboxEnvs[2] != (types.SandboxEnv{Key: "EMPTY_VAR", Value: ""}) {
					t.Errorf("env 2 = %+v, want EMPTY_VAR with its empty value", p.SandboxEnvs[2])
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error {
						return s.UpdateProjectSandboxEnvs("proj-col", []types.SandboxEnv{{Key: "NEW_VAR", Value: "new_value"}})
					},
					want: func(t *testing.T, p *types.Project) {
						if len(p.SandboxEnvs) != 1 || p.SandboxEnvs[0].Key != "NEW_VAR" {
							t.Errorf("SandboxEnvs = %+v, want the one NEW_VAR row the write stored", p.SandboxEnvs)
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxEnvs("proj-col", nil) },
					want: func(t *testing.T, p *types.Project) {
						if len(p.SandboxEnvs) != 0 {
							t.Errorf("SandboxEnvs = %+v after the clear, want none", p.SandboxEnvs)
						}
					},
				},
			},
		},
		{
			name: "sandbox_capabilities",
			bare: func(t *testing.T, p *types.Project) {
				if len(p.SandboxCapabilities) != 0 {
					t.Errorf("SandboxCapabilities = %v on a project created without any", p.SandboxCapabilities)
				}
			},
			seed: func(p *types.Project) {
				p.SandboxCapabilities = []string{"CAP_CHOWN", "CAP_SETUID", "CAP_NET_RAW"}
			},
			want: func(t *testing.T, p *types.Project) {
				if len(p.SandboxCapabilities) != 3 || p.SandboxCapabilities[0] != "CAP_CHOWN" || p.SandboxCapabilities[2] != "CAP_NET_RAW" {
					t.Errorf("SandboxCapabilities = %v, want the three it was created with, in order", p.SandboxCapabilities)
				}
			},
			writes: []struct {
				apply func(*Store) error
				want  func(*testing.T, *types.Project)
			}{
				{
					apply: func(s *Store) error {
						return s.UpdateProjectSandboxCapabilities("proj-col", []string{"CAP_SYS_PTRACE"})
					},
					want: func(t *testing.T, p *types.Project) {
						if len(p.SandboxCapabilities) != 1 || p.SandboxCapabilities[0] != "CAP_SYS_PTRACE" {
							t.Errorf("SandboxCapabilities = %v, want the one capability the write stored", p.SandboxCapabilities)
						}
					},
				},
				{
					apply: func(s *Store) error { return s.UpdateProjectSandboxCapabilities("proj-col", nil) },
					want: func(t *testing.T, p *types.Project) {
						if len(p.SandboxCapabilities) != 0 {
							t.Errorf("SandboxCapabilities = %v after the clear, want none", p.SandboxCapabilities)
						}
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			if err := s.CreateProject(newProject("proj-bare", "bare", "/tmp/bare")); err != nil {
				t.Fatalf("create the bare project: %v", err)
			}
			if tc.bare != nil {
				checkProjectReads(t, s, "proj-bare", tc.bare)
			}

			p := newProject("proj-col", "col", "/tmp/col")
			if tc.seed != nil {
				tc.seed(p)
			}
			if err := s.CreateProject(p); err != nil {
				t.Fatalf("create the project: %v", err)
			}
			checkProjectReads(t, s, "proj-col", tc.want)

			for i, w := range tc.writes {
				if err := w.apply(s); err != nil {
					t.Fatalf("write %d: %v", i, err)
				}
				checkProjectReads(t, s, "proj-col", w.want)
			}
		})
	}
}

// checkProjectReads holds both reads of one row to the same reading: GetProject for the settings dialog,
// ListProjects for the sidebar, which builds its own rows from the same column list.
func checkProjectReads(t *testing.T, s *Store, id string, want func(*testing.T, *types.Project)) {
	t.Helper()
	got, err := s.GetProject(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	want(t, got)

	listed, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	var row *types.Project
	for i := range listed {
		if listed[i].ID == id {
			row = &listed[i]
		}
	}
	if row == nil {
		t.Fatalf("%s is not in ListProjects", id)
	}
	want(t, row)
}

func wantText(t *testing.T, column, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", column, got, want)
	}
}

// TestProjectCommitAssistedByDefaultsOff: a row that never answered gets no trailer, whether it was
// created without an answer or inserted without the column.
func TestProjectCommitAssistedByDefaultsOff(t *testing.T) {
	s := testStore(t)

	if err := s.CreateProject(newProject("proj-new", "New", "/tmp/new")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO projects (id, name, path, created_at, updated_at) VALUES ('proj-old', 'Old', '/tmp/old', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert row without the column: %v", err)
	}

	for _, id := range []string{"proj-new", "proj-old"} {
		got, err := s.GetProject(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.AssistedByEnabled() {
			t.Errorf("%s: trailer reads on, want off", id)
		}
	}
}

// TestUpdateProjectCommitAssistedBy: an explicit "off" is stored as an answer, not collapsed into the default.
func TestUpdateProjectCommitAssistedBy(t *testing.T) {
	s := testStore(t)
	off := false
	proj := newProject("proj-1", "One", "/tmp/one")
	proj.CommitAssistedBy = &off
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.CommitAssistedBy == nil || *got.CommitAssistedBy {
		t.Fatalf("an explicit off did not survive the insert: %v", got.CommitAssistedBy)
	}

	if err := s.UpdateProjectCommitAssistedBy("proj-1", true); err != nil {
		t.Fatalf("turn trailer on: %v", err)
	}
	if got, err = s.GetProject("proj-1"); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.CommitAssistedBy == nil || !*got.CommitAssistedBy {
		t.Fatalf("trailer did not turn on: %v", got.CommitAssistedBy)
	}

	if err := s.UpdateProjectCommitAssistedBy("proj-1", false); err != nil {
		t.Fatalf("turn trailer off: %v", err)
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].AssistedByEnabled() {
		t.Fatalf("the list read lost the stored off: %v", projects[0].CommitAssistedBy)
	}
}
