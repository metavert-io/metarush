package models

import (
	"strings"
	"time"

	"github.com/invopop/jsonschema"
)

// Abilities (D230–D235): capabilities added without a release, one record per
// workspace and version (the SHA-256 of the folder's canonical archive), what
// each computer set up for them, and their runs.

// States of an ability's version.
const (
	AbilityWaiting    = "waiting"     // checked; waits for an owner
	AbilityApproved   = "approved"    // an owner approved it: members may run it
	AbilityTurnedDown = "turned_down" // an owner said no
	AbilityRevoked    = "revoked"     // approved once, then taken back
	AbilityFailed     = "failed"      // its check didn't pass: nobody can approve it
	// AbilityImported: brought in from another workspace's ability file
	// (D292); its test hasn't run here and no owner here approved it.
	AbilityImported = "imported"
)

// AbilityObject is a stored file (the archive) by key and SHA-256.
type AbilityObject struct {
	Key    string `bson:"key" json:"-"`
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
}

// AbilityFile is one file of a version, as the approval page lists it.
type AbilityFile struct {
	Path    string `bson:"path" json:"path"`
	Size    int64  `bson:"size" json:"size"`
	SHA256  string `bson:"sha256" json:"sha256"`
	Program bool   `bson:"program,omitempty" json:"program,omitempty" jsonschema:"description=Marked as a program (it may run)."`
}

// AbilityKey is a key a version uses, the variable its code reads a stand-in
// from, and the hosts MetaRush's proxy puts it in for.
type AbilityKey struct {
	Name  string   `bson:"name" json:"name"`
	Env   string   `bson:"env" json:"env"`
	Hosts []string `bson:"hosts" json:"hosts"`
	// Header or Query: the one place it goes (D232).
	Header string `bson:"header,omitempty" json:"header,omitempty"`
	Query  string `bson:"query,omitempty" json:"query,omitempty"`
}

// AbilityInput is one input as lists and the run dialog show it.
type AbilityInput struct {
	Name     string   `bson:"name" json:"name"`
	Kind     string   `bson:"kind" json:"kind" jsonschema:"enum=words,enum=number,enum=choice,enum=yes-no,enum=footage,enum=picture,enum=sound"`
	Says     string   `bson:"says,omitempty" json:"says,omitempty"`
	Min      *float64 `bson:"min,omitempty" json:"min,omitempty"`
	Max      *float64 `bson:"max,omitempty" json:"max,omitempty"`
	Default  any      `bson:"default,omitempty" json:"default,omitempty"`
	Options  []string `bson:"options,omitempty" json:"options,omitempty"`
	Optional bool     `bson:"optional,omitempty" json:"optional,omitempty"`
}

// AbilityPrice is a priced ability's price, with the day it was read.
type AbilityPrice struct {
	Service string  `bson:"service,omitempty" json:"service,omitempty"`
	USD     float64 `bson:"usd" json:"usd"`
	Per     string  `bson:"per" json:"per"`
	From    string  `bson:"from,omitempty" json:"from,omitempty" jsonschema:"description=The number input that says how many units a run uses (none: one per run)."`
	AsOf    string  `bson:"asOf" json:"asOf"`
}

// AbilityFacts are what a version declares, as MetaRush enforces it: read
// from its manifest when it was checked.
type AbilityFacts struct {
	Runs    string         `bson:"runs" json:"runs"`
	Inputs  []AbilityInput `bson:"inputs" json:"inputs"`
	Outputs []string       `bson:"outputs" json:"outputs"`
	Free    bool           `bson:"free" json:"free"`
	Price   *AbilityPrice  `bson:"price,omitempty" json:"price,omitempty"`
	Keys    []AbilityKey   `bson:"keys" json:"keys"`
	Hosts   []string       `bson:"hosts" json:"hosts"`
	Network string         `bson:"network" json:"network" jsonschema:"enum=off,enum=metarush,enum=open"`
	// Local is the one service on the computer a run happens on that it may
	// reach, once an owner agreed (D284).
	Local    *AbilityLocal `bson:"local,omitempty" json:"local,omitempty"`
	Sends    []string      `bson:"sends" json:"sends"`
	Outside  bool          `bson:"changesOutside,omitempty" json:"changesOutside,omitempty"`
	Likeness bool          `bson:"likeness,omitempty" json:"likeness,omitempty"`
	Programs []string      `bson:"programs" json:"programs"`
	// Files are files a person sets up on each computer (D285).
	Files   []AbilityNeededFile `bson:"files,omitempty" json:"files,omitempty"`
	OS      []string            `bson:"os,omitempty" json:"os,omitempty"`
	Licence string              `bson:"licence" json:"licence"`
	// Terms are the service's terms a person should know (D283).
	Terms   string `bson:"terms,omitempty" json:"terms,omitempty"`
	Minutes int    `bson:"minutes" json:"minutes"`
	MB      int    `bson:"mb" json:"mb"`
	// Provides are the kinds of AI work it does (D281): once approved, it is
	// a provider of each, priced, consented and recorded like any other.
	Provides []string `bson:"provides,omitempty" json:"provides,omitempty"`
}

