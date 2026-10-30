package broker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pyzyi/dockerbox/internal/dockerapi"
)

// fakeEngine is an in-memory Docker Engine sufficient for lifecycle tests.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer // by name
	networks   map[string]*dockerapi.NetworkJSON
	volumes    map[string]*dockerapi.VolumeJSON
	images     map[string]bool
	pulls      int
	failStart  bool
	failCreate bool
	failVolRm  bool
	nextID     int
}

type fakeContainer struct {
	id, name string
	spec     *dockerapi.ContainerSpec
	state    string
	nets     map[string]dockerapi.SummaryNetwork
}

func newFakeEngine() *fakeEngine {
	f := &fakeEngine{
		containers: map[string]*fakeContainer{},
		networks:   map[string]*dockerapi.NetworkJSON{},
		volumes:    map[string]*dockerapi.VolumeJSON{},
		images:     map[string]bool{},
	}
	f.containers["egress"] = &fakeContainer{id: "egress-id", name: "egress", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{LabelRole: RoleEgress, LabelInstance: "default"}}}
	return f
}

func notFound() error { return &dockerapi.APIError{Status: 404, Message: "not found"} }

func matches(labels map[string]string, filters []string) bool {
	for _, f := range filters {
		k, v, _ := strings.Cut(f, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *fakeEngine) byID(id string) *fakeContainer {
	for _, c := range f.containers {
		if c.id == id || c.name == id {
			return c
		}
	}
	return nil
}

func (f *fakeEngine) ContainerList(_ context.Context, labels []string) ([]dockerapi.ContainerSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dockerapi.ContainerSummary
	for _, c := range f.containers {
		if matches(c.spec.Labels, labels) {
			sum := dockerapi.ContainerSummary{ID: c.id, Names: []string{"/" + c.name}, State: c.state, Labels: c.spec.Labels, Image: c.spec.Image}
			sum.NetworkSettings.Networks = c.nets
			out = append(out, sum)
		}
	}
	return out, nil
}

func (f *fakeEngine) ContainerRename(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, taken := f.containers[name]; taken {
		return &dockerapi.APIError{Status: 409, Message: "name already in use"}
	}
	c := f.byID(id)
	delete(f.containers, c.name)
	c.name = name
	f.containers[name] = c
	return nil
}

func (f *fakeEngine) ContainerCreate(_ context.Context, name string, spec *dockerapi.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[name]; ok {
		return "", &dockerapi.APIError{Status: 409, Message: "conflict"}
	}
	if f.failCreate {
		return "", &dockerapi.APIError{Status: 500, Message: "create failed"}
	}
	f.nextID++
	id := fmt.Sprintf("c%d", f.nextID)
	f.containers[name] = &fakeContainer{id: id, name: name, spec: spec, state: "created"}
	for _, m := range spec.HostConfig.Mounts {
		if _, ok := f.volumes[m.Source]; !ok {
			f.volumes[m.Source] = &dockerapi.VolumeJSON{Name: m.Source, Labels: m.VolumeOptions.Labels}
		}
	}
	return id, nil
}

func (f *fakeEngine) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart {
		return &dockerapi.APIError{Status: 500, Message: "start failed"}
	}
	f.byID(id).state = "running"
	return nil
}

func (f *fakeEngine) ContainerStop(_ context.Context, id string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID(id).state = "exited"
	return nil
}

func (f *fakeEngine) ContainerRestart(context.Context, string, int) error { return nil }

func (f *fakeEngine) ContainerRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.byID(id); c != nil {
		delete(f.containers, c.name)
	}
	return nil
}

func (f *fakeEngine) ContainerStats(context.Context, string) (*dockerapi.Stats, error) {
	st := &dockerapi.Stats{}
	st.MemoryStats.Usage = 100
	st.MemoryStats.Stats = map[string]uint64{"inactive_file": 40}
	return st, nil
}

func (f *fakeEngine) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[ref], nil
}

func (f *fakeEngine) ImagePull(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls++
	f.images[ref] = true
	return nil
}

func (f *fakeEngine) NetworkCreate(_ context.Context, spec *dockerapi.NetworkSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[spec.Name] = &dockerapi.NetworkJSON{ID: "n-" + spec.Name, Name: spec.Name, Internal: spec.Internal,
		Options: spec.Options, Labels: spec.Labels, Containers: map[string]dockerapi.NetworkEndpoint{}}
	return "n-" + spec.Name, nil
}

