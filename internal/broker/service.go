package broker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/pyzyi/dockerbox/internal/dockerapi"
)

// Domain errors, mapped to HTTP status codes by the server.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("agent not found")
	ErrConflict = errors.New("agent already exists")
	ErrLimit    = errors.New("agent limit reached")
	ErrNoEgress = errors.New("egress proxy is not running")
	ErrStopped  = errors.New("agent is not running")
)

const (
	labelMemory = "dockerbox.memory-mb"
	labelCPUs   = "dockerbox.cpus"
)

// Engine is the subset of the Docker Engine API the broker uses.
type Engine interface {
	ContainerList(ctx context.Context, labels []string) ([]dockerapi.ContainerSummary, error)
	ContainerCreate(ctx context.Context, name string, spec *dockerapi.ContainerSpec) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, timeout int) error
	ContainerRestart(ctx context.Context, id string, timeout int) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerRename(ctx context.Context, id, name string) error
	ContainerStats(ctx context.Context, id string) (*dockerapi.Stats, error)
	ImageExists(ctx context.Context, ref string) (bool, error)
	ImagePull(ctx context.Context, ref string) error
	NetworkCreate(ctx context.Context, spec *dockerapi.NetworkSpec) (string, error)
	NetworkInspect(ctx context.Context, id string) (*dockerapi.NetworkJSON, error)
	NetworkList(ctx context.Context, labels []string) ([]dockerapi.NetworkJSON, error)
	NetworkConnect(ctx context.Context, network, container string, aliases []string) error
	NetworkDisconnect(ctx context.Context, network, container string) error
	NetworkRemove(ctx context.Context, id string) error
	VolumeInspect(ctx context.Context, name string) (*dockerapi.VolumeJSON, error)
	VolumeRemove(ctx context.Context, name string) error
	ExecCreate(ctx context.Context, container string, spec *dockerapi.ExecSpec) (string, error)
	ExecResize(ctx context.Context, execID string, rows, cols int) error
	ExecAttach(ctx context.Context, execID string) (net.Conn, *bufio.Reader, error)
}

// Agent is the public view of an agent box.
type Agent struct {
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Status   string    `json:"status"`
	Image    string    `json:"image"`
	Created  time.Time `json:"created"`
	MemoryMB int       `json:"memoryMB"`
	CPUs     float64   `json:"cpus"`
}

// CreateRequest is the input for creating an agent box.
type CreateRequest struct {
	Name     string  `json:"name"`
	MemoryMB int     `json:"memoryMB"`
	CPUs     float64 `json:"cpus"`
}

// Usage is a resource usage sample of a running box.
type Usage struct {
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryBytes   uint64  `json:"memoryBytes"`
	MemoryLimit   uint64  `json:"memoryLimit"`
	Processes     uint64  `json:"processes"`
	SampledAtUnix int64   `json:"sampledAt"`
}

type cpuSample struct{ total, system uint64 }

// Service implements the agent lifecycle on top of the Docker Engine.
type Service struct {
	cfg    *Config
	engine Engine
	log    *slog.Logger

	// lifecycle serialises create, recreate and delete so that name checks,
	// the agent limit and network setup cannot race.
	lifecycle sync.Mutex

	mu      sync.Mutex
	cpuPrev map[string]cpuSample
	execs   map[string]string // exec ID -> agent name, for resize authorisation
}

// NewService returns a Service.
func NewService(cfg *Config, engine Engine, log *slog.Logger) *Service {
	return &Service{
		cfg: cfg, engine: engine, log: log,
		cpuPrev: map[string]cpuSample{},
		execs:   map[string]string{},
	}
}

// List returns all agent boxes of this instance, sorted by name.
func (s *Service) List(ctx context.Context) ([]Agent, error) {
	cs, err := s.engine.ContainerList(ctx, s.cfg.managedFilter(KindAgent))
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(cs))
	for _, c := range cs {
		// Skip foreign objects and containers parked by an interrupted recreate.
		if !s.cfg.isManaged(c.Labels, KindAgent) || !ValidName(c.Labels[LabelAgent]) ||
			!slices.Contains(c.Names, "/"+s.cfg.ContainerName(c.Labels[LabelAgent])) {
			continue
		}
		out = append(out, agentFromSummary(c))
	}
	slices.SortFunc(out, func(a, b Agent) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out, nil
}