// AbilityNeededFile is a file a version reads that a person sets up on each
// computer (D285), and the variable its copy is at.
type AbilityNeededFile struct {
	Name string `bson:"name" json:"name"`
	Says string `bson:"says" json:"says"`
	// Kind is a kind of file MetaRush checks by its first bytes
	// (whisper-model, whisper-translator; "" for any).
	Kind string `bson:"kind,omitempty" json:"kind,omitempty"`
	Env  string `bson:"env" json:"env"`
}

// AbilityLocal is a service on this computer an ability may reach (D284).
type AbilityLocal struct {
	Port    int                `bson:"port" json:"port"`
	Service string             `bson:"service" json:"service"`
	Probe   *AbilityLocalProbe `bson:"probe,omitempty" json:"probe,omitempty"`
}

// AbilityLocalProgram is the program listening on an ability's one local
// service (D284): what an owner agreed to, by name and executable path.
type AbilityLocalProgram struct {
	PID  int    `bson:"-" json:"pid,omitempty"`
	Name string `bson:"name" json:"name"`
	Path string `bson:"path" json:"path"`
	// Interpreter: a program that runs a script it is given (Python,
	// Node.js…), identified also by the folder it was started in and the
	// script or module it runs ("-m http.server").
	Interpreter bool   `bson:"interpreter,omitempty" json:"interpreter,omitempty"`
	Cwd         string `bson:"cwd,omitempty" json:"cwd,omitempty"`
	Script      string `bson:"script,omitempty" json:"script,omitempty"`
}

// AbilityLocalProbe is a request MetaRush makes to an ability's local
// service to tell it apart (D284): a GET of Path answering 200 in JSON with
// each Expect field (dotted: system.comfyui_version).
type AbilityLocalProbe struct {
	Path   string   `bson:"path" json:"path"`
	Expect []string `bson:"expect" json:"expect"`
}

// AbilityTestRun is how the check's run went (the network off).
type AbilityTestRun struct {
	OK        bool    `bson:"ok" json:"ok"`
	Seconds   float64 `bson:"seconds" json:"seconds"`
	Exit      int     `bson:"exit" json:"exit"`
	Isolation string  `bson:"isolation" json:"isolation"`
	// Outputs are the files it wrote, with the kind each is.
	Outputs []AbilityTestOutput `bson:"outputs" json:"outputs"`
	// Log is the end of what it printed (keys and secrets hidden).
	Log string `bson:"log,omitempty" json:"log,omitempty"`
}

// AbilityTestOutput is one file a test run wrote.
type AbilityTestOutput struct {
	Path string `bson:"path" json:"path"`
	Kind string `bson:"kind" json:"kind"`
	Size int64  `bson:"size" json:"size"`
	OK   bool   `bson:"ok" json:"ok" jsonschema:"description=It is a kind the ability declared."`
}

// AbilityCheck is what `ability check` found.
type AbilityCheck struct {
	At       time.Time       `bson:"at" json:"at"`
	OK       bool            `bson:"ok" json:"ok"`
	Problems []string        `bson:"problems" json:"problems"`
	Warnings []string        `bson:"warnings" json:"warnings"`
	Test     *AbilityTestRun `bson:"test,omitempty" json:"test,omitempty"`
	By       Actor           `bson:"by" json:"by"`
	Machine  string          `bson:"machine" json:"machine"`
}

