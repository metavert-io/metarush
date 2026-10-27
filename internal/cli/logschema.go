package cli

import (
	"reflect"

	"github.com/metavert-io/metarush/internal/improve"
	"github.com/metavert-io/metarush/internal/logbook"
)

// The activity log's and the improvement pipeline's types get distinct names
// in the generated schema and TypeScript (several are generic words: Entry,
// Item, Status, Target).
func init() {
	for t, name := range map[reflect.Type]string{
		reflect.TypeOf(logbook.Entry{}):         "LogEntry",
		reflect.TypeOf(logbook.Refs{}):          "LogRefs",
		reflect.TypeOf(logbook.EntryError{}):    "LogError",
		reflect.TypeOf(logbook.OpInfo{}):        "LogOpInfo",
		reflect.TypeOf(logbook.OutputInfo{}):    "LogOutputInfo",
		reflect.TypeOf(logbook.CountInfo{}):     "LogCountInfo",
		reflect.TypeOf(logbook.JobInfo{}):       "LogJobInfo",
		reflect.TypeOf(logbook.ToolInfo{}):      "LogToolInfo",
		reflect.TypeOf(logbook.ClaudeInfo{}):    "LogClaudeInfo",
		reflect.TypeOf(logbook.ClaudeTool{}):    "LogClaudeTool",
		reflect.TypeOf(logbook.ActionInfo{}):    "LogActionInfo",
		reflect.TypeOf(logbook.EventInfo{}):     "LogEventInfo",
		reflect.TypeOf(logbook.BodyRef{}):       "LogBodyRef",
		reflect.TypeOf(logbook.Status{}):        "LogStatus",
		reflect.TypeOf(logbook.SpoolStatus{}):   "LogSpoolStatus",
		reflect.TypeOf(logbook.Target{}):        "LogTarget",
		reflect.TypeOf(logbook.PurgeResult{}):   "LogPurgeResult",
		reflect.TypeOf(logbook.HookStatus{}):    "ClaudeHookStatus",
		reflect.TypeOf(logbook.HookChange{}):    "ClaudeHookChange",
		reflect.TypeOf(improve.Item{}):          "ImprovementItem",
		reflect.TypeOf(improve.Report{}):        "ImprovementReport",
		reflect.TypeOf(improve.ReportSummary{}): "ImprovementReportSummary",
		reflect.TypeOf(improve.Signal{}):        "ImprovementSignal",
		reflect.TypeOf(improve.Example{}):       "ImprovementSignalExample",
		reflect.TypeOf(improve.StateCounts{}):   "ImprovementCounts",
		reflect.TypeOf(improve.SignalsIn{}):     "ImproveSignalsIn",
		reflect.TypeOf(improve.SignalsOut{}):    "ImproveSignalsOut",
		reflect.TypeOf(improve.SaveIn{}):        "ImproveSaveIn",
		reflect.TypeOf(improve.SaveOut{}):       "ImproveSaveOut",
		reflect.TypeOf(improve.ListIn{}):        "ImproveListIn",
		reflect.TypeOf(improve.ListOut{}):       "ImproveListOut",
		reflect.TypeOf(improve.ShowIn{}):        "ImproveShowIn",
		reflect.TypeOf(improve.ShowOut{}):       "ImproveShowOut",
		reflect.TypeOf(improve.DecideIn{}):      "ImproveDecideIn",
		reflect.TypeOf(improve.DecideOut{}):     "ImproveDecideOut",
		reflect.TypeOf(improve.ExportIn{}):      "ImproveExportIn",
		reflect.TypeOf(improve.ExportOut{}):     "ImproveExportOut",
	} {
		opsSchemaNames[t] = name
	}
}
