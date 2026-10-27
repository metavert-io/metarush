# Google Drive and Dropbox folders

MetaRush reads Google Drive and Dropbox folders through **your own rclone** on the computer that
runs it, without syncing them. MetaRush never signs in to Google or Dropbox, never holds a token, and never reads
rclone's settings: you add the Drive or the Dropbox with rclone's own setup (`rclone config`, which signs you in
in your browser), and MetaRush runs the rclone it finds through its allowlisted tools with typed arguments.

```bash
metarush import cloud dropbox:Shoot/Day 1 --list                       # the folder's files, with their IDs
metarush import cloud dropbox:Shoot/Day 1 --name A001.mov --project "Garden walk"
metarush import cloud gdrive:Teaser/Art --all --project "Garden walk"  # every video, sound and picture
metarush doctor                                                         # GOOGLE DRIVE and DROPBOX: rclone and its remotes
```

Ops `add.cloud.list` (read) and `add.cloud` (brings files in), both on this computer only (the rclone is its own).
Recipes take a Google Drive folder as an input (`from: {drive: "gdrive:Teaser/Art"}`, docs/recipes.md).

## How it reads

| | Google Drive (`drive` remotes) | Dropbox (`dropbox` remotes) |
|---|---|---|
| What is set up | `rclone version`, `rclone listremotes --long` (no network) | the same |
| A folder's files | `rclone lsjson --files-only --hash`: Drive IDs, sizes, times, SHA-256/MD5 | the same: Dropbox IDs (`id:…`) and Dropbox's content hash |
| One file, by its ID | `rclone backend copyid` | the folder listed again to find where the ID is now (a renamed file is still found), then `rclone copyto` |
| Checked against the listing | size, and SHA-256 (else MD5) | size, and the content hash (the SHA-256 of each 4 MiB block's SHA-256) |

Every call passes `--ask-password=false`; only remotes of the folder's own type are used, named exactly as rclone
lists them, so nothing in a folder's address can make rclone read this computer's files or another service. A
file brought in says where it came from: footage with its platform (`drive` or `dropbox`), the file's ID and the
rclone version; pictures tied to their file (a changed file is a new version of the same picture).

## When something is missing

The doctor and every error say it plainly, with rclone's own command behind Details: rclone not installed
(`brew install rclone`), no Drive or Dropbox set up (`rclone config`), settings locked with a password, a sign-in
the service wants again (`rclone config reconnect <remote>:`), a folder or file that isn't there.

## Tests

`internal/testutil/fakerclone` stands in for rclone (its Drives and Dropboxes are folders on the computer); the
real rclone is refused under `go test`. `go test ./internal/drive ./internal/intake -run 'Dropbox|Drive'`.