// AbilityDecision is one decision an owner made about a version.
type AbilityDecision struct {
	State string    `bson:"state" json:"state" jsonschema:"enum=approved,enum=turned_down,enum=revoked"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
	Note  string    `bson:"note,omitempty" json:"note,omitempty"`
	// OpenNetwork: the owner also agreed to the computer's whole network
	// (an ability whose manifest asks for it, D235).
	OpenNetwork bool `bson:"openNetwork,omitempty" json:"openNetwork,omitempty"`
	// LocalPort: the owner agreed to the service on this port of the computer
	// a run happens on (D284), and to the program listening there then.
	LocalPort    int                  `bson:"localPort,omitempty" json:"localPort,omitempty"`
	LocalProgram *AbilityLocalProgram `bson:"localProgram,omitempty" json:"localProgram,omitempty"`
}

// AbilityVersion is one version of an ability in a workspace (collection
// abilities, _id <workspace>/<sha256>).
type AbilityVersion struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	Name        string `bson:"name" json:"name"`
	Hash        string `bson:"hash" json:"hash"`
	// Number counts the versions of a name: 1, 2, 3…
	Number   int           `bson:"number" json:"number"`
	Title    string        `bson:"title" json:"title"`
	Does     string        `bson:"does" json:"does"`
	Search   []string      `bson:"search" json:"search"`
	Facts    AbilityFacts  `bson:"facts" json:"facts"`
	Manifest string        `bson:"manifest" json:"manifest" jsonschema:"description=ability.yaml as written."`
	Notes    string        `bson:"notes,omitempty" json:"notes,omitempty" jsonschema:"description=notes.md (at most 8 KB)."`
	Files    []AbilityFile `bson:"files" json:"files"`
	Ignored  []string      `bson:"ignored,omitempty" json:"ignored,omitempty" jsonschema:"description=Hidden files and folders left out of the version."`
	Archive  AbilityObject `bson:"archive" json:"archive"`
	Check    AbilityCheck  `bson:"check" json:"check"`
	State    string        `bson:"state" json:"state" jsonschema:"enum=waiting,enum=approved,enum=turned_down,enum=revoked,enum=failed,enum=imported"`
	// Decisions are the owners' decisions, oldest first.
	Decisions []AbilityDecision `bson:"decisions" json:"decisions"`
	// OpenNetwork: an owner agreed to the computer's whole network (D235).
	OpenNetwork bool `bson:"openNetwork,omitempty" json:"openNetwork,omitempty"`
	// Imported says where a version brought in from an ability file came
	// from (D292): information only — nothing there approves it here.
	Imported *AbilityImport `bson:"imported,omitempty" json:"imported,omitempty"`
	// LocalPort: an owner agreed to the service on this port of the computer
	// a run happens on, the one its manifest names (D284).
	LocalPort int `bson:"localPort,omitempty" json:"localPort,omitempty"`
	// LocalProgram is the program that listened there when it was approved.
	LocalProgram *AbilityLocalProgram `bson:"localProgram,omitempty" json:"localProgram,omitempty"`
	By           Actor                `bson:"by" json:"by"`
	CreatedAt    time.Time            `bson:"createdAt" json:"createdAt"`
	UpdatedAt    time.Time            `bson:"updatedAt" json:"updatedAt"`
}

// AbilityProgram is a program a person set up for an ability on one
// computer: where it is and the SHA-256 it had then (a changed program is
// refused until a person sets it up again).
type AbilityProgram struct {
	Name   string `bson:"name" json:"name"`
	Path   string `bson:"path" json:"path"`
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
}

// AbilityFilePin is a file a person set up for an ability on one computer
// (D285): where it is, its size and modification time (checked cheaply on
// every plan) and its SHA-256 (checked on the copy each run reads).
type AbilityFilePin struct {
	Name    string    `bson:"name" json:"name"`
	Path    string    `bson:"path" json:"path"`
	Size    int64     `bson:"size" json:"size"`
	ModTime time.Time `bson:"modTime" json:"modTime"`
	SHA256  string    `bson:"sha256" json:"sha256"`
	// Kind and About are what MetaRush read the file to be (a whisper.cpp
	// model, medium, multilingual).
	Kind  string `bson:"kind,omitempty" json:"kind,omitempty"`
	About string `bson:"about,omitempty" json:"about,omitempty"`
}

// AbilitySetup is what a person set up for an ability on one computer
// (collection ability_setups, _id <workspace>/<installation>/<name>).
type AbilitySetup struct {
	ID           string           `bson:"_id" json:"id"`
	WorkspaceID  string           `bson:"workspaceId" json:"-"`
	Name         string           `bson:"name" json:"name"`
	Installation string           `bson:"installation" json:"installation"`
	Machine      string           `bson:"machine" json:"machine"`
	Programs     []AbilityProgram `bson:"programs" json:"programs"`
	Files        []AbilityFilePin `bson:"files,omitempty" json:"files,omitempty"`
	// LocalProgram is the program an owner set up as the version's local
	// service on this computer (D284), over the one recorded at approval.
	LocalProgram *AbilityLocalProgram `bson:"localProgram,omitempty" json:"localProgram,omitempty"`
	By           Actor                `bson:"by" json:"by"`
	At           time.Time            `bson:"at" json:"at"`
}

// States of a run.
const (
	AbilityRunQueued   = "queued"
	AbilityRunRunning  = "running"
	AbilityRunDone     = "done"
	AbilityRunFailed   = "failed"
	AbilityRunCanceled = "canceled"
)

// AbilityRunInput is one input a run was given: words, numbers and choices
// as given, footage, pictures and sound by ID and SHA-256.
type AbilityRunInput struct {
	Name  string `bson:"name" json:"name"`
	Kind  string `bson:"kind" json:"kind"`
	Value any    `bson:"value,omitempty" json:"value,omitempty"`
	// Ref is the clip or picture used (its ID); SHA256 its bytes.
	Ref    string `bson:"ref,omitempty" json:"ref,omitempty"`
	Title  string `bson:"title,omitempty" json:"title,omitempty"`
	SHA256 string `bson:"sha256,omitempty" json:"sha256,omitempty"`
}

// AbilityRequest is one request a run made through MetaRush's proxy: host,
// path and the names of query parameters, never their values or a key.
type AbilityRequest struct {
	At       time.Time `bson:"at" json:"at"`
	Method   string    `bson:"method" json:"method"`
	Host     string    `bson:"host" json:"host"`
	Port     int       `bson:"port,omitempty" json:"port,omitempty"`
	Path     string    `bson:"path" json:"path"`
	Query    []string  `bson:"query,omitempty" json:"query,omitempty"`
	Status   int       `bson:"status,omitempty" json:"status,omitempty"`
	Sent     int64     `bson:"sent" json:"sent"`
	Received int64     `bson:"received" json:"received"`
	Millis   int64     `bson:"ms" json:"ms"`
	Keys     []string  `bson:"keys,omitempty" json:"keys,omitempty"`
	Refused  string    `bson:"refused,omitempty" json:"refused,omitempty"`
}

// AbilityOutput is one file a run made and what it became.
type AbilityOutput struct {
	Path       string `bson:"path" json:"path"`
	Kind       string `bson:"kind" json:"kind"`
	Size       int64  `bson:"size" json:"size"`
	SHA256     string `bson:"sha256" json:"sha256"`
	AssetID    string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	Title      string `bson:"title,omitempty" json:"title,omitempty"`
	// Licence and Credit are what the run said about the file (D283).
	Licence string `bson:"licence,omitempty" json:"licence,omitempty"`
	Credit  string `bson:"credit,omitempty" json:"credit,omitempty"`
	Status  string `bson:"status" json:"status" jsonschema:"description=added\\, already there\\, or left out."`
	Why     string `bson:"why,omitempty" json:"why,omitempty" jsonschema:"description=Why it was left out\\, in plain words."`
}

// AbilityRun is one run of an ability (collection ability_runs).
type AbilityRun struct {
	ID            string            `bson:"_id" json:"id"`
	WorkspaceID   string            `bson:"workspaceId" json:"-"`
	Name          string            `bson:"name" json:"name"`
	Title         string            `bson:"title" json:"title"`
	Hash          string            `bson:"hash" json:"hash"`
	VersionNumber int               `bson:"versionNumber" json:"versionNumber"`
	Number        int               `bson:"number" json:"number"`
	ProjectID     string            `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Inputs        []AbilityRunInput `bson:"inputs" json:"inputs"`
	Fingerprint   string            `bson:"fingerprint" json:"fingerprint"`
	// EstimateUSD is what the plan said it would cost; MaxUSD what the person
	// agreed to.
	EstimateUSD  float64          `bson:"estimateUsd" json:"estimateUsd"`
	MaxUSD       float64          `bson:"maxUsd,omitempty" json:"maxUsd,omitempty"`
	State        string           `bson:"state" json:"state" jsonschema:"enum=queued,enum=running,enum=done,enum=failed,enum=canceled"`
	JobID        string           `bson:"jobId,omitempty" json:"jobId,omitempty"`
	Installation string           `bson:"installation" json:"installation"`
	Machine      string           `bson:"machine" json:"machine"`
	Isolation    string           `bson:"isolation,omitempty" json:"isolation,omitempty"`
	Network      string           `bson:"network" json:"network"`
	By           Actor            `bson:"by" json:"by"`
	Requests     []AbilityRequest `bson:"requests" json:"requests"`
	// RequestsLeftOut counts requests past the 2,000 kept.
	RequestsLeftOut int             `bson:"requestsLeftOut,omitempty" json:"requestsLeftOut,omitempty"`
	Outputs         []AbilityOutput `bson:"outputs" json:"outputs"`
	Exit            *int            `bson:"exit,omitempty" json:"exit,omitempty"`
	Seconds         float64         `bson:"seconds,omitempty" json:"seconds,omitempty"`
	Summary         string          `bson:"summary,omitempty" json:"summary,omitempty"`
	Error           string          `bson:"error,omitempty" json:"error,omitempty"`
	// Log is the end of what the program printed (keys and secrets hidden).
	Log        string     `bson:"log,omitempty" json:"-"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
	StartedAt  *time.Time `bson:"startedAt,omitempty" json:"startedAt,omitempty"`
	FinishedAt *time.Time `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
	UpdatedAt  time.Time  `bson:"updatedAt" json:"updatedAt"`
	// Work is the AI work it did as a provider (D281: its generation record),
	// which keeps what it made and tells the person.
	Work string `bson:"work,omitempty" json:"work,omitempty"`
}

