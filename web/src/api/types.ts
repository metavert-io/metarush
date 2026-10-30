// Every op input/output and model type comes from @metarush/schema, which is
// generated from the Go structs (schema/gen-types.mjs). Never hand-write those.
import type { OpMap, RenderPlan, api, ops } from '@metarush/schema';

export type { OpMap, RenderPlan };

export type ProjectSummary = ops.ProjectSummary;
export type Project = ops.Project;
export type ProjectShowOut = ops.ProjectShowOut;
export type Brief = ops.Brief;
export type AssetSummary = ops.AssetSummary;
export type Asset = ops.Asset;
export type AssetShowOut = ops.AssetShowOut;
export type RevisionSummary = ops.RevisionSummary;
// Drafts from AI apps and the review desk (D248–D250).
export type DraftView = ops.DraftView;
export type DraftReviewCopy = ops.DraftReviewCopy;
export type Packet = ops.Packet;
export type ReviewWaitingRow = ops.ReviewWaitingRow;
export type RevisionShowOut = ops.RevisionShowOut;
export type RevisionCompareOut = ops.RevisionCompareOut;
export type Diff = ops.Diff;
export type FieldDiff = ops.FieldDiff;
export type Spec = ops.Spec;
export type Clip = ops.Clip;
export type Overlay = ops.Overlay;
export type AudioTrack = ops.AudioTrack;
export type Captions = ops.Captions;
export type Evidence = ops.Evidence;
export type EditOp = ops.Op;
export type Issue = ops.Issue;
export type Job = ops.Job;
export type Render = ops.Render;
export type RenderOut = ops.RenderOut;
export type WorkerRow = ops.WorkerRow;
export type Comment = ops.Comment;
export type TranscriptOut = ops.TranscriptOut;
export type TranscriptSegment = ops.TranscriptSegment;
export type Word = ops.Word;
export type EvidenceOut = ops.EvidenceOut;
export type Insight = ops.Insight;
export type MomentsOut = ops.MomentsOut;
export type Moment = ops.Moment;
export type Actor = ops.Actor;
export type Approval = ops.Approval;
export type WorkspaceShowOut = ops.WorkspaceShowOut;
export type Installation = ops.Installation;
export type IngestSession = ops.IngestSession;
export type IngestResult = ops.Result;
export type MediaInfo = ops.MediaInfo;
export type DuplicateOf = ops.DuplicateOf;

/* Activity (activity.summary): what is running, waiting and why. */
export type ActivitySummary = ops.Summary;
export type ActivityItem = ops.Item;
export type ActivityStep = ops.Step;
export type WaitGroup = ops.WaitGroup;
export type WaitReason = ops.WaitReason;
export type WorkerBrief = ops.WorkerBrief;
export type WorkerSettings = ops.WorkerSettingsOut;

/* Notifications (notification.list / notification.read), with open questions about the clip. */
export type Notification = ops.NotificationRow;
/* "2 questions about this clip" and where to answer them (job details, activity, bell). */
export type OpenQuestions = ops.OpenQuestions;

/* Duplicate precheck before uploading (asset.precheck). */
export type PrecheckIn = ops.PrecheckIn;
export type PrecheckOut = ops.PrecheckOut;
export type PrecheckResult = ops.PrecheckResult;

/* Search (search op). */
export type SearchOut = ops.SearchOut;
export type SearchMoment = ops.SearchMoment;
export type SnippetPart = ops.SnippetPart;

/* Link imports (ingest.url*): videos from YouTube, TikTok and X, and where a clip came from. */
export type ImportIn = ops.ImportIn;
export type ImportOut = ops.ImportOut;
export type ImportRow = ops.ImportRow;
export type ImportTools = ops.ImportTools;
export type AssetSource = ops.AssetSource;

/* The "Send to MetaRush" browser extension (extension.*). */
export type ExtensionStatus = ops.ExtensionStatusOut;
export type ExtensionBrowser = ops.Browser;
/* Everyone's browsers, with whose each is (owners, D197). */
export type PairedBrowser = ops.PairedBrowser;
export type ExtensionPairOut = ops.ExtensionPairOut;

/* Deleted (archived) projects and clearing them out for good (D196). */
export type ProjectArchiveOut = ops.ProjectArchiveOut;
export type ProjectPurgeOut = ops.ProjectPurgeOut;
export type ProjectPurgeRow = ops.ProjectPurgeRow;

