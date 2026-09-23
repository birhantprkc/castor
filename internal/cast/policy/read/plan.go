package read

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
)

// Plan binds fetch policy to each input (policies stay in cast layer).
type Plan map[media.InputID]Policy

// Validate ensures every input has a policy and no stale policies remain.
func (p Plan) Validate(program media.Program) error {
	for _, input := range program.Inputs {
		if _, ok := p[input.ID]; !ok {
			return fmt.Errorf("read plan has no policy for input %q", input.ID)
		}
	}
	for id := range p {
		if _, ok := program.LookupInput(id); !ok {
			return fmt.Errorf("read plan has a policy for unknown input %q", id)
		}
	}
	return nil
}

// ForProgram derives each input independently (demuxed program can read live/VOD at different paces).
func ForProgram(program media.Program, deadline time.Duration) Plan {
	plan := make(Plan, len(program.Inputs))
	for _, input := range program.Inputs {
		plan[input.ID] = For(input.Fetching(), deadline)
	}
	return plan
}

// Clone owns every retry-status slice as well as the map itself.
func (p Plan) Clone() Plan {
	clone := make(Plan, len(p))
	for id, policy := range p {
		policy.RetryStatuses = slices.Clone(policy.RetryStatuses)
		clone[id] = policy
	}
	return clone
}

// Primary returns clock input's policy (pace describing program).
func (p Plan) Primary(program media.Program) Policy {
	return p[program.ClockInput]
}

// Cautious lowers inputs that can give up startup burst (single finite recovery).
func (p Plan) Cautious() (Plan, bool) {
	changed := false
	plan := p.Clone()
	for id, policy := range plan {
		if relaxed, ok := cautious(policy); ok {
			plan[id] = relaxed
			changed = true
		}
	}
	return plan, changed
}

// Pace returns tightest positive pace imposed on any input (zero if none).
func (p Plan) Pace() float64 {
	var pace float64
	for _, policy := range p {
		if policy.Pace.Realtime > 0 && (pace == 0 || policy.Pace.Realtime < pace) {
			pace = policy.Pace.Realtime
		}
	}
	return pace
}

// String returns stable secret-free identity for attempt logs and recovery ledger.
func (p Plan) String() string {
	parts := make([]string, 0, len(p))
	for _, id := range slices.Sorted(maps.Keys(p)) {
		policy := p[id]
		parts = append(parts, fmt.Sprintf("%s=%s@%gx+%s", id, policy.Name, policy.Pace.Realtime, policy.Pace.Burst))
	}
	return strings.Join(parts, ",")
}
