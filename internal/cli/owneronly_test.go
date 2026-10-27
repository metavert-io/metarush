package cli

import (
	"sort"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
)

// ownerOnlyOps are the ops only an owner runs (D183, D191; the list in
// docs/accounts.md): people and sign-in, remote access, the storage and the
// Trash, keys, Claude Code, the watched folder and link imports, the log and
// the weekly improvement report, this computer's battery setting, machines
// and clearing archived projects out for good (D196), approving abilities
// and taking an approval back (D231; adding an ability's keys checks inside,
// ability.setup). Ops whose workspace
// setting is the owners' while a project's own stays open check inside
// (ops.RequireOwner): footage.describe.configure, generate.configure and
// account.preferences.set (estimates for everyone, D208; your own stay yours); so does
// extension.revoke for someone else's browser (D197: everyone pairs and
// unpairs their own). (provider.settings changes Content Settings and a
// project's budget, open to members: D193.)
var ownerOnlyOps = []string{
	"auth.method.set", "users.invite", "users.resetLink", "users.revoke", "users.disable", "users.enable", "users.remove", "users.role",
	"remote.enable", "remote.disable", "remote.allow", "remote.signout",
	"storage.migrate", "trash.configure", "trash.empty", "worker.configure", "asset.delete",
	"watch.browse", "resources.link", "resources.folder", "render.follow.set", "sound.setup",
	"provider.set", "provider.remove", "ai.local.setup", "components.add", "components.remove",
	"assistant.settings.set",
	"watch.set", "watch.disable", "watch.mkdir", "ingest.url.configure", "ingest.url.update",
	"project.purge",
	"log.configure", "log.purge", "log.hooks.enable", "log.hooks.disable", "improve.schedule.set",
	"machine.invite", "machine.revoke", "machine.rekey",
	"ability.approve", "ability.revoke",
	"publish.setup",
	"delivery.gate.set", "content.gate.set",
	"share.settings.set",
}

// Every op of the list is owner-only, and every owner-only op is on the list
// (so the docs say which they are).
func TestOwnerOnlyOps(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range ownerOnlyOps {
		listed[name] = true
		op, ok := ops.Get(name)
		if !ok {
			t.Errorf("unknown op %s", name)
		} else if !op.OwnerOnly {
			t.Errorf("%s should be owner-only (D191)", name)
		}
	}
	var extra []string
	for _, op := range ops.All() {
		if op.OwnerOnly && !listed[op.Name] {
			extra = append(extra, op.Name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("owner-only ops missing from the list (and from docs/accounts.md): %v", extra)
	}
}