// AbilityMadeWith is how an ability made a clip or a picture (D234): the
// ability and its version, the run, its inputs by value or SHA-256, and what
// the run was expected to cost.
type AbilityMadeWith struct {
	// Ability is name@version (the first 8 characters of the hash).
	Ability string            `bson:"ability" json:"ability"`
	Name    string            `bson:"name" json:"name"`
	Hash    string            `bson:"hash" json:"hash"`
	Title   string            `bson:"title,omitempty" json:"title,omitempty"`
	RunID   string            `bson:"runId" json:"runId"`
	Inputs  []AbilityRunInput `bson:"inputs,omitempty" json:"inputs,omitempty"`
	CostUSD float64           `bson:"costUsd,omitempty" json:"costUsd,omitempty"`
	// Licence and Source are what the run said about the file (a stock
	// photo's licence and page), when it said.
	Licence string `bson:"licence,omitempty" json:"licence,omitempty"`
	Source  string `bson:"source,omitempty" json:"source,omitempty"`
	// Credit is the credit line the file's licence asks for, as the run said
	// it ("Photo by … on Pexels"); Author is who made the file (D283).
	Credit string `bson:"credit,omitempty" json:"credit,omitempty"`
	Author string `bson:"author,omitempty" json:"author,omitempty"`
}

