package router

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
)

// Increment this when Render behavior changes independently of the template
// or proxy policy. It makes renderer upgrades explicit recovery inputs.
const renderSchemaVersion = 1

type configProjection struct {
	Config    []byte
	ConfigSHA string
	SourceSHA string
}

func (c *Controller) renderProjection(state State) (configProjection, error) {
	template, err := os.ReadFile(c.config.TemplatePath)
	if err != nil {
		return configProjection{}, err
	}
	config, err := Render(template, state, c.config.ProxyPolicy)
	if err != nil {
		return configProjection{}, err
	}
	return configProjection{
		Config:    config,
		ConfigSHA: hashBytes(config),
		SourceSHA: renderSourceSHA(template, c.config.ProxyPolicy),
	}, nil
}

func renderSourceSHA(template []byte, policy ProxyPolicy) string {
	policy = policy.withDefaults()
	var source bytes.Buffer
	fmt.Fprintf(
		&source,
		"gonka-router-renderer\x00%d\x00%d\x00%d\x00%d\x00%d\x00",
		renderSchemaVersion,
		policy.MaxBodyBytes,
		durationSeconds(policy.ConnectTimeout),
		durationSeconds(policy.StreamIdleTimeout),
		policy.UpstreamKeepalive,
	)
	source.Write(template)
	return hashBytes(source.Bytes())
}

func validateCommittedProjection(journal operationJournal) error {
	if len(journal.NewConfig) == 0 {
		return errors.New("desired config is empty")
	}
	if got := hashBytes(journal.NewConfig); got != journal.NewConfigSHA {
		return fmt.Errorf(
			"journal config sha256 is %s, want %s",
			got,
			journal.NewConfigSHA,
		)
	}
	if journal.RenderRevision == 0 {
		return errors.New("render revision is zero")
	}
	if journal.RenderSourceSHA == "" {
		return errors.New("render source sha256 is empty")
	}
	return nil
}

func (c *Controller) refreshCommittedProjection(
	journal *operationJournal,
) error {
	mutable, err := mutableProjectionPhase(journal.Phase)
	if err != nil {
		return err
	}
	if !mutable {
		return nil
	}
	current, err := c.renderProjection(journal.NewState)
	if err != nil {
		return err
	}
	if current.SourceSHA == journal.RenderSourceSHA {
		if current.ConfigSHA != journal.NewConfigSHA ||
			!bytes.Equal(current.Config, journal.NewConfig) {
			return errors.New(
				"renderer output changed without a render schema version bump",
			)
		}
		return nil
	}
	if journal.RenderRevision == math.MaxUint64 {
		return errors.New("render revision exhausted")
	}
	oldRevision := journal.RenderRevision
	oldSourceSHA := journal.RenderSourceSHA
	oldConfigSHA := journal.NewConfigSHA
	journal.RenderRevision++
	journal.RenderSourceSHA = current.SourceSHA
	journal.NewConfig = current.Config
	journal.NewConfigSHA = current.ConfigSHA
	if journal.Audit.OperationID == "" {
		journal.Audit = auditRecord(
			mutationFromJournal(*journal),
			journal.NewState.Generation,
			current.ConfigSHA,
		)
	} else {
		journal.Audit.EventID = auditEventID(journal.Audit, current.ConfigSHA)
	}
	if err := writeJSONAtomic(c.config.JournalPath, *journal, 0o600); err != nil {
		return err
	}
	slog.Info(
		"router config projection refreshed",
		"operation_id", journal.OperationID,
		"old_revision", oldRevision,
		"new_revision", journal.RenderRevision,
		"old_render_source_sha256", oldSourceSHA,
		"new_render_source_sha256", journal.RenderSourceSHA,
		"old_config_sha256", oldConfigSHA,
		"new_config_sha256", journal.NewConfigSHA,
	)
	return nil
}

func mutableProjectionPhase(phase string) (bool, error) {
	switch phase {
	case operationPhaseIntentCommitted, operationPhaseDesiredPersisted:
		return true, nil
	case operationPhaseApplied, operationPhaseAuditEnqueued:
		return false, nil
	default:
		return false, fmt.Errorf("unknown roll-forward phase %q", phase)
	}
}

func mutationFromJournal(journal operationJournal) mutation {
	return mutation{
		OperationID:  journal.OperationID,
		Action:       journal.Action,
		Host:         journal.Host,
		MembershipID: journal.MembershipID,
		From:         journal.From,
		To:           journal.To,
		Target:       journal.Target,
		Result:       journal.Result,
	}
}