/* Watched folder (watch.*). */
export type WatchStatusOut = ops.WatchStatusOut;
export type BrowseOut = ops.BrowseOut;
export type BrowseEntry = ops.BrowseEntry;

/* Cut sheets (internal/cutsheet): the Markdown form of a cut. */
export type CutShowOut = ops.CutShowOut;
export type CutCheckOut = ops.CutCheckOut;
export type CutApplyOut = ops.CutApplyOut;
export type CutCompareOut = ops.CutCompareOut;
export type CutProblem = ops.Problem;
export type CutDiffLine = ops.DiffLine;

/* Speakers, people and to-dos */
export type SpeakerRow = ops.SpeakerRow;
export type SpeakersOut = ops.SpeakersOut;
export type SpeakersStatus = ops.SpeakersStatus;
export type TimeSpan = ops.TimeSpan;
export type PersonSummary = ops.PersonSummary;
export type Appearance = ops.Appearance;
export type Todo = ops.Todo;
export type TodoSuggestion = ops.TodoSuggestion;
export type TodoCounts = ops.TodoCounts;
export type TodoKind = ops.Todo['kind'];
export type PersonSample = ops.PersonSample;
export type PersonInClip = ops.PersonInClip;
export type ClipVoice = ops.ClipVoice;
export type AutoMerge = ops.AutoMerge;
export type SpeakerRenameOut = ops.SpeakerRenameOut;
export type SpeakerAssignOut = ops.SpeakerAssignOut;
export type SpeakerDetectOut = ops.SpeakerDetectOut;
export type PersonCompareOut = ops.PersonCompareOut;
export type PersonRebuildOut = ops.PersonRebuildOut;
/* One question per person on screen (D155–D157) and what a name was learned from (D158). */
export type TodoFace = ops.TodoFace;
export type TodoEvidence = ops.TodoEvidence;
export type TodoGroupsOut = ops.TodoGroupsOut;
export type ShownClip = ops.ShownClip;
export type PersonLearnedOut = ops.PersonLearnedOut;
export type LearnedVoice = ops.LearnedVoice;
export type LearnedFace = ops.LearnedFace;

/* Integrations (API-key services such as ElevenLabs) and narration voices. */
export type ProviderName = ops.NameIn['name'];
export type ProviderStatus = ops.Status;
export type ProviderStatusOut = ops.StatusOut;
export type ProviderSettings = ops.ProviderSettings;
export type ProviderSettingsIn = ops.SettingsIn;
export type ProviderUsage = ops.Usage;
export type ResolvedVoice = ops.ResolvedVoice;
export type VoiceSettings = ops.TTSVoiceSettings;
export type BudgetStatus = ops.BudgetStatus;
export type VoiceView = ops.VoiceView;
export type VoiceModel = ops.Model;

/* The Voiceover tab (voiceover.*, mix.*): narration lines, takes, the AI voice and the mixer. */
export type NarrationOut = ops.ShowOut;
export type NarrationLine = ops.BeatView;
export type LineFit = ops.BeatFit;
export type LineFix = ops.FitFix;
export type LineIn = ops.LineIn;
export type Anchor = ops.Anchor;
export type TakeView = ops.TakeView;
export type PlanResult = ops.PlanResult;
export type AppliedChange = ops.Applied;
export type MixOut = ops.MixOut;
export type MixBus = ops.BusView;
export type MixSetIn = ops.MixSetIn;
export type MixBusChange = ops.BusChange;
export type MixPreviewOut = ops.MixPreviewOut;
export type VoiceEstimate = ops.EstimateOut;
export type AlignOut = ops.AlignOut;
/* Live takes (D85) and script versions (D86). */
export type PlaceOut = ops.PlaceOut;
export type PlacedLine = ops.PlacedLine;
export type ReadView = ops.ReadView;
export type ScriptVersions = ops.ScriptView;
export type FittedOut = ops.FittedOut;
export type FitLineView = ops.FitLineView;
export type WordOp = ops.WordOp;