// CreditLine is the credit for a file an ability brought in, for deliveries
// (D283): what brought it in, the credit its licence asks for, the licence
// and the file's page. what says the file ("footage", "sound", "a picture");
// "" when the run said nothing about the file.
func (m *AbilityMadeWith) CreditLine(what string) string {
	if m == nil || (m.Licence == "" && m.Source == "" && m.Credit == "") {
		return ""
	}
	title := m.Title
	if title == "" {
		title = m.Name
	}
	c := "Includes " + what + " brought in with " + title
	if m.Credit != "" {
		c += " — " + m.Credit
	}
	if m.Licence != "" && !strings.Contains(m.Credit, m.Licence) {
		c += " (" + m.Licence + ")"
	}
	if m.Source != "" {
		c += ": " + m.Source
	}
	return c
}

// PlatformAbility marks a clip an ability made (AssetSource.Platform).
const PlatformAbility = "ability"

// AbilityImport is where a version brought in from an ability file came
// from (D292). Its approval record there is information only: here it waits
// for its check and an owner like any other version.
type AbilityImport struct {
	// Workspace is the name of the workspace the file was made in; Made when.
	Workspace string    `bson:"workspace,omitempty" json:"workspace,omitempty"`
	Made      time.Time `bson:"made" json:"made"`
	MetaRush  string    `bson:"metarush,omitempty" json:"metarush,omitempty" jsonschema:"description=The MetaRush version that made the file."`
	// Number and State are the version's number and state there.
	Number int    `bson:"number" json:"number"`
	State  string `bson:"state" json:"state"`
	// Decisions are the owners' decisions there (names, not accounts).
	Decisions []AbilityFileDecision `bson:"decisions" json:"decisions"`
	// Lineage is every version this one descends from, oldest first, this
	// one last.
	Lineage []string  `bson:"lineage" json:"lineage"`
	At      time.Time `bson:"at" json:"at"`
	By      Actor     `bson:"by" json:"by"`
	// Catalogue says the community catalogue it was brought in from (D485),
	// as the catalogue listed it then: its words, never checked by MetaRush.
	Catalogue *AbilityFromCatalogue `bson:"catalogue,omitempty" json:"catalogue,omitempty"`
}