func agentFromSummary(c dockerapi.ContainerSummary) Agent {
	mem, _ := strconv.Atoi(c.Labels[labelMemory])
	cpus, _ := strconv.ParseFloat(c.Labels[labelCPUs], 64)
	return Agent{
		Name: c.Labels[LabelAgent], State: c.State, Status: c.Status, Image: c.Image,
		Created: time.Unix(c.Created, 0).UTC(), MemoryMB: mem, CPUs: cpus,
	}
}

// Get returns one agent box.
func (s *Service) Get(ctx context.Context, name string) (*Agent, error) {
	c, err := s.find(ctx, name)
	if err != nil {
		return nil, err
	}
	a := agentFromSummary(*c)
	return &a, nil
}

// find returns the managed container of an agent or ErrNotFound.
func (s *Service) find(ctx context.Context, name string) (*dockerapi.ContainerSummary, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: name must match %s", ErrInvalid, nameRE)
	}
	cs, err := s.engine.ContainerList(ctx, append(s.cfg.managedFilter(KindAgent), LabelAgent+"="+name))
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if s.cfg.isManaged(c.Labels, KindAgent) && c.Labels[LabelAgent] == name &&
			slices.Contains(c.Names, "/"+s.cfg.ContainerName(name)) {
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// Create provisions a new isolated agent box and starts it.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Agent, error) {
	if !ValidName(req.Name) {
		return nil, fmt.Errorf("%w: name must be 1-31 characters a-z, 0-9 or '-' and start with a letter or digit", ErrInvalid)
	}
	// Pull before taking the lock: a slow download must not block other users.
	if err := s.ensureImage(ctx); err != nil {
		return nil, err
	}

	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	existing, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(existing, func(a Agent) bool { return a.Name == req.Name }) {
		return nil, ErrConflict
	}
	if len(existing) >= s.cfg.MaxAgents {
		return nil, fmt.Errorf("%w (%d)", ErrLimit, s.cfg.MaxAgents)
	}
	egress, err := s.egressContainer(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.checkVolumes(ctx, req.Name); err != nil {
		return nil, err
	}
	mem, cpus := s.cfg.clampMemory(req.MemoryMB), s.cfg.clampCPUs(req.CPUs)
	netCreated, err := s.ensureNetwork(ctx, req.Name, egress, true)
	if err != nil {
		return nil, err
	}
	if err := s.createAndStart(ctx, req.Name, mem, cpus); err != nil {
		if netCreated {
			s.removeNetwork(context.WithoutCancel(ctx), req.Name)
		}
		return nil, err
	}
	s.log.Info("agent created", "agent", req.Name, "memoryMB", mem, "cpus", cpus)
	return s.Get(ctx, req.Name)
}

// checkVolumes refuses to reuse volumes with the agent's names that were not
// created for this agent by this instance. Labels are only applied when Docker
// creates a volume, so an existing foreign volume would otherwise be mounted.
func (s *Service) checkVolumes(ctx context.Context, name string) error {
	home, ws := s.cfg.VolumeNames(name)
	for _, v := range []string{home, ws} {
		vol, err := s.engine.VolumeInspect(ctx, v)
		if dockerapi.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !s.cfg.isManaged(vol.Labels, KindVolume) || vol.Labels[LabelAgent] != name {
			return fmt.Errorf("%w: volume %s exists but does not belong to this agent", ErrConflict, v)
		}
	}
	return nil
}

func (s *Service) createAndStart(ctx context.Context, name string, mem int, cpus float64) error {
	spec := s.cfg.ContainerSpec(name, mem, cpus)
	spec.Labels[labelMemory] = strconv.Itoa(mem)
	spec.Labels[labelCPUs] = strconv.FormatFloat(cpus, 'f', -1, 64)
	home, ws := s.cfg.VolumeNames(name)
	if err := Validate(spec, s.cfg.NetworkName(name), home, ws); err != nil {
		// Should never happen; refusing is the safe outcome.
		s.log.Error("generated container spec violates policy", "agent", name, "err", err)
		return err
	}
	id, err := s.engine.ContainerCreate(ctx, s.cfg.ContainerName(name), spec)
	if err != nil {
		if dockerapi.IsConflict(err) {
			return fmt.Errorf("%w: a container named %s already exists", ErrConflict, s.cfg.ContainerName(name))
		}
		return err
	}
	if err := s.engine.ContainerStart(ctx, id); err != nil {
		_ = s.engine.ContainerRemove(context.WithoutCancel(ctx), id)
		return err
	}
	return nil
}

// Recreate replaces the container of an agent with a fresh one from the
// configured image. /root and /workspace volumes are kept.
func (s *Service) Recreate(ctx context.Context, name string) (*Agent, error) {
	if _, err := s.find(ctx, name); err != nil {
		return nil, err
	}
	// Always pull: the point of re-creating is to pick up a newer image, which
	// a floating tag (e.g. "latest") would otherwise never deliver.
	if err := s.engine.ImagePull(ctx, s.cfg.AgentImage); err != nil {
		if ok, _ := s.engine.ImageExists(ctx, s.cfg.AgentImage); !ok {
			return nil, fmt.Errorf("pull %s: %w", s.cfg.AgentImage, err)
		}
		s.log.Warn("image pull failed, re-creating from the local image", "image", s.cfg.AgentImage, "err", err)
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	c, err := s.find(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.removeParked(ctx, name); err != nil {
		return nil, err
	}
	old := agentFromSummary(*c)
	egress, err := s.egressContainer(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkVolumes(ctx, name); err != nil {
		return nil, err
	}
	if _, err := s.ensureNetwork(ctx, name, egress, true); err != nil {
		return nil, err
	}
	// Keep the old container aside until the replacement runs, so a broken
	// image cannot make the agent disappear.
	if err := s.engine.ContainerStop(ctx, c.ID, 10); err != nil {
		return nil, err
	}
	aside := s.parkedName(name)
	if err := s.engine.ContainerRename(ctx, c.ID, aside); err != nil {
		s.restoreAfterFailedRecreate(ctx, c.ID, "", old.State)
		return nil, err
	}
	if err := s.createAndStart(ctx, name, s.cfg.clampMemory(old.MemoryMB), s.cfg.clampCPUs(old.CPUs)); err != nil {
		s.restoreAfterFailedRecreate(ctx, c.ID, s.cfg.ContainerName(name), old.State)
		return nil, err
	}
	if err := s.engine.ContainerRemove(ctx, c.ID); err != nil {
		s.log.Warn("could not remove replaced container", "agent", name, "err", err)
	}
	s.log.Info("agent recreated", "agent", name, "image", s.cfg.AgentImage)
	return s.Get(ctx, name)
}

// parkedName is where Recreate keeps the previous container until the new one
// runs. Agent names cannot contain ".", so this never collides with an agent.
func (s *Service) parkedName(name string) string { return s.cfg.ContainerName(name) + ".replaced" }

// removeParked deletes containers of an agent left behind by an interrupted
// recreate (anything with the agent's labels but not its exact name).
func (s *Service) removeParked(ctx context.Context, name string) error {
	cs, err := s.engine.ContainerList(ctx, append(s.cfg.managedFilter(KindAgent), LabelAgent+"="+name))
	if err != nil {
		return err
	}
	for _, c := range cs {
		if !s.cfg.isManaged(c.Labels, KindAgent) || c.Labels[LabelAgent] != name ||
			slices.Contains(c.Names, "/"+s.cfg.ContainerName(name)) {
			continue
		}
		s.log.Warn("removing leftover container of an interrupted recreate", "agent", name, "container", c.ID)
		if err := s.engine.ContainerRemove(ctx, c.ID); err != nil {
			return err
		}
	}
	return nil
}

// restoreAfterFailedRecreate puts the previous container back in place.
func (s *Service) restoreAfterFailedRecreate(ctx context.Context, id, originalName, prevState string) {
	ctx = context.WithoutCancel(ctx)
	if originalName != "" {
		if err := s.engine.ContainerRename(ctx, id, originalName); err != nil {
			s.log.Error("could not restore container name after failed recreate", "container", id, "err", err)
		}
	}
	if prevState == "running" {
		_ = s.engine.ContainerStart(ctx, id)
	}
}

// Delete removes an agent box, its network and, unless keepData is set, its volumes.
func (s *Service) Delete(ctx context.Context, name string, keepData bool) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: invalid name", ErrInvalid)
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	c, err := s.find(ctx, name)
	switch {
	case err == nil:
		if err := s.engine.ContainerRemove(ctx, c.ID); err != nil {
			return err
		}
	case errors.Is(err, ErrNotFound):
		// Continue: clean up leftovers of a partially deleted agent.
	default:
		return err
	}
	if err := s.removeParked(ctx, name); err != nil {
		return err
	}
	s.removeNetwork(ctx, name)
	if !keepData {
		// The user asked for the data to be gone: report every failure.
		home, ws := s.cfg.VolumeNames(name)
		for _, v := range []string{home, ws} {
			vol, err := s.engine.VolumeInspect(ctx, v)
			if dockerapi.IsNotFound(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect volume %s: %w", v, err)
			}
			if !s.cfg.isManaged(vol.Labels, KindVolume) || vol.Labels[LabelAgent] != name {
				continue // never touch volumes we do not own
			}
			if err := s.engine.VolumeRemove(ctx, v); err != nil {
				return fmt.Errorf("remove volume %s: %w", v, err)
			}
		}
	}
	s.forgetCPU(name)
	s.log.Info("agent deleted", "agent", name, "keepData", keepData)
	return nil
}

// Start starts a stopped agent box.
func (s *Service) Start(ctx context.Context, name string) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	c, err := s.find(ctx, name)
	if err != nil {
		return err
	}
	// Make sure the box can reach the proxy (e.g. after the stack was re-created).
	if egress, err := s.egressContainer(ctx); err == nil {
		if _, err := s.ensureNetwork(ctx, name, egress, true); err != nil {
			return err
		}
	}
	s.log.Info("agent start", "agent", name)
	return s.engine.ContainerStart(ctx, c.ID)
}

// Stop stops an agent box.
func (s *Service) Stop(ctx context.Context, name string) error {
	c, err := s.find(ctx, name)
	if err != nil {
		return err
	}
	s.log.Info("agent stop", "agent", name)
	s.forgetCPU(name)
	return s.engine.ContainerStop(ctx, c.ID, 10)
}

// Restart restarts an agent box.
func (s *Service) Restart(ctx context.Context, name string) error {
	c, err := s.find(ctx, name)
	if err != nil {
		return err
	}
	s.log.Info("agent restart", "agent", name)
	s.forgetCPU(name)
	return s.engine.ContainerRestart(ctx, c.ID, 10)
}

// Usage returns current resource usage of a running agent box.
func (s *Service) Usage(ctx context.Context, name string) (*Usage, error) {
	c, err := s.find(ctx, name)
	if err != nil {
		return nil, err
	}
	if c.State != "running" {
		return nil, ErrStopped
	}
	st, err := s.engine.ContainerStats(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	u := &Usage{
		MemoryBytes:   memoryUsage(st),
		MemoryLimit:   st.MemoryStats.Limit,
		Processes:     st.PidsStats.Current,
		SampledAtUnix: time.Now().Unix(),
	}
	cur := cpuSample{st.CPUStats.CPUUsage.TotalUsage, st.CPUStats.SystemUsage}
	s.mu.Lock()
	prev, ok := s.cpuPrev[name]
	s.cpuPrev[name] = cur
	s.mu.Unlock()
	if ok && cur.system > prev.system && cur.total >= prev.total {
		cpus := float64(max(st.CPUStats.OnlineCPUs, 1))
		u.CPUPercent = float64(cur.total-prev.total) / float64(cur.system-prev.system) * cpus * 100
	}
	return u, nil
}

// memoryUsage subtracts reclaimable page cache like `docker stats` does.
func memoryUsage(st *dockerapi.Stats) uint64 {
	cache := st.MemoryStats.Stats["inactive_file"] // cgroup v2
	if v, ok := st.MemoryStats.Stats["total_inactive_file"]; ok {
		cache = v // cgroup v1
	}
	if cache < st.MemoryStats.Usage {
		return st.MemoryStats.Usage - cache
	}
	return st.MemoryStats.Usage
}

func (s *Service) forgetCPU(name string) {
	s.mu.Lock()
	delete(s.cpuPrev, name)
	s.mu.Unlock()
}

// Terminal opens an interactive TTY session in a running agent box.
func (s *Service) Terminal(ctx context.Context, name string, rows, cols int) (execID string, conn net.Conn, r *bufio.Reader, err error) {
	c, err := s.find(ctx, name)
	if err != nil {
		return "", nil, nil, err
	}
	if c.State != "running" {
		return "", nil, nil, ErrStopped
	}
	execID, err = s.engine.ExecCreate(ctx, c.ID, TerminalExec(rows, cols))
	if err != nil {
		return "", nil, nil, err
	}
	conn, r, err = s.engine.ExecAttach(ctx, execID)
	if err != nil {
		return "", nil, nil, err
	}
	s.mu.Lock()
	s.execs[execID] = name
	s.mu.Unlock()
	s.log.Info("terminal opened", "agent", name)
	return execID, conn, r, nil
}

// CloseTerminal forgets an exec session.
func (s *Service) CloseTerminal(execID string) {
	s.mu.Lock()
	delete(s.execs, execID)
	s.mu.Unlock()
}

// Resize changes the TTY size of a terminal opened through this broker.
func (s *Service) Resize(ctx context.Context, execID string, rows, cols int) error {
	s.mu.Lock()
	_, ok := s.execs[execID]
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if rows < 1 || cols < 1 || rows > 1000 || cols > 1000 {
		return fmt.Errorf("%w: terminal size out of range", ErrInvalid)
	}
	return s.engine.ExecResize(ctx, execID, rows, cols)
}

func (s *Service) ensureImage(ctx context.Context) error {
	ok, err := s.engine.ImageExists(ctx, s.cfg.AgentImage)
	if err != nil || ok {
		return err
	}
	s.log.Info("pulling agent image", "image", s.cfg.AgentImage)
	if err := s.engine.ImagePull(ctx, s.cfg.AgentImage); err != nil {
		return fmt.Errorf("pull %s: %w", s.cfg.AgentImage, err)
	}
	return nil
}

// egressContainer returns the ID of the running egress proxy of this instance.
func (s *Service) egressContainer(ctx context.Context) (string, error) {
	cs, err := s.engine.ContainerList(ctx, []string{LabelRole + "=" + RoleEgress, LabelInstance + "=" + s.cfg.Instance})
	if err != nil {
		return "", err
	}
	for _, c := range cs {
		if c.State == "running" {
			return c.ID, nil
		}
	}
	return "", ErrNoEgress
}

// ensureNetwork makes sure the isolated network of an agent exists (creating
// it only if create is set) and that the egress proxy is attached. It reports
// whether the network was created. Callers must hold s.lifecycle.
func (s *Service) ensureNetwork(ctx context.Context, name, egress string, create bool) (created bool, err error) {
	spec := s.cfg.NetworkSpec(name)
	if err := ValidateNetwork(spec); err != nil {
		return false, err
	}
	n, err := s.engine.NetworkInspect(ctx, spec.Name)
	switch {
	case dockerapi.IsNotFound(err) && !create:
		return false, nil
	case dockerapi.IsNotFound(err):
		if _, err := s.engine.NetworkCreate(ctx, spec); err != nil {
			return false, err
		}
		created = true
		if n, err = s.engine.NetworkInspect(ctx, spec.Name); err != nil {
			return true, err
		}
	case err != nil:
		return false, err
	}
	// Refuse to reuse a network we did not create or that lost its isolation.
	if !s.cfg.isManaged(n.Labels, KindNetwork) || n.Labels[LabelAgent] != name ||
		!n.Internal || n.Options[OptInhibitIPv4] != "true" {
		return created, fmt.Errorf("%w: network %s exists but is not an isolated dockerbox network", ErrConflict, spec.Name)
	}
	if _, attached := n.Containers[egress]; !attached {
		if err := s.engine.NetworkConnect(ctx, n.ID, egress, []string{EgressAlias}); err != nil && !alreadyAttached(err) {
			if created {
				_ = s.engine.NetworkRemove(context.WithoutCancel(ctx), n.ID)
			}
			return false, fmt.Errorf("attach egress proxy: %w", err)
		}
	}
	return created, nil
}

func alreadyAttached(err error) bool {
	var apiErr *dockerapi.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 403 // "endpoint already exists"
}

// removeNetwork detaches all endpoints (the egress proxy) and deletes the network.
func (s *Service) removeNetwork(ctx context.Context, name string) {
	n, err := s.engine.NetworkInspect(ctx, s.cfg.NetworkName(name))
	if err != nil || !s.cfg.isManaged(n.Labels, KindNetwork) || n.Labels[LabelAgent] != name {
		return
	}
	for id := range n.Containers {
		_ = s.engine.NetworkDisconnect(ctx, n.ID, id)
	}
	if err := s.engine.NetworkRemove(ctx, n.ID); err != nil {
		s.log.Warn("could not remove network", "network", n.Name, "err", err)
	}
}

// Reconcile attaches the egress proxy to every agent network. The proxy loses
// these attachments when the compose stack re-creates its container.
func (s *Service) Reconcile(ctx context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	egress, err := s.egressContainer(ctx)
	if err != nil {
		return err
	}
	nets, err := s.engine.NetworkList(ctx, s.cfg.managedFilter(KindNetwork))
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range nets {
		name := n.Labels[LabelAgent]
		if !ValidName(name) {
			continue
		}
		// Never create here: a network that vanished was deleted on purpose.
		if _, err := s.ensureNetwork(ctx, name, egress, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RunReconciler calls Reconcile periodically until ctx is cancelled.
func (s *Service) RunReconciler(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