func (f *fakeEngine) net(id string) *dockerapi.NetworkJSON {
	for _, n := range f.networks {
		if n.ID == id || n.Name == id {
			return n
		}
	}
	return nil
}

func (f *fakeEngine) NetworkInspect(_ context.Context, id string) (*dockerapi.NetworkJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.net(id); n != nil {
		cp := *n
		cp.Containers = map[string]dockerapi.NetworkEndpoint{}
		for k, v := range n.Containers {
			cp.Containers[k] = v
		}
		return &cp, nil
	}
	return nil, notFound()
}

func (f *fakeEngine) NetworkList(_ context.Context, labels []string) ([]dockerapi.NetworkJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dockerapi.NetworkJSON
	for _, n := range f.networks {
		if matches(n.Labels, labels) {
			out = append(out, dockerapi.NetworkJSON{ID: n.ID, Name: n.Name, Labels: n.Labels})
		}
	}
	return out, nil
}

func (f *fakeEngine) NetworkConnect(_ context.Context, network, container string, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.net(network).Containers[container] = dockerapi.NetworkEndpoint{}
	if c := f.byID(container); c != nil {
		if c.nets == nil {
			c.nets = map[string]dockerapi.SummaryNetwork{}
		}
		c.nets[f.net(network).Name] = dockerapi.SummaryNetwork{NetworkID: f.net(network).ID}
	}
	return nil
}

func (f *fakeEngine) NetworkDisconnect(_ context.Context, network, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.net(network).Containers, container)
	return nil
}

func (f *fakeEngine) NetworkRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.net(id); n != nil {
		delete(f.networks, n.Name)
	}
	return nil
}

func (f *fakeEngine) VolumeInspect(_ context.Context, name string) (*dockerapi.VolumeJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.volumes[name]; ok {
		return v, nil
	}
	return nil, notFound()
}

func (f *fakeEngine) VolumeRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failVolRm {
		return &dockerapi.APIError{Status: 409, Message: "volume is in use"}
	}
	delete(f.volumes, name)
	return nil
}

func (f *fakeEngine) ExecCreate(context.Context, string, *dockerapi.ExecSpec) (string, error) {
	return "exec1", nil
}
func (f *fakeEngine) ExecResize(context.Context, string, int, int) error { return nil }
func (f *fakeEngine) ExecAttach(context.Context, string) (net.Conn, *bufio.Reader, error) {
	a, b := net.Pipe()
	go b.Close()
	return a, bufio.NewReader(a), nil
}

func newTestService() (*Service, *fakeEngine) {
	f := newFakeEngine()
	return NewService(testConfig(), f, slog.New(slog.DiscardHandler)), f
}