// AbilityFromCatalogue is where a version brought in from a community
// catalogue came from (D485): the catalogue, its listing then, and the
// source MetaRush fetched (pinned exactly). Its words are the catalogue's.
type AbilityFromCatalogue struct {
	// Key is the catalogue's short name in this workspace; URL its address.
	Key   string `bson:"key" json:"key"`
	URL   string `bson:"url" json:"url"`
	Title string `bson:"title" json:"title"`
	// Version and Date are the catalogue's own, as its index said.
	Version int    `bson:"version" json:"version"`
	Date    string `bson:"date,omitempty" json:"date,omitempty"`
	// Publisher is who the catalogue says published it (its words, not verified).
	Publisher string `bson:"publisher,omitempty" json:"publisher,omitempty"`
	// Source is what MetaRush fetched: a repository at a commit and a folder,
	// or an ability file's address and its SHA-256.
	Repository string `bson:"repository,omitempty" json:"repository,omitempty"`
	Commit     string `bson:"commit,omitempty" json:"commit,omitempty"`
	Path       string `bson:"path,omitempty" json:"path,omitempty"`
	FileURL    string `bson:"fileUrl,omitempty" json:"fileUrl,omitempty"`
	FileSHA256 string `bson:"fileSha256,omitempty" json:"fileSha256,omitempty"`
}

// AbilityCatalogue is a community catalogue of abilities an owner trusts
// (D481, collection ability_catalogues, _id <workspace>/<key>): its address,
// who added it, and the last copy of its index MetaRush read (D482), cleaned
// (D483). Trusting it lets people browse it and bring abilities in from it;
// each one still waits for a check here and an owner's approval.
type AbilityCatalogue struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	// Key is its short name here: what people type before the colon in
	// catalogue:ability.
	Key     string    `bson:"key" json:"key"`
	URL     string    `bson:"url" json:"url"`
	AddedBy Actor     `bson:"addedBy" json:"addedBy"`
	AddedAt time.Time `bson:"addedAt" json:"addedAt"`
	// FetchedAt is when its index was last read whole (the server's time);
	// CheckedAt when MetaRush last asked for it, read or not.
	FetchedAt *time.Time `bson:"fetchedAt,omitempty" json:"fetchedAt,omitempty"`
	CheckedAt *time.Time `bson:"checkedAt,omitempty" json:"checkedAt,omitempty"`
	// ETag is what its address said of that copy (asked again with it).
	ETag string `bson:"etag,omitempty" json:"etag,omitempty"`
	// SHA256 is of the index's bytes as read.
	SHA256 string `bson:"sha256,omitempty" json:"sha256,omitempty"`
	// Problem is why the last time MetaRush asked didn't give a new copy, in
	// plain words ("" when it did): the copy before stays in use.
	Problem string `bson:"problem,omitempty" json:"problem,omitempty"`
	// Index is the copy MetaRush read, cleaned: its words are the catalogue's.
	Index *CatalogueCopy `bson:"index,omitempty" json:"index,omitempty"`
}

// CatalogueCopy is a catalogue's index as MetaRush keeps it (D480, D483):
// what it says of itself, the abilities it lists that this MetaRush can read,
// the ones it left out and why, and the versions the catalogue took back.
type CatalogueCopy struct {
	Name       string              `bson:"name" json:"name"`
	Title      string              `bson:"title" json:"title"`
	About      string              `bson:"about,omitempty" json:"about,omitempty"`
	Maintainer *CataloguePublisher `bson:"maintainer,omitempty" json:"maintainer,omitempty"`
	Version    int                 `bson:"version" json:"version"`
	Date       string              `bson:"date" json:"date"`
	Abilities  []CatalogueEntry    `bson:"abilities" json:"abilities"`
	LeftOut    []CatalogueLeftOut  `bson:"leftOut,omitempty" json:"leftOut,omitempty"`
	Revoked    []CatalogueRevoked  `bson:"revoked,omitempty" json:"revoked,omitempty"`
	Signed     bool                `bson:"signed,omitempty" json:"signed,omitempty" jsonschema:"description=The index carries signatures (a later registry's): this MetaRush neither reads nor trusts them."`
}

