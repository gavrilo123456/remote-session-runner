package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"runtime"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
)

// PRDServiceController operates only the canonical unsuffixed PRD service on
// the current supported host. It deliberately exposes no arbitrary unit,
// label, or command input.
type PRDServiceController struct {
	base *SimpleServiceController
}

func NewPRDServiceController() (*PRDServiceController, error) {
	base, err := NewSimpleServiceController()
	if err != nil {
		return nil, err
	}
	return &PRDServiceController{base: base}, nil
}

func (c *PRDServiceController) Status(ctx context.Context, plan PRDUpgradePlan) (bool, error) {
	ctx, cancel := simpleBoundedContext(ctx, 5*time.Second)
	defer cancel()
	if err := c.validate(plan); err != nil {
		return false, err
	}
	if plan.Namespace.Host == config.HostKindMac {
		local, err := c.base.macAgentStatus(ctx, plan.Namespace.LocalAgentLabel)
		if err != nil {
			return false, err
		}
		locald, err := c.base.macAgentStatus(ctx, plan.Namespace.LocalDAgentLabel)
		return local || locald, err
	}
	result, err := c.base.runner.Run(ctx, "/usr/bin/systemctl", "is-active", plan.Namespace.RunnerUnit)
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 && strings.TrimSpace(result.Output) == "active" {
		return true, nil
	}
	if result.ExitCode == 3 || (result.ExitCode == 0 && strings.TrimSpace(result.Output) != "active") {
		return false, nil
	}
	if result.ExitCode == 4 {
		return false, fmt.Errorf("canonical unsuffixed PRD unit is missing")
	}
	return false, fmt.Errorf("canonical PRD service status failed (exit %d)", result.ExitCode)
}

func (c *PRDServiceController) Stop(ctx context.Context, plan PRDUpgradePlan) error {
	ctx, cancel := simpleBoundedContext(ctx, 45*time.Second)
	defer cancel()
	if err := c.validate(plan); err != nil {
		return err
	}
	active, err := c.Status(ctx, plan)
	if err != nil || !active {
		return err
	}
	if plan.Namespace.Host == config.HostKindMac {
		for _, label := range []string{plan.Namespace.LocalDAgentLabel, plan.Namespace.LocalAgentLabel} {
			isActive, statusErr := c.base.macAgentStatus(ctx, label)
			if statusErr != nil {
				return statusErr
			}
			if isActive {
				if err := c.base.launchctl(ctx, "bootout", "gui/"+c.base.uid+"/"+label); err != nil {
					return err
				}
			}
		}
	} else {
		result, err := c.base.runner.Run(ctx, "/usr/bin/sudo", "-n", "/usr/bin/systemctl", "stop", plan.Namespace.RunnerUnit)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("stop canonical PRD systemd service failed (exit %d)", result.ExitCode)
		}
	}
	return c.waitForState(ctx, plan, false)
}

func (c *PRDServiceController) Start(ctx context.Context, plan PRDUpgradePlan) error {
	ctx, cancel := simpleBoundedContext(ctx, 45*time.Second)
	defer cancel()
	if err := c.validate(plan); err != nil {
		return err
	}
	active, err := c.Status(ctx, plan)
	if err != nil {
		return err
	}
	if plan.Namespace.Host == config.HostKindMac {
		started := make([]string, 0, 2)
		for _, agent := range []struct{ label, plist string }{
			{plan.Namespace.LocalAgentLabel, plan.Namespace.LocalAgentPlist},
			{plan.Namespace.LocalDAgentLabel, plan.Namespace.LocalDAgentPlist},
		} {
			isActive, statusErr := c.base.macAgentStatus(ctx, agent.label)
			if statusErr != nil {
				return errors.Join(statusErr, c.base.stopMacAgents(ctx, Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}, started))
			}
			if isActive {
				continue
			}
			if err := c.base.launchctl(ctx, "bootstrap", "gui/"+c.base.uid, agent.plist); err != nil {
				return errors.Join(err, c.base.stopMacAgents(ctx, Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}, started))
			}
			started = append(started, agent.label)
			if err := c.base.launchctl(ctx, "kickstart", "-k", "gui/"+c.base.uid+"/"+agent.label); err != nil {
				return errors.Join(err, c.base.stopMacAgents(ctx, Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}, started))
			}
		}
	} else {
		if active {
			return errors.New("canonical PRD service is already active")
		}
		result, err := c.base.runner.Run(ctx, "/usr/bin/sudo", "-n", "/usr/bin/systemctl", "start", plan.Namespace.RunnerUnit)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("start canonical PRD systemd service failed (exit %d)", result.ExitCode)
		}
	}
	if err := c.waitForState(ctx, plan, true); err != nil {
		return err
	}
	if plan.Namespace.Host == config.HostKindMac {
		for _, label := range []string{plan.Namespace.LocalAgentLabel, plan.Namespace.LocalDAgentLabel} {
			isActive, err := c.base.macAgentStatus(ctx, label)
			if err != nil {
				return err
			}
			if !isActive {
				return fmt.Errorf("canonical PRD LaunchAgent %s remained inactive", label)
			}
		}
	}
	return nil
}

func (c *PRDServiceController) validate(plan PRDUpgradePlan) error {
	if c == nil || c.base == nil || validatePRDPlan(plan) != nil {
		return ErrInvalidPRDUpgradePlan
	}
	current, err := user.Current()
	if err != nil || current.Username != plan.Namespace.Account {
		return ErrInvalidPRDUpgradePlan
	}
	want := simpleHostPlatforms[plan.HostID]
	if runtime.GOOS != want.goos || runtime.GOARCH != want.goarch {
		return ErrInvalidPRDUpgradePlan
	}
	return nil
}

func (c *PRDServiceController) waitForState(ctx context.Context, plan PRDUpgradePlan, wanted bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err := c.Status(ctx, plan)
		if err != nil {
			return err
		}
		if active == wanted {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("canonical PRD service did not reach requested state: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