func TestCreateProvisionsIsolatedBox(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	a, err := svc.Create(ctx, CreateRequest{Name: "alice", MemoryMB: 99999, CPUs: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if a.State != "running" || a.MemoryMB != 4096 || a.CPUs != 0.5 {
		t.Fatalf("unexpected agent %+v", a)
	}
	if f.pulls != 1 {
		t.Errorf("image should be pulled once, got %d", f.pulls)
	}
	n := f.networks[svc.cfg.NetworkName("alice")]
	if n == nil || !n.Internal || n.Options[OptInhibitIPv4] != "true" {
		t.Fatalf("network not isolated: %+v", n)
	}
	if _, ok := n.Containers["egress-id"]; !ok {
		t.Error("egress proxy not attached to agent network")
	}
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate create: %v", err)
	}
}

func TestCreateEnforcesLimitAndNames(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "../etc"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid name accepted: %v", err)
	}
	for i := range 3 {
		if _, err := svc.Create(ctx, CreateRequest{Name: fmt.Sprintf("a%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Create(ctx, CreateRequest{Name: "a3"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit not enforced: %v", err)
	}
}

func TestCreateRollsBackOnStartFailure(t *testing.T) {
	svc, f := newTestService()
	f.failStart = true
	if _, err := svc.Create(context.Background(), CreateRequest{Name: "bob"}); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := f.containers[svc.cfg.ContainerName("bob")]; ok {
		t.Error("container not rolled back")
	}
	if _, ok := f.networks[svc.cfg.NetworkName("bob")]; ok {
		t.Error("network not rolled back")
	}
}

func TestCreateFailsWithoutEgress(t *testing.T) {
	svc, f := newTestService()
	f.containers["egress"].state = "exited"
	if _, err := svc.Create(context.Background(), CreateRequest{Name: "bob"}); !errors.Is(err, ErrNoEgress) {
		t.Fatalf("expected ErrNoEgress, got %v", err)
	}
	if len(f.networks) != 0 {
		t.Error("no network should be created without egress")
	}
}

func TestDeleteKeepsOrRemovesData(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	for _, name := range []string{"keep", "drop"} {
		if _, err := svc.Create(ctx, CreateRequest{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	// an unrelated volume with a colliding name must never be deleted
	f.volumes["dockerbox-agent-ghost-home"] = &dockerapi.VolumeJSON{Name: "dockerbox-agent-ghost-home"}

	if err := svc.Delete(ctx, "keep", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "drop", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "ghost", false); err != nil {
		t.Fatal(err)
	}
	kh, _ := svc.cfg.VolumeNames("keep")
	dh, _ := svc.cfg.VolumeNames("drop")
	if _, ok := f.volumes[kh]; !ok {
		t.Error("keepData removed the volume")
	}
	if _, ok := f.volumes[dh]; ok {
		t.Error("volume not removed")
	}
	if _, ok := f.volumes["dockerbox-agent-ghost-home"]; !ok {
		t.Error("unmanaged volume was removed")
	}
	if len(f.networks) != 0 {
		t.Errorf("networks left: %v", f.networks)
	}
}

func TestUnmanagedContainersAreInvisible(t *testing.T) {
	svc, f := newTestService()
	f.containers["dockerbox-agent-evil"] = &fakeContainer{id: "x", name: "dockerbox-agent-evil", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{LabelAgent: "evil"}}}
	f.containers["other-instance"] = &fakeContainer{id: "y", name: "other-instance", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{LabelManaged: "true", LabelInstance: "other", LabelKind: KindAgent, LabelAgent: "evil"}}}
	ctx := context.Background()
	if err := svc.Stop(ctx, "evil"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unmanaged container must not be controllable: %v", err)
	}
	list, _ := svc.List(ctx)
	if len(list) != 0 {
		t.Fatalf("unmanaged containers listed: %+v", list)
	}
}

func TestReconcileReattachesEgress(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	// simulate re-created egress container
	delete(f.containers, "egress")
	f.containers["egress2"] = &fakeContainer{id: "egress-2", name: "egress2", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{LabelRole: RoleEgress, LabelInstance: "default"}}}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.networks[svc.cfg.NetworkName("alice")].Containers["egress-2"]; !ok {
		t.Fatal("egress not re-attached")
	}
}

func TestRefusesForeignNetwork(t *testing.T) {
	svc, f := newTestService()
	f.networks[svc.cfg.NetworkName("alice")] = &dockerapi.NetworkJSON{ID: "n1", Name: svc.cfg.NetworkName("alice"),
		Labels: map[string]string{}, Containers: map[string]dockerapi.NetworkEndpoint{}}
	if _, err := svc.Create(context.Background(), CreateRequest{Name: "alice"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict for foreign network, got %v", err)
	}
}

func TestRecreateKeepsLimits(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice", MemoryMB: 2048, CPUs: 2}); err != nil {
		t.Fatal(err)
	}
	old := f.containers[svc.cfg.ContainerName("alice")].id
	a, err := svc.Recreate(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if f.containers[svc.cfg.ContainerName("alice")].id == old || a.MemoryMB != 2048 || a.CPUs != 2 {
		t.Fatalf("recreate did not replace container or lost limits: %+v", a)
	}
}

func TestUsageAndResizeAuthorization(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	u, err := svc.Usage(ctx, "alice")
	if err != nil || u.MemoryBytes != 60 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	if err := svc.Resize(ctx, "unknown-exec", 24, 80); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resize of foreign exec must fail: %v", err)
	}
	id, conn, _, err := svc.Terminal(ctx, "alice", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := svc.Resize(ctx, id, 50, 200); err != nil {
		t.Fatal(err)
	}
	if err := svc.Resize(ctx, id, 0, 5000); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out of range resize: %v", err)
	}
	svc.CloseTerminal(id)
	if err := svc.Resize(ctx, id, 24, 80); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed terminal must not be resizable")
	}
}

func TestRecreateFailureKeepsOldBox(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	old := f.containers[svc.cfg.ContainerName("alice")].id
	f.failCreate = true
	if _, err := svc.Recreate(ctx, "alice"); err == nil {
		t.Fatal("expected error")
	}
	c := f.containers[svc.cfg.ContainerName("alice")]
	if c == nil || c.id != old || c.state != "running" {
		t.Fatalf("old box not restored: %+v", c)
	}
	if a, err := svc.Get(ctx, "alice"); err != nil || a.State != "running" {
		t.Fatalf("agent lost after failed recreate: %+v %v", a, err)
	}
}

func TestForeignVolumeIsNotReused(t *testing.T) {
	svc, f := newTestService()
	home, _ := svc.cfg.VolumeNames("alice")
	f.volumes[home] = &dockerapi.VolumeJSON{Name: home, Labels: map[string]string{
		LabelManaged: "true", LabelInstance: "other", LabelKind: KindVolume, LabelAgent: "alice"}}
	if _, err := svc.Create(context.Background(), CreateRequest{Name: "alice"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign volume must be refused, got %v", err)
	}
	if len(f.networks) != 0 {
		t.Error("nothing should be created when volumes conflict")
	}
}

func TestInstancesUseSeparateNames(t *testing.T) {
	a, b := testConfig(), testConfig()
	b.Instance = "lab"
	if a.ContainerName("x") != "dockerbox-agent-x" || a.NetworkName("x") != "dockerbox-net-x" {
		t.Fatalf("default names changed: %s %s", a.ContainerName("x"), a.NetworkName("x"))
	}
	ah, aw := a.VolumeNames("x")
	bh, bw := b.VolumeNames("x")
	if b.ContainerName("x") == a.ContainerName("x") || b.NetworkName("x") == a.NetworkName("x") || ah == bh || aw == bw {
		t.Fatal("instances must not share object names")
	}
	// instance "agent" must not collide with default-instance agent "agent-x"
	c := testConfig()
	c.Instance = "agent"
	if c.ContainerName("x") == a.ContainerName("agent-x") {
		t.Fatal("name collision between instances")
	}
}

func TestReconcileDoesNotResurrectDeletedNetworks(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	// network vanished between list and inspect (concurrent delete)
	delete(f.networks, svc.cfg.NetworkName("alice"))
	if _, err := svc.ensureNetwork(ctx, "alice", "egress-id", false); err != nil {
		t.Fatal(err)
	}
	if len(f.networks) != 0 {
		t.Fatal("reconcile must not create networks")
	}
}

func TestParkedContainersAreIgnored(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	spec := f.containers[svc.cfg.ContainerName("alice")].spec
	f.containers["parked"] = &fakeContainer{id: "p1", name: svc.parkedName("alice"), state: "exited", spec: spec}
	list, _ := svc.List(ctx)
	if len(list) != 1 {
		t.Fatalf("parked container listed: %+v", list)
	}
}

func TestParkedNameCannotCollideWithAgents(t *testing.T) {
	svc, _ := newTestService()
	parked := strings.TrimPrefix(svc.parkedName("foo"), svc.cfg.ContainerName(""))
	if ValidName(parked) {
		t.Fatalf("parked suffix %q is a valid agent name", parked)
	}
}

func TestRecreateAndDeleteCleanUpParkedContainers(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	// leftover of an interrupted recreate
	spec := f.containers[svc.cfg.ContainerName("alice")].spec
	f.containers[svc.parkedName("alice")] = &fakeContainer{id: "old", name: svc.parkedName("alice"), state: "exited", spec: spec}
	if _, err := svc.Recreate(ctx, "alice"); err != nil {
		t.Fatalf("recreate must succeed despite leftover: %v", err)
	}
	if _, ok := f.containers[svc.parkedName("alice")]; ok {
		t.Fatal("parked container left behind after recreate")
	}
	f.containers[svc.parkedName("alice")] = &fakeContainer{id: "old2", name: svc.parkedName("alice"), state: "exited", spec: spec}
	if err := svc.Delete(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 1 { // only the egress container remains
		t.Fatalf("containers left: %v", f.containers)
	}
}

func TestDeleteReportsVolumeFailures(t *testing.T) {
	svc, f := newTestService()
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	f.failVolRm = true
	if err := svc.Delete(ctx, "alice", false); err == nil {
		t.Fatal("delete must fail when the data could not be removed")
	}
}

func TestEgressFoundByComposeLabels(t *testing.T) {
	svc, f := newTestService()
	// labels rewritten by the platform: only compose labels remain
	delete(f.containers, "egress")
	svc.cfg.SelfID = "abcdef123456"
	f.containers["broker"] = &fakeContainer{id: "abcdef1234567890", name: "broker", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{composeService: "broker", composeProject: "dockerbox"}}}
	f.containers["other-egress"] = &fakeContainer{id: "x1", name: "other-egress", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{composeService: "egress", composeProject: "someone-else"}}}
	f.containers["egress"] = &fakeContainer{id: "egress-id", name: "egress", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: map[string]string{composeService: "egress", composeProject: "dockerbox"}}}
	id, err := svc.egressContainer(context.Background())
	if err != nil || id != "egress-id" {
		t.Fatalf("egress = %q, %v; want egress-id of the own project", id, err)
	}
}