// CatalogueEntry is one ability a catalogue lists (metarush.ability-catalogue/1, D480).
type CatalogueEntry struct {
	// Name is the ability's name, as its manifest says.
	Name  string `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Title string `bson:"title" json:"title" jsonschema:"minLength=1,maxLength=80"`
	// Does is one line: what it does for a video.
	Does string `bson:"does" json:"does" jsonschema:"minLength=1,maxLength=300"`
	// Notes are longer notes for people: how to use it well, what it needs.
	Notes     string             `bson:"notes,omitempty" json:"notes,omitempty" jsonschema:"maxLength=4000"`
	Publisher CataloguePublisher `bson:"publisher" json:"publisher"`
	Source    CatalogueSource    `bson:"source" json:"source"`
	// Archive is the SHA-256 of the ability's canonical archive
	// (metarush.ability/1): its version, which what MetaRush fetches must be.
	Archive  string   `bson:"archive" json:"archive" jsonschema:"pattern=^[0-9a-f]{64}$"`
	Licence  string   `bson:"licence" json:"licence" jsonschema:"minLength=1,maxLength=80"`
	Provides []string `bson:"provides,omitempty" json:"provides,omitempty" jsonschema:"maxItems=8"`
	Outputs  []string `bson:"outputs,omitempty" json:"outputs,omitempty" jsonschema:"maxItems=6"`
	// Network is off, metarush (only the hosts it names, through MetaRush) or open.
	Network string         `bson:"network" json:"network" jsonschema:"enum=off,enum=metarush,enum=open"`
	Hosts   []string       `bson:"hosts,omitempty" json:"hosts,omitempty" jsonschema:"maxItems=32"`
	Keys    []CatalogueKey `bson:"keys,omitempty" json:"keys,omitempty" jsonschema:"maxItems=8"`
	// Sends is what leaves the computer when it runs: nothing, words, pictures, sound, footage.
	Sends []string `bson:"sends,omitempty" json:"sends,omitempty" jsonschema:"maxItems=5"`
	// AI says what AI does to each kind of output: made, edited, enhanced or none.
	AI CatalogueAI `bson:"ai,omitempty" json:"ai,omitempty"`
	// Cost is free (no price, no key), keyed (a key to a service, which may
	// charge) or priced (each run priced first).
	Cost  string          `bson:"cost" json:"cost" jsonschema:"enum=free,enum=keyed,enum=priced"`
	Local *CatalogueLocal `bson:"local,omitempty" json:"local,omitempty"`
	// ChangesOutside: it publishes, uploads or drives another app.
	ChangesOutside bool `bson:"changesOutside,omitempty" json:"changesOutside,omitempty"`
	// Programs are what each computer needs installed; OS the systems it runs on.
	Programs []string        `bson:"programs,omitempty" json:"programs,omitempty" jsonschema:"maxItems=8"`
	OS       []string        `bson:"os,omitempty" json:"os,omitempty" jsonschema:"maxItems=2"`
	Tags     []string        `bson:"tags,omitempty" json:"tags,omitempty" jsonschema:"maxItems=20"`
	Links    []CatalogueLink `bson:"links,omitempty" json:"links,omitempty" jsonschema:"maxItems=8"`
}

// CatalogueAI says, per kind of output, what AI does to it (as an ability's
// manifest says it): made, edited, enhanced or none.
type CatalogueAI map[string]string

// CatalogueAIWords are the four answers, CatalogueAIKinds the kinds of output.
var (
	CatalogueAIWords = []string{"made", "edited", "enhanced", "none"}
	CatalogueAIKinds = []string{"footage", "sound", "picture", "document", "font", "captions"}
)

// JSONSchema names the kinds of output, each one of the four answers.
func (CatalogueAI) JSONSchema() *jsonschema.Schema {
	props := jsonschema.NewProperties()
	words := make([]any, 0, len(CatalogueAIWords))
	for _, w := range CatalogueAIWords {
		words = append(words, w)
	}
	for _, k := range CatalogueAIKinds {
		props.Set(k, &jsonschema.Schema{Type: "string", Enum: words})
	}
	return &jsonschema.Schema{Type: "object", Properties: props, AdditionalProperties: jsonschema.FalseSchema,
		Description: "What AI does to each kind of output: made, edited, enhanced or none."}
}

// CataloguePublisher is who a catalogue says made or maintains something: a
// GitHub account or organisation, or a web address (and a name). Its word,
// never verified.
type CataloguePublisher struct {
	Name   string `bson:"name,omitempty" json:"name,omitempty" jsonschema:"maxLength=80"`
	GitHub string `bson:"github,omitempty" json:"github,omitempty" jsonschema:"pattern=^[A-Za-z0-9][A-Za-z0-9-]{0\\,38}$"`
	URL    string `bson:"url,omitempty" json:"url,omitempty" jsonschema:"maxLength=500,pattern=^https://"`
}

// CatalogueSource is where an ability's files are, pinned exactly: a
// repository at a commit and a folder, or an ability file and its SHA-256.
type CatalogueSource struct {
	Git  *CatalogueGitSource  `bson:"git,omitempty" json:"git,omitempty"`
	File *CatalogueFileSource `bson:"file,omitempty" json:"file,omitempty"`
}

// CatalogueGitSource is a folder of a repository at one commit.
type CatalogueGitSource struct {
	// Repository is the repository's address: https://github.com/<owner>/<name>.
	Repository string `bson:"repository" json:"repository" jsonschema:"maxLength=200,pattern=^https://github\\.com/"`
	Commit     string `bson:"commit" json:"commit" jsonschema:"pattern=^[0-9a-f]{40}$"`
	// Path is the ability's folder in the repository ("" for its top).
	Path string `bson:"path" json:"path" jsonschema:"maxLength=300"`
}

// CatalogueFileSource is an ability file (metarush.ability-file/1) at an address.
type CatalogueFileSource struct {
	URL    string `bson:"url" json:"url" jsonschema:"maxLength=500,pattern=^https://"`
	SHA256 string `bson:"sha256" json:"sha256" jsonschema:"pattern=^[0-9a-f]{64}$"`
}

// CatalogueKey is a key an ability uses and the only hosts it goes to.
type CatalogueKey struct {
	Name  string   `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Hosts []string `bson:"hosts,omitempty" json:"hosts,omitempty" jsonschema:"maxItems=32"`
}