/* The Sound tab (sound.*): levels per part and person, clearer voices, music. */
export type SoundShowOut = ops.SoundShowOut;
export type SoundPart = ops.PartView;
export type SoundPerson = ops.PersonView;
export type SoundEnhanceView = ops.EnhanceView;
export type SoundMusicView = ops.MusicView;
export type SoundModeView = ops.ModeView;
export type SoundLevelsIn = ops.LevelsIn;
export type SoundPreviewOut = ops.SoundPreviewOut;
export type SoundEnhanceIn = ops.EnhanceIn;
export type SoundEnhanceOut = ops.EnhanceOut;
export type SoundScore = ops.PersonScore;
export type SoundMusicOut = ops.MusicOut;
export type SoundSetupOut = ops.SetupOut;
export type SoundComponent = ops.ComponentStatus;

export type OpName = keyof OpMap;
/** Ops that need this machine's filesystem; the server rejects them over HTTP. */
export type LocalOnlyOp =
  | 'ingest.files'
  | 'footage.frames'
  | 'footage.sheet'
  | 'render.fetch'
  | 'watch.choose'
  | 'watch.reveal'
  | 'footage.speakers.samples'
  | 'assistant.apply'
  | 'voiceover.preview'
  | 'project.checkout'
  | 'project.status'
  | 'project.pull'
  | 'project.push'
  | 'log.hooks.enable'
  | 'log.hooks.disable'
  | 'motion.new'
  | 'resources.path'
  | 'resources.add'
  | 'resources.link'
  | 'resources.folder'
  | 'add.paths'
  | 'conversation.import'
  | 'remotion.stills'
  | 'remotion.checkout'
  | 'auth.reset'
  // Recipe files between workspaces (D219): /api/recipes/export and /api/recipes/import.
  | 'recipe.export'
  | 'recipe.import'
  // Abilities (D233): a check reads a folder on this computer.
  | 'ability.check'
  // Ability files between workspaces (D292): /api/abilities/export and /api/abilities/import.
  | 'ability.export'
  | 'ability.import'
  // A handover's file and passphrase are a person's, at a terminal (D348).
  | 'machine.handover.export'
  | 'machine.handover.receive'
  | 'machine.handover.clean'
  | 'machine.takeover';
