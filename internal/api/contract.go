package api

import (
	"reflect"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/recorder"
	"github.com/metavert-io/metarush/internal/uploads"
)

// ContractType is one HTTP payload outside the op registry.
type ContractType struct {
	Name  string // property name in api.schema.json
	Value any
}

// ContractTypes are the dashboard payloads that are not op inputs/outputs: the
// daemon status, the session, uploads, SSE payloads, waveform peaks, the stored
// technical evidence and the assistant endpoints. `metarush schema export`
// writes them to schema/api.schema.json, which `npm run gen:types` turns into
// schema/ts/api.ts, so the dashboard never hand-mirrors a Go struct.
func ContractTypes() []ContractType {
	return []ContractType{
		{"error", apperr.Error{}},
		{"status", Status{}},
		{"session", Session{}},
		{"authState", AuthState{}},
		{"signInRequest", SignInRequest{}},
		{"signInResult", SignInResult{}},
		{"linkInfo", LinkInfo{}},
		{"linkAccept", LinkAccept{}},
		{"opInfo", OpInfo{}},
		{"upload", uploads.Record{}},
		{"uploadList", UploadList{}},
		{"uploadCreateIn", UploadCreateIn{}},
		{"serverEvent", Event{}},
		{"eventsHello", EventsHello{}},
		{"peaks", derive.PeaksFile{}},
		{"technicalEvidence", evidence.Stored{}},
		{"assistantStatus", assistant.AssistantStatus{}},
		{"assistantEvent", assistant.AssistantEvent{}},
		{"assistantPartial", assistant.AssistantPartial{}},
		{"assistantConversation", assistant.Conversation{}},
		{"renderRequest", assistant.RenderRequest{}},
		{"extPairIn", ExtPairIn{}},
		{"extPairOut", ExtPairOut{}},
		{"extHelloOut", ExtHelloOut{}},
		{"extUploadIn", ExtUploadIn{}},
		{"extUploadOut", ExtUploadOut{}},
		{"extImportIn", ExtImportIn{}},
		{"extImportOut", ExtImportOut{}},
		{"extPageIn", ExtPageIn{}},
		{"extPageOut", ExtPageOut{}},
		{"sendToEditorIn", SendToEditorIn{}},
		{"sendToEditorOut", SendToEditorOut{}},
		{"exportRevealIn", ExportRevealIn{}},
		{"folderChooseIn", FolderChooseIn{}},
		{"folderChooseOut", FolderChooseOut{}},
		{"storageMovePlanIn", StorageMovePlanIn{}},
		{"storageMovePlanOut", StorageMovePlanOut{}},
		{"storageMoveStartIn", StorageMoveStartIn{}},
		{"storageMoveStartOut", StorageMoveStartOut{}},
		{"storageChooseOut", StorageChooseOut{}},
		{"recordingCreateIn", recorder.RecordingCreateIn{}},
		{"recordingClockIn", recorder.RecordingClockIn{}},
		{"recordingPieceOut", recorder.RecordingPieceOut{}},
		{"recording", models.Recording{}},
	}
}

// ContractNames gives Go types whose names are ambiguous in TypeScript a clearer
// generated name.
var ContractNames = map[reflect.Type]string{
	reflect.TypeOf(apperr.Error{}):            "ApiErrorBody",
	reflect.TypeOf(Event{}):                   "ServerEvent",
	reflect.TypeOf(derive.PeaksFile{}):        "Peaks",
	reflect.TypeOf(evidence.Stored{}):         "TechnicalEvidence",
	reflect.TypeOf(evidence.StoredLoudness{}): "TechnicalLoudness",
	reflect.TypeOf(evidence.Span{}):           "EvidenceSpan",
	reflect.TypeOf(evidence.Scene{}):          "EvidenceScene",
	reflect.TypeOf(ingest.Result{}):           "IngestResult",
	reflect.TypeOf(ingest.WatchStatus{}):      "WatchStatus",
	reflect.TypeOf(uploads.Record{}):          "UploadRecord",
	reflect.TypeOf(costs.Estimate{}):          "CostEstimate", // on Claude's priced cards (D208), as in the ops
	reflect.TypeOf(costs.Part{}):              "CostPart",
}
