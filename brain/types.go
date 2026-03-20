/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"time"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/tools"
)

// BrainState represents the current state of a brain worker.
type BrainState string

const (
	StateIdle       BrainState = "idle"
	StateBuilding   BrainState = "building"
	StateMonitoring BrainState = "monitoring"
	StatePaused     BrainState = "paused"
	StateError      BrainState = "error"
)

// PipelineStage represents a stage in the deterministic build pipeline.
type PipelineStage string

const (
	StagePlan       PipelineStage = "PLAN"
	StageBuild      PipelineStage = "BUILD"
	StageValidate   PipelineStage = "VALIDATE"
	StageComplete   PipelineStage = "COMPLETE"
	StageMonitoring PipelineStage = "MONITORING"
	StageUpdatePlan PipelineStage = "UPDATE_PLAN"
)

// Pause reasons — stored in ho_pipeline_state.pause_reason.
const (
	PauseReasonOwnerAnswers = "awaiting_owner_answers"
	PauseReasonApproval     = "awaiting_approval"
)

// BrainCommand is a message sent to a brain worker to trigger an action.
type BrainCommand struct {
	Type    string                 `json:"type"`
	SiteID  int                    `json:"site_id"`
	Payload map[string]interface{} `json:"payload,omitempty"`
}

// Command type constants.
const (
	CommandWake          = "wake"
	CommandChat          = "chat"
	CommandModeChange    = "mode_change"
	CommandShutdown      = "shutdown"
	CommandScheduledTask = "scheduled_task"
	CommandUpdate        = "update" // trigger incremental update
)

// Deps holds shared dependencies injected into pipeline workers.
type Deps struct {
	DB              *db.DB
	SiteDBManager   *db.SiteDBManager
	Encryptor       *security.Encryptor
	LLMRegistry     *llm.Registry
	ToolRegistry    *tools.Registry
	ToolExecutor    *tools.Executor
	Bus             *events.Bus
	ProviderFactory llm.ProviderFactory

	// Monitoring interval overrides (zero means use built-in defaults).
	MonitoringBase time.Duration
	MonitoringMax  time.Duration

	// PublicPort is the port the public-facing HTTP server listens on.
	// Used by functional validation to make requests to built pages.
	PublicPort int
}