/* Abilities (D230–D235): versions an owner approves, set up per computer, run in the sandbox. */
export type AbilityRow = ops.AbilityRow;
export type AbilityListOut = ops.AbilityListOut;
export type AbilityShowOut = ops.AbilityShowOut;
export type AbilityFileLook = ops.AbilityFileLook;
export type HereLocal = ops.HereLocal;
export type AbilityVersion = ops.AbilityVersion;
export type AbilityVersionRow = ops.AbilityVersionRow;
export type AbilityPage = ops.AbilityPage;
export type AbilityHere = ops.AbilityHere;
export type AbilityInput = ops.AbilityInput;
export type AbilityPlanOut = ops.AbilityPlanOut;
export type AbilityRunOut = ops.AbilityRunOut;
export type AbilityRunRow = ops.AbilityRunRow;
export type AbilityRunsOut = ops.AbilityRunsOut;
export type AbilityRequest = ops.AbilityRequest;
export type AbilityImportOut = ops.AbilityImportOut;
/* What may leave (D288–D291): a client's rules, and what of a project left. */
export type LeaveOut = ops.LeaveOut;
export type LeavePlaceOut = ops.LeavePlaceOut;
export type LeaveRules = ops.LeaveRules;
export type ReceiptLeft = ops.ReceiptLeft;
export type ProjectLeavesOut = ops.ProjectLeavesOut;
/* Resources (D79–D84): the library, linked folders, the brand kit, project links, where things are used. */
export type ResourceRow = ops.ResourceRow;
export type ResourceShowOut = ops.ResourceShowOut;
export type ResourceListOut = ops.ResourceListOut;
export type ResourceTag = ops.ResourceTag;
/* Pictures a repository keeps from its folders (D216). */
export type RepoPictures = ops.RepoPictures;
export type RepoPictureRow = ops.RepoPictureRow;
export type RepoPicturesOut = ops.RepoPicturesOut;
export type UseRow = ops.UseRow;
export type AssetWhereOut = ops.AssetWhereOut;
export type AssetDeleteOut = ops.AssetDeleteOut;
export type LinkedFolderRow = ops.LinkedFolderRow;
export type LinkedFolderOut = ops.LinkedFolderOut;
export type ProjectLinkRow = ops.ProjectLinkRow;
export type ProjectLinksOut = ops.ProjectLinksOut;
export type BrandShowOut = ops.BrandShowOut;
export type BrandCutOffer = ops.BrandCutOffer;
export type ContentShowOut = ops.ContentShowOut;
export type ContentSettings = ops.ContentSettings;
export type BrandKit = ops.BrandKit;
/* Logo files, house style and house sign-offs (D198–D200): Content Settings parts. */
export type BrandLogoOut = ops.BrandLogoOut;
export type HouseStyle = ops.HouseStyle;
export type StyleCard = ops.StyleCard;
export type ContentStyleOut = ops.ContentStyleOut;
export type StyleCutOffer = ops.StyleCutOffer;
// Layout packs (D263–D267).
export type ContentPackOut = ops.ContentPackOut;
export type PackRoleOut = ops.PackRoleOut;
export type PackDraftOut = ops.PackDraftOut;
export type PackCutOffer = ops.PackCutOffer;
export type PackPreview = ops.PackPreview;
export type ContentPackPreviewOut = ops.ContentPackPreviewOut;
export type HouseSignoffsOut = ops.HouseSignoffsOut;
export type LinkOutcome = ops.LinkOutcome;
export type AddLinksOut = ops.AddLinksOut;
/* /api/resources/folders/choose (the native folder picker, then the LocalOnly resources.link). */
export type FolderChooseIn = api.FolderChooseIn;
export type FolderChooseOut = api.FolderChooseOut;
/* The dashboard reaches the two local folder actions through /api/watch/{choose,reveal}. */
export type WatchChooseOut = ops.WatchChooseOut;
export type WatchRevealOut = ops.WatchRevealOut;
/* "Send to editing app": /api/export/timeline runs export.timeline into a folder MetaRush chooses (D92). */
export type SendToEditorIn = api.SendToEditorIn;
export type SendToEditorOut = api.SendToEditorOut;
export type ExportTimelineOut = ops.ExportTimelineOut;
/* Moving the media from Settings → Storage (D112): /api/storage/{choose,plan,move}, then storage.status. */
export type StorageTarget = ops.StorageTarget;
export type StoragePlanOut = ops.StoragePlanOut;
export type StorageStatusOut = ops.StorageStatusOut;
export type StorageMovePlanIn = api.StorageMovePlanIn;
export type StorageMovePlanOut = api.StorageMovePlanOut;
export type StorageMoveStartIn = api.StorageMoveStartIn;
export type StorageMoveStartOut = api.StorageMoveStartOut;
export type StorageChooseOut = api.StorageChooseOut;
/* Moving the database from Settings → Storage (D338): /api/database/{plan,move,switch}, then database.move.status. */
export type DatabaseMoveForm = api.DatabaseMoveForm;
export type DatabasePlanOut = ops.DatabasePlanOut;
export type DatabasePlace = ops.DatabasePlace;
export type DatabaseMoveStatusOut = ops.DatabaseMoveStatusOut;
export type DatabaseSwitchOut = ops.DatabaseSwitchOut;
export type DatabaseAbandonOut = ops.DatabaseAbandonOut;
export type DatabaseMovePlanIn = api.DatabaseMovePlanIn;
export type DatabaseMovePlanOut = api.DatabaseMovePlanOut;
export type DatabaseMoveStartIn = api.DatabaseMoveStartIn;
export type DatabaseMoveStartOut = api.DatabaseMoveStartOut;
export type DatabaseSwitchFormIn = api.DatabaseSwitchFormIn;
/* Something written into the old database after a move (D352–D358): database.late.writes|show|carry|leave. */
export type LateLookView = ops.LateLookView;
export type LateWriteView = ops.LateWriteView;
export type DatabaseLateWritesOut = ops.DatabaseLateWritesOut;
export type DatabaseLateShowOut = ops.DatabaseLateShowOut;
export type DatabaseLateCarryOut = ops.DatabaseLateCarryOut;
export type DatabaseLateLeaveOut = ops.DatabaseLateLeaveOut;
export type JobShowOut = ops.JobShowOut;
export type JobTarget = ops.JobTarget;
export type PreviewPrepareOut = ops.PreviewPrepareOut;
/* Conversations from online calls (D99–D103). */
export type CallInfo = ops.CallInfo;
export type CallMemberInfo = ops.CallMemberInfo;
export type CallPlanOut = ops.CallPlanOut;
export type CallCleanupOut = ops.CallCleanupOut;
export type CleanupInfo = ops.CleanupInfo;
export type CallMatchOut = ops.CallMatchOut;
export type CallMoment = ops.CallMoment;
export type CallDetectOut = ops.CallDetectOut;
export type CallStyle = ops.CallStyle;
export type CallLayout = ops.CallLayout;
export type ConversationPin = ops.ConversationPin;
export type Shot = ops.Shot;
export type RemoteOp = Exclude<OpName, LocalOnlyOp>;
export type OpInput<K extends OpName> = OpMap[K]['input'];
export type OpOutput<K extends OpName> = OpMap[K]['output'];

