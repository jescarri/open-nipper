package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Phase represents a shutdown priority. Lower values shut down first.
// Components at the same phase shut down concurrently.
type Phase int

const (
	PhaseHTTP       Phase = 10 // Stop accepting new HTTP connections
	PhaseAdapters   Phase = 20 // Stop channel adapters (cron, MQTT, RabbitMQ channel)
	PhaseDrain      Phase = 30 // Drain in-flight requests
	PhaseConsumers  Phase = 40 // Stop event consumers / dispatchers
	PhasePublishers Phase = 50 // Stop publishers
	PhaseBrokers    Phase = 60 // Close broker connections
	PhaseDatastore  Phase = 70 // Close database
	PhaseTelemetry  Phase = 80 // Flush telemetry
	PhaseLogger     Phase = 90 // Flush logger
)

// Component represents a managed lifecycle component.
type Component struct {
	Name    string
	Phase   Phase
	StopFn  func(ctx context.Context) error
	StartFn func(ctx context.Context) error // optional
}

// Manager coordinates ordered startup and shutdown of gateway components.
type Manager struct {
	mu         sync.Mutex
	components []Component
	logger     *zap.Logger
	timeout    time.Duration
}

// NewManager creates a lifecycle manager with the given per-phase timeout.
func NewManager(logger *zap.Logger, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Manager{
		logger:  logger,
		timeout: timeout,
	}
}

// Register adds a component to the lifecycle manager.
func (m *Manager) Register(c Component) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.components = append(m.components, c)
}

// RegisterStop is a convenience for components that only need stop logic.
func (m *Manager) RegisterStop(name string, phase Phase, fn func(ctx context.Context) error) {
	m.Register(Component{Name: name, Phase: phase, StopFn: fn})
}

// Shutdown stops all components in phase order (lowest first).
// Components within the same phase stop concurrently.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	comps := make([]Component, len(m.components))
	copy(comps, m.components)
	m.mu.Unlock()

	sort.SliceStable(comps, func(i, j int) bool {
		return comps[i].Phase < comps[j].Phase
	})

	groups := groupByPhase(comps)
	var allErrs []error

	for _, g := range groups {
		phaseCtx, cancel := context.WithTimeout(ctx, m.timeout)
		errs := m.shutdownGroup(phaseCtx, g)
		cancel()
		allErrs = append(allErrs, errs...)
	}

	if len(allErrs) > 0 {
		return fmt.Errorf("shutdown errors: %v", allErrs)
	}
	return nil
}

type phaseGroup struct {
	phase      Phase
	components []Component
}

func groupByPhase(comps []Component) []phaseGroup {
	if len(comps) == 0 {
		return nil
	}
	var groups []phaseGroup
	current := phaseGroup{phase: comps[0].Phase}
	for _, c := range comps {
		if c.Phase != current.phase {
			groups = append(groups, current)
			current = phaseGroup{phase: c.Phase}
		}
		current.components = append(current.components, c)
	}
	groups = append(groups, current)
	return groups
}

func (m *Manager) shutdownGroup(ctx context.Context, g phaseGroup) []error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for _, c := range g.components {
		wg.Add(1)
		go func(comp Component) {
			defer wg.Done()
			m.logger.Info("stopping component",
				zap.String("name", comp.Name),
				zap.Int("phase", int(comp.Phase)),
			)
			if comp.StopFn == nil {
				return
			}
			if err := comp.StopFn(ctx); err != nil {
				m.logger.Error("component stop failed",
					zap.String("name", comp.Name),
					zap.Error(err),
				)
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", comp.Name, err))
				mu.Unlock()
			}
		}(c)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		m.logger.Warn("shutdown phase timed out",
			zap.Int("phase", int(g.phase)),
		)
		mu.Lock()
		errs = append(errs, fmt.Errorf("phase %d: %w", g.phase, ctx.Err()))
		mu.Unlock()
	}

	return errs
}

// ComponentCount returns the number of registered components.
func (m *Manager) ComponentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.components)
}