// CatalogueLocal is the one service on the computer an ability reaches.
type CatalogueLocal struct {
	Port    int    `bson:"port" json:"port" jsonschema:"minimum=1024,maximum=32767"`
	Service string `bson:"service" json:"service" jsonschema:"minLength=1,maxLength=60"`
}

// CatalogueLink is a page about an ability.
type CatalogueLink struct {
	Title string `bson:"title" json:"title" jsonschema:"minLength=1,maxLength=80"`
	URL   string `bson:"url" json:"url" jsonschema:"maxLength=500,pattern=^https://"`
}

// CatalogueRevoked is a version a catalogue took back: MetaRush doesn't bring
// it in from that catalogue, and says so of a copy already here.
type CatalogueRevoked struct {
	Archive string `bson:"archive" json:"archive" jsonschema:"pattern=^[0-9a-f]{64}$"`
	Name    string `bson:"name,omitempty" json:"name,omitempty"`
	Why     string `bson:"why,omitempty" json:"why,omitempty" jsonschema:"maxLength=300"`
	Date    string `bson:"date,omitempty" json:"date,omitempty"`
}

// CatalogueLeftOut is an entry of an index MetaRush didn't take, and why.
type CatalogueLeftOut struct {
	Name string `bson:"name" json:"name"`
	Why  string `bson:"why" json:"why"`
}

// AbilityFileDecision is an owner's decision as an ability file carries it:
// who by name, when and what (information only).
type AbilityFileDecision struct {
	State       string    `bson:"state" json:"state" jsonschema:"enum=approved,enum=turned_down,enum=revoked"`
	By          string    `bson:"by" json:"by"`
	At          time.Time `bson:"at" json:"at"`
	Note        string    `bson:"note,omitempty" json:"note,omitempty"`
	OpenNetwork bool      `bson:"openNetwork,omitempty" json:"openNetwork,omitempty"`
}