/*
 * Endpoints outside the op registry, generated from internal/api ContractTypes
 * (schema/api.schema.json → schema/ts/api.ts). Nothing here mirrors Go by hand.
 */
export type ApiErrorBody = api.ApiErrorBody;
export type WatchStatus = api.WatchStatus;
/** GET /api/status */
export type StatusOut = api.Status;
/** GET /api/session */
export type SessionInfo = api.Session;
/* Signing in (D183): /api/auth/state, /api/auth/login, /api/auth/link/{token}; they work without a session. */
export type AuthState = api.AuthState;
export type AuthMethod = api.AuthState['method'];
export type SignInRequest = api.SignInRequest;
export type SignInResult = api.SignInResult;
export type LinkInfo = api.LinkInfo;
export type LinkAccept = api.LinkAccept;
/* Settings → People (auth.*, users.*, account.*): the sign-in method, people and your own account. */
export type Account = ops.Account;
export type AuthStatusOut = ops.AuthStatusOut;
export type AuthMethodIn = ops.AuthMethodIn;
export type UsersListOut = ops.UsersListOut;
export type UserLinkOut = ops.UserLinkOut;
export type UserOut = ops.UserOut;
export type UsersInviteIn = ops.UsersInviteIn;
/** GET /api/ops */
export type OpInfo = api.OpInfo;
/** /api/uploads/{id} */
export type UploadRecord = api.UploadRecord;
/** GET /api/uploads */
export type UploadList = api.UploadList;
// Screen recordings made in the dashboard (D258–D260).
export type RecordingCreateIn = api.RecordingCreateIn;
export type RecordingClockIn = api.RecordingClockIn;
export type RecordingPieceOut = api.RecordingPieceOut;
export type RecordingRecord = api.Recording;
export type RecordingInfo = ops.RecordingInfo;
export type RecordingScript = api.RecordingScript;
/** /media/assets/{id}/peaks */
export type Peaks = api.Peaks;
/** Insight.data for kind "technical" (internal/evidence Stored). */
export type TechnicalEvidence = api.TechnicalEvidence;
export type Span = api.EvidenceSpan;
/** Server-Sent Event payloads from /api/events. */
export type ServerEvent = api.ServerEvent;
export type EventsHello = api.EventsHello;

/* Claude Code panel (local Claude Code conversations): /api/assistant/* and the assistant.* ops */
export type AssistantStatus = api.AssistantStatus;
export type AssistantEvent = ops.AssistantEvent;
export type AssistantPartial = api.AssistantPartial;
export type Conversation = ops.Conversation;
export type ConversationTurn = ops.Turn;
export type QueuedMessage = ops.QueuedMessage;
export type AssistantAction = ops.AssistantAction;
/* Project motion graphics (D68): versions checked in this workspace and what people decided. */
export type MotionRow = ops.MotionRow;
export type MotionListOut = ops.MotionListOut;
export type MotionShowOut = ops.MotionShowOut;
export type MotionSourceFile = ops.MotionSourceFile;
export type MotionField = ops.MotionField;
export type MotionProblem = ops.MotionProblem;
export type MotionImport = ops.MotionImport;
export type MotionComponent = ops.MotionComponent;
export type AssistantToolCall = ops.ToolCall;
export type AssistantImage = ops.ImageRef;
export type TodoItem = ops.TodoItem;
export type TurnResult = ops.TurnResult;
export type TurnContext = ops.TurnContext;
export type AssistantMessageIn = ops.AssistantAskIn;
export type ConversationListIn = ops.ConversationListIn;
export type ConversationProposals = ops.ConversationProposals;
export type TurnProposals = ops.TurnProposals;
export type PlanProposal = ops.PlanProposal;
export type RenderProposal = ops.RenderProposal;
export type AssistantModel = ops.AssistantModel;
export type AssistantModelsOut = ops.AssistantModelsOut;
export type AssistantSettingsOut = ops.AssistantSettingsOut;
export type AssistantSuggestion = ops.AssistantSuggestion;
export type EditCheckOut = ops.EditCheckOut;
export type EditApplyOut = ops.EditApplyOut;

