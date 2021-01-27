package targets

import (
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/grafana/loki/pkg/promtail/api"
	"github.com/grafana/loki/pkg/promtail/positions"
	"github.com/grafana/loki/pkg/promtail/scrapeconfig"
	"github.com/grafana/loki/pkg/promtail/targets/file"
	"github.com/grafana/loki/pkg/promtail/targets/gcplog"
	"github.com/grafana/loki/pkg/promtail/targets/journal"
	"github.com/grafana/loki/pkg/promtail/targets/lokipush"
	"github.com/grafana/loki/pkg/promtail/targets/stdin"
	"github.com/grafana/loki/pkg/promtail/targets/syslog"
	"github.com/grafana/loki/pkg/promtail/targets/target"
)

const (
	FileScrapeConfigs    = "fileScrapeConfigs"
	JournalScrapeConfigs = "journalScrapeConfigs"
	SyslogScrapeConfigs  = "syslogScrapeConfigs"
	GcplogScrapeConfigs  = "gcplogScrapeConfigs"
	PushScrapeConfigs    = "pushScrapeConfigs"
)

type targetManager interface {
	Ready() bool
	Stop()
	ActiveTargets() map[string][]target.Target
	AllTargets() map[string][]target.Target
}

// TargetManagers manages a list of target managers.
type TargetManagers struct {
	targetManagers []targetManager
	positions      positions.Positions
}

// NewTargetManagers makes a new TargetManagers
func NewTargetManagers(
	app stdin.Shutdownable,
	reg prometheus.Registerer,
	logger log.Logger,
	positionsConfig positions.Config,
	client api.EntryHandler,
	scrapeConfigs []scrapeconfig.Config,
	targetConfig *file.Config,
) (*TargetManagers, error) {
	var targetManagers []targetManager
	targetScrapeConfigs := make(map[string][]scrapeconfig.Config, 4)

	if targetConfig.Stdin {
		level.Debug(logger).Log("msg", "configured to read from stdin")
		stdin, err := stdin.NewStdinTargetManager(reg, logger, app, client, scrapeConfigs)
		if err != nil {
			return nil, err
		}
		targetManagers = append(targetManagers, stdin)
		return &TargetManagers{targetManagers: targetManagers}, nil
	}

	positions, err := positions.New(logger, positionsConfig)
	if err != nil {
		return nil, err
	}

	for _, cfg := range scrapeConfigs {
		switch {
		case cfg.HasServiceDiscoveryConfig():
			targetScrapeConfigs[FileScrapeConfigs] = append(targetScrapeConfigs[FileScrapeConfigs], cfg)
		case cfg.JournalConfig != nil:
			targetScrapeConfigs[JournalScrapeConfigs] = append(targetScrapeConfigs[JournalScrapeConfigs], cfg)
		case cfg.SyslogConfig != nil:
			targetScrapeConfigs[SyslogScrapeConfigs] = append(targetScrapeConfigs[SyslogScrapeConfigs], cfg)
		case cfg.GcplogConfig != nil:
			targetScrapeConfigs[GcplogScrapeConfigs] = append(targetScrapeConfigs[GcplogScrapeConfigs], cfg)
		case cfg.PushConfig != nil:
			targetScrapeConfigs[PushScrapeConfigs] = append(targetScrapeConfigs[PushScrapeConfigs], cfg)
		default:
			return nil, errors.New("unknown scrape config")
		}
	}

	var (
		fileMetrics   *file.Metrics
		syslogMetrics *syslog.Metrics
		gcplogMetrics *gcplog.Metrics
	)
	if len(targetScrapeConfigs[FileScrapeConfigs]) > 0 {
		fileMetrics = file.NewMetrics(reg)
	}
	if len(targetScrapeConfigs[SyslogScrapeConfigs]) > 0 {
		syslogMetrics = syslog.NewMetrics(reg)
	}
	if len(targetScrapeConfigs[GcplogScrapeConfigs]) > 0 {
		gcplogMetrics = gcplog.NewMetrics(reg)
	}

	for target, scrapeConfigs := range targetScrapeConfigs {
		switch target {
		case FileScrapeConfigs:
			fileTargetManager, err := file.NewFileTargetManager(
				fileMetrics,
				logger,
				positions,
				client,
				scrapeConfigs,
				targetConfig,
			)
			if err != nil {
				return nil, errors.Wrap(err, "failed to make file target manager")
			}
			targetManagers = append(targetManagers, fileTargetManager)
		case JournalScrapeConfigs:
			journalTargetManager, err := journal.NewJournalTargetManager(
				reg,
				logger,
				positions,
				client,
				scrapeConfigs,
			)
			if err != nil {
				return nil, errors.Wrap(err, "failed to make journal target manager")
			}
			targetManagers = append(targetManagers, journalTargetManager)
		case SyslogScrapeConfigs:
			syslogTargetManager, err := syslog.NewSyslogTargetManager(
				syslogMetrics,
				logger,
				client,
				scrapeConfigs,
			)
			if err != nil {
				return nil, errors.Wrap(err, "failed to make syslog target manager")
			}
			targetManagers = append(targetManagers, syslogTargetManager)
		case GcplogScrapeConfigs:
			pubsubTargetManager, err := gcplog.NewGcplogTargetManager(
				gcplogMetrics,
				logger,
				client,
				scrapeConfigs,
			)
			if err != nil {
				return nil, errors.Wrap(err, "failed to make syslog target manager")
			}
			targetManagers = append(targetManagers, pubsubTargetManager)
		case PushScrapeConfigs:
			pushTargetManager, err := lokipush.NewPushTargetManager(
				reg,
				logger,
				client,
				scrapeConfigs,
			)
			if err != nil {
				return nil, errors.Wrap(err, "failed to make Loki Push API target manager")
			}
			targetManagers = append(targetManagers, pushTargetManager)
		default:
			return nil, errors.New("unknown scrape config")
		}
	}

	return &TargetManagers{
		targetManagers: targetManagers,
		positions:      positions,
	}, nil

}

// ActiveTargets returns active targets per jobs
func (tm *TargetManagers) ActiveTargets() map[string][]target.Target {
	result := map[string][]target.Target{}
	for _, t := range tm.targetManagers {
		for job, targets := range t.ActiveTargets() {
			result[job] = append(result[job], targets...)
		}
	}
	return result
}

// AllTargets returns all targets per jobs
func (tm *TargetManagers) AllTargets() map[string][]target.Target {
	result := map[string][]target.Target{}
	for _, t := range tm.targetManagers {
		for job, targets := range t.AllTargets() {
			result[job] = append(result[job], targets...)
		}
	}
	return result
}

// Ready if there's at least one ready target manager.
func (tm *TargetManagers) Ready() bool {
	for _, t := range tm.targetManagers {
		if t.Ready() {
			return true
		}
	}
	return false
}

// Stop the TargetManagers.
func (tm *TargetManagers) Stop() {
	for _, t := range tm.targetManagers {
		t.Stop()
	}
	if tm.positions != nil {
		tm.positions.Stop()
	}
}