func TestBrokerAPIRequiresToken(t *testing.T) {
	svc, _ := newTestService()
	srv := httptest.NewServer(NewServer(svc, testConfig(), "s3cret-token-0123456789abcdef0123", "test", slog.New(slog.DiscardHandler)).Handler())
	defer srv.Close()
	get := func(path, auth string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("/healthz", ""); code != http.StatusNoContent {
		t.Fatalf("healthz must not need a token: %d", code)
	}
	for _, auth := range []string{"", "Bearer wrong", "s3cret-token-0123456789abcdef0123"} {
		if code := get("/v1/agents", auth); code != http.StatusUnauthorized {
			t.Fatalf("auth %q: %d, want 401", auth, code)
		}
	}
	if code := get("/v1/agents", "Bearer s3cret-token-0123456789abcdef0123"); code != http.StatusOK {
		t.Fatalf("valid token: %d", code)
	}
}

func TestLoadOrCreateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "broker.token")
	a, err := LoadOrCreateToken(path)
	if err != nil || len(a) != 64 {
		t.Fatalf("token %q, %v", a, err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", st.Mode())
	}
	if b, _ := LoadOrCreateToken(path); b != a {
		t.Fatal("token not persisted")
	}
}

func TestAdvertiseJoinsWebAndPublishesAddress(t *testing.T) {
	svc, f := newTestService()
	svc.cfg.SelfID = "aaaaaaaaaaaa"
	compose := func(service string) map[string]string {
		return map[string]string{composeProject: "dockerbox", composeService: service}
	}
	f.networks["bridge"] = &dockerapi.NetworkJSON{ID: "n-bridge", Name: "bridge", Containers: map[string]dockerapi.NetworkEndpoint{}}
	f.containers["broker"] = &fakeContainer{id: "aaaaaaaaaaaa1111", name: "broker", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: compose("broker")},
		nets: map[string]dockerapi.SummaryNetwork{"bridge": {NetworkID: "n-bridge", IPAddress: "172.17.0.3"}}}
	// Like ZimaOS: web ended up on a different network than the broker.
	f.containers["web"] = &fakeContainer{id: "bbbbbbbbbbbb2222", name: "web", state: "running",
		spec: &dockerapi.ContainerSpec{Labels: compose("web")},
		nets: map[string]dockerapi.SummaryNetwork{"other": {NetworkID: "n-other", IPAddress: "10.9.0.2"}}}
	path := filepath.Join(t.TempDir(), "broker.addr")
	if err := svc.Advertise(context.Background(), 8081, path); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.networks["bridge"].Containers["bbbbbbbbbbbb2222"]; !ok {
		t.Fatal("web was not attached to the broker's network")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "http://172.17.0.3:8081\n" {
		t.Fatalf("published address %q", b)
	}
	// Second run: already shared, nothing to change.
	if err := svc.Advertise(context.Background(), 8081, path); err != nil {
		t.Fatal(err)
	}
}