/* The activity log (log.*) and the improvement pipeline (improve.*). */
export type LogRow = ops.LogRow;
export type LogListIn = ops.LogListIn;
export type LogListOut = ops.LogListOut;
export type LogShowOut = ops.LogShowOut;
export type LogSettingsOut = ops.LogSettingsOut;
export type LogClaudeTool = ops.LogClaudeTool;
export type LogActionInfo = ops.LogActionInfo;
export type ImprovementItem = ops.ImprovementItem;
export type ImprovementReport = ops.ImprovementReport;
export type ImprovementReportSummary = ops.ImprovementReportSummary;
export type ImproveListOut = ops.ImproveListOut;

/* The Library (D96–D98): the Inbox, collections, the Archive and the Trash. */
export type LibraryIn = ops.LibraryIn;
export type LibraryOut = ops.LibraryOut;
export type LibraryItem = ops.LibraryItem;
export type LibraryCounts = ops.LibraryCounts;
export type LibraryRef = ops.LibraryRef;
export type LibraryActionOut = ops.LibraryActionOut;
export type LibraryActionRow = ops.LibraryActionRow;
export type CollectionRow = ops.CollectionRow;
export type TrashListOut = ops.TrashListOut;
export type TrashRow = ops.TrashRow;
export type TrashEmptyOut = ops.TrashEmptyOut;
export type TrashSettingsOut = ops.TrashSettingsOut;

/* Words changed in the speaker's own voice copy (D308–D312). */
export type SayIn = ops.SayIn;
export type SayEstimate = ops.SayEstimate;
export type SayMakeOut = ops.SayMakeOut;
export type SpokenView = ops.SpokenView;
export type CutSayPlaceOut = ops.CutSayPlaceOut;
export type TranscriptSaid = ops.TranscriptSaid;
export type SaidSound = ops.SaidSound;

/* Estimates of what outside AI services cost (D208) and Settings → Preferences. */
export type CostEstimate = ops.CostEstimate;
export type CostPart = ops.CostPart;
export type PreferencesOut = ops.PreferencesOut;
export type PreferencesSetIn = ops.PreferencesSetIn;

/* Generated video (D123–D127): shots made with Google Gemini, their records, looks and consent. */
export type GenShotIn = ops.GenShotIn;
export type GenEstimate = ops.GenEstimate;
export type GenRunOut = ops.GenRunOut;
export type GenView = ops.GenView;
// AI services (D224–D228): pictures, speech, music, dubs… and voice copies.
export type AIEstimate = ops.AIEstimate;
export type AIMakeIn = ops.AIMakeIn;
export type AIMakeOut = ops.AIMakeOut;
export type AIWorkView = ops.AIWorkView;
export type AIModelView = ops.AIModelView;
export type AIModelsOut = ops.AIModelsOut;
export type AIVoiceCopyIn = ops.AIVoiceCopyIn;
export type AIRulesOut = ops.AIRulesOut;
export type AIRulesSetIn = ops.AIRulesSetIn;
export type AIServiceRules = ops.AIServiceRules;
// Presenters (D273–D277): a person's own presenter at an AI service, stock presenters, training opt-outs.
export type PresenterView = ops.PresenterView;
export type PresenterOut = ops.PresenterOut;
export type PresenterStartIn = ops.PresenterStartIn;
export type PresenterMakeIn = ops.PresenterMakeIn;
export type PresenterTestIn = ops.PresenterTestIn;
export type PresenterStockOut = ops.PresenterStockOut;
export type GenOptions = ops.GenOptions;
export type GenClip = ops.GenClip;
export type GenListOut = ops.GenListOut;
export type GenSettingsOut = ops.GenSettingsOut;
export type GenModelView = ops.GenModelView;
export type GenLimit = ops.GenLimit;
export type GeneratedSource = ops.GeneratedSource;
export type LookView = ops.LookView;
export type LookCreateIn = ops.LookCreateIn;
export type ConsentView = ops.ConsentView;
export type ConsentRecordIn = ops.ConsentRecordIn;
/* Settings → General (version.show, D130): this MetaRush, what's new in it, every computer's version. */
export type VersionShowOut = ops.VersionShowOut;
export type ComputerVersion = ops.ComputerVersion;
/* The first run (setup.status, D256): what works with no keys, what a key adds, Claude Code (D255). */
export type SetupStatusOut = ops.SetupStatusOut;
export type WorksItem = ops.WorksItem;
/* Components (D324–D327): what MetaRush needs, the optional parts that unlock features, adding and removing them. */
export type ComponentState = ops.ComponentState;
export type ComponentsOut = ops.ComponentsOut;
export type ReleaseNotes = ops.ReleaseNotes;
export type ReleaseNote = ops.ReleaseNote;

/* Remotion sources (D131–D138): Remotion projects brought into a project, their versions, approvals and clips. */
export type RemotionSourceRow = ops.RemotionSourceRow;
export type RemotionCompositionRow = ops.RemotionCompositionRow;
export type RemotionClipRow = ops.RemotionClipRow;
export type RemotionShowOut = ops.RemotionShowOut;
export type RemotionVersionRow = ops.RemotionVersionRow;
export type RemotionStillRow = ops.RemotionStillRow;
export type RemotionVersion = ops.RemotionVersion;
export type RemotionClipSource = ops.RemotionClipSource;
/* The project map, words as settings, sound in layers, lifted graphics (D167–D172). */
export type RemotionMapOut = ops.RemotionMapOut;
export type RemotionMapComposition = ops.RemotionMapComposition;
export type RemotionMapScene = ops.RemotionMapScene;
export type RemotionMapSound = ops.RemotionMapSound;
export type RemotionWordsOut = ops.RemotionWordsOut;
export type RemotionWordRow = ops.RemotionWordRow;
export type RemotionWordsCheckOut = ops.RemotionWordsCheckOut;
export type RemotionTaskOut = ops.RemotionTaskOut;
/* Pictures as settings (D209): a source's pictures, replaced as data without a new version of its code. */
export type RemotionPicturesOut = ops.RemotionPicturesOut;
export type RemotionPictureRow = ops.RemotionPictureRow;
export type RemotionPicturesCheckOut = ops.RemotionPicturesCheckOut;
/* Refreshing from its own steps and sending changes back (D173–D176). */
export type RemotionRefreshIn = ops.RemotionRefreshIn;
export type RemotionRefreshPlanOut = ops.RemotionRefreshPlanOut;
export type RemotionRefreshRow = ops.RemotionRefreshRow;
export type RemotionKey = ops.RemotionKey;
export type RemotionPushIn = ops.RemotionPushIn;
export type RemotionPushPlanOut = ops.RemotionPushPlanOut;
export type RemotionPushRow = ops.RemotionPushRow;

/* What a project keeps (D166): "Keep this?" cards, attached files, rules and checks, sign-offs, where the video goes. */
export type KeepFile = ops.KeepFile;
export type KeepCardInfo = ops.KeepCard;
export type KeepItem = ops.KeepItem;
export type KeepKind = ops.KeepItem['kind'];
export type ProjectRule = ops.ProjectRule;
export type Signoff = ops.Signoff;
export type SignoffsOut = ops.SignoffsOut;
export type Deliverable = ops.Deliverable;
/* Call recordings (D160–D162): split-second flashes at clip edges and the printed names' bar. */
export type CutFlashesOut = ops.CutFlashesOut;
export type CutNamesOut = ops.CutNamesOut;
export type EdgeFlash = ops.EdgeFlash;
/* Motion graphics' test frames in the shapes of the cuts they are for (D164). */
export type MotionShape = ops.MotionShape;
export type MotionFrame = ops.MotionFrame;

/* Fixing transcripts (D145–D149): fixes and their history, find and replace, spelling lists, fixes waiting for a new version, the caption check. */
export type TranscriptFix = ops.TranscriptFix;
export type TranscriptState = ops.TranscriptState;
export type TranscriptHistoryEntry = ops.TranscriptHistoryEntry;
export type TranscriptHit = ops.Hit;
export type SpellingEntry = ops.SpellingEntry;
export type SpellingSuggestion = ops.Suggestion;
export type PendingFix = ops.PendingFix;
export type CaptionIssue = ops.CaptionIssue;
export type CaptionsAtOut = ops.CaptionsAtOut;
// The menu of abilities (D272): built in and added, with what works here.
export type CatalogueListOut = ops.CatalogueListOut;
export type CatalogueShowOut = ops.CatalogueShowOut;
export type MenuItem = ops.MenuItem;
export type MenuGroup = ops.MenuGroup;
