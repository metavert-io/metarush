# MetaRush in WSL on Windows

There are two ways to use MetaRush from a Windows PC:

- **In a browser, against a Mac or a Linux computer that runs MetaRush** — nothing to install on the PC but Tailscale
  ([windows.md](windows.md)). The right choice for most people, and for a team.
- **MetaRush itself on the PC, inside WSL 2** (the Windows Subsystem for Linux) with Ubuntu — for people who work in
  a terminal, want Claude Code and MetaRush on the same PC, and are happy to look after a small Linux. This page.

There is no Windows build of MetaRush: inside WSL it is the Linux build, and it works the way it does on any Ubuntu
computer ([installation.md](installation.md)). This page says what is different, and what MetaRush says and does
about it.

## What you need

- **Windows 11**, or Windows 10 22H2, with WSL from the Microsoft Store (`wsl --version` answers). Windows 11 gets
  every WSL setting this page mentions.
- **WSL 2** and **Ubuntu 24.04** (22.04 and 20.04 work too, and so does Debian 12). MetaRush refuses WSL 1: its
  database and many of its tools don't run there.
- 16 GB of memory is comfortable (WSL takes up to half by default), and room on C: for the components (about 2 GB)
  and your footage.
- **Edge or Chrome** on Windows for the dashboard.

## 1. WSL 2 and Ubuntu

In **PowerShell** (as administrator the first time):

```powershell
wsl --install             # WSL 2 and Ubuntu; restart Windows when it asks
wsl --update              # the newest WSL and its Linux kernel
wsl -l -v                 # Ubuntu should say VERSION 2
wsl --set-version Ubuntu 2   # only if it says 1
```

Open **Ubuntu** from the Start menu and choose a Linux user name and password (they are Ubuntu's own, not
Windows').

## 2. systemd, so MetaRush can run in the background

MetaRush's background service is a systemd user unit, as on any Linux. Ubuntu's recent WSL images turn systemd on
already. In Ubuntu, `ps -p 1 -o comm=` says `systemd` when it is on. If it says `init`:

```bash
sudo nano /etc/wsl.conf      # add these two lines, then save
[boot]
systemd=true
```

Then in **PowerShell**: `wsl --shutdown`, and open Ubuntu again. MetaRush never edits `/etc/wsl.conf` itself (it needs
an administrator, and WSL has to restart): setup, `metarush doctor` and the dashboard say what to add instead. Until
then MetaRush runs whenever you open the dashboard (`metarush dashboard`), and `metarush service install` says why
it can't be set up yet.

## 3. MetaRush in Ubuntu

Everything below runs in **Ubuntu**. First the system's own packages MetaRush's components use (MetaRush never runs
`sudo`; these need you once):

```bash
sudo apt update
sudo apt install -y ffmpeg poppler-utils espeak-ng bubblewrap
# the renderer's browser draws your videos without a window, but needs these libraries
# (Ubuntu 24.04 names; on 22.04 and Debian 12: libasound2, libatk1.0-0, libatk-bridge2.0-0 and libcups2):
sudo apt install -y libnss3 libdbus-1-3 libatk1.0-0t64 libasound2t64 libxrandr2 libxkbcommon0 libxfixes3 \
  libxcomposite1 libxdamage1 libgbm1 libcups2t64 libcairo2 libpango-1.0-0 libatk-bridge2.0-0t64
```

Then MetaRush, the Linux way ([installation.md](installation.md)): a release archive (`linux_amd64`; `linux_arm64` on
a Windows on Arm PC) into `~/.local/bin`, or Homebrew on Linux. Then:

```bash
metarush setup
```

Setup says it runs in Ubuntu on Windows and asks its usual questions. The suggested answers are right for WSL:

- **The database on this computer** is MongoDB for Ubuntu, downloaded at setup and kept in your Linux home. WSL's
  own Linux kernel (6.18 with WSL 3.0, late 2026) runs MongoDB 8; if a later WSL kernel is one MongoDB 8 refuses
  (6.19 through 7.0.13), setup says so before downloading and sets up MongoDB 7.0 instead, as on any Linux. A WSL
  kernel is updated from Windows (`wsl --update` in PowerShell), never with Ubuntu's packages.
- **Where things are kept**: keep MetaRush's own folder (`~/.metarush`) and the storage folder in your Linux home.
  Setup refuses the database on a Windows drive (`/mnt/c/…`): MongoDB needs Linux's own disk.
- **Components**: as on any Linux — Homebrew where it is installed, else MetaRush's pinned downloads (Node.js, the
  transcriber, the models), else your packages (FFmpeg, poppler, espeak-ng, bubblewrap above).
- **The renderer** sets itself up with Remotion's headless browser. If Ubuntu lacks a library it needs, setup's "what
  works now", Getting started and doctor say so, with the one command that adds them (Details).
- **Claude Code** runs in Ubuntu, like MetaRush: install it there (`npm install -g @anthropic-ai/claude-code`, or its
  own installer), open `claude` once and sign in. A Claude Code installed only on Windows isn't used, and MetaRush
  says so.
- **Start when you log in**: yes, when systemd runs (step 2). Then make it keep running (below).

## 4. Keeping it running

Windows stops Ubuntu about 15 seconds after its last window closes — and MetaRush with it, even as a background
service. Setup, doctor and Settings → General say so until it is set. To keep MetaRush running with no Ubuntu window
open, tell WSL not to stop it, in `.wslconfig` in your Windows user's folder (`C:\Users\<you>\.wslconfig`; the **WSL
Settings** app in the Start menu writes the same file):

```ini
[general]
instanceIdleTimeout=-1
```

Then `wsl --shutdown` in PowerShell, and open Ubuntu again. MetaRush reads this file to know it is set.

To have MetaRush start when you sign in to Windows (without opening Ubuntu):

```bash
sudo loginctl enable-linger "$USER"   # in Ubuntu: your services start when Ubuntu starts
```

and in **Task Scheduler** → Create Task → Triggers: **At log on** → Actions: Start a program `wsl.exe`, arguments
`-d Ubuntu --exec /bin/true`. Windows starts Ubuntu at sign-in, systemd starts MetaRush, and the setting above keeps
it running.

## 5. Opening the dashboard

`metarush dashboard` (and setup's last step) opens the dashboard in your **Windows** browser: through `wslview` when
it is installed, else Windows' own `rundll32.exe` from `C:\Windows\System32` (`url.dll,FileProtocolHandler
<address>`, which hands the address to Windows as if you had clicked it; no command interpreter reads it), which WSL
lets Ubuntu run. MetaRush never runs a copy of it found anywhere else. The address is `http://127.0.0.1:4845` with a
one-time sign-in; WSL forwards `127.0.0.1` from Windows to Ubuntu, so Edge and Chrome reach it as they would on a Mac.
If Ubuntu can't start Windows programs (`[interop] enabled=false`), MetaRush prints the link and says to open it in
your Windows browser.

- **Use `127.0.0.1`, not `localhost`.** For `localhost` a browser tries `[::1]` first, where any program on Windows
  (and, with mirrored networking, in Ubuntu) could be listening and would get the one-time sign-in. MetaRush's own
  links say `127.0.0.1`, and it never hands Windows' browser a `localhost` address.
- **Only this PC can open it.** MetaRush listens on 127.0.0.1 only, inside WSL as everywhere. If you turned on WSL's
  **mirrored networking** (`networkingMode=mirrored` in `.wslconfig`), MetaRush still listens only on this PC: other
  computers on your network can't open it (doctor says so). To open it from other computers, use Tailscale
  ([windows.md](windows.md), `metarush remote enable`), as from a Mac.
- If `.wslconfig` turns **localhost forwarding** off (`localhostForwarding=false`, NAT mode), Windows' browser can't
  reach Ubuntu's 127.0.0.1: doctor warns, and turning it back on fixes it.
- Install the site as an app in Edge or Chrome (… → Apps → Install this site as an app) to open MetaRush from the Start
  menu.

## 6. Files on Windows

Ubuntu sees Windows' drives as folders: `C:\Users\<you>\Videos` is `/mnt/c/Users/<you>/Videos`. They work, but
Ubuntu reads and writes them over a slower link than its own disk, and Windows never tells Ubuntu that a file there
changed.

- **Add footage from the dashboard** (drag from File Explorer, Choose files): your Windows browser reads the file and
  sends it to MetaRush, which is quick.
- **In the terminal**, MetaRush takes paths as Windows gives them: `metarush add "C:\Users\<you>\Videos\take 1.mp4"`
  (File Explorer's **Copy as path** puts the quotes), `C:/Users/…`, or `\\wsl.localhost\Ubuntu\home\<you>\…`. Files
  that come from the Windows side say so: they take longer to add, and copying footage into your Linux home first is
  faster (`cp /mnt/c/Users/<you>/Videos/*.mp4 ~/Videos/`). A drive plugged in after Ubuntu started isn't visible
  until WSL restarts, or you mount it (`sudo mkdir -p /mnt/e && sudo mount -t drvfs E: /mnt/e`); a network folder's
  Windows address (`\\server\share`) and another Linux's files can't be opened as they are, and MetaRush says so,
  with the command that mounts the share behind Details. A path without its drive (`\Users\<you>`) or relative to a
  drive's current folder (`C:Videos`) is refused too, never taken as a file in the current folder; so is a
  `\\wsl.localhost\…` path when MetaRush can't tell which Linux it runs in (`metarush service install` tells the
  background service the distribution's name, so install it from an Ubuntu terminal).
- **A watched folder or a linked folder** may be on the Windows side (an OBS recordings folder, say): MetaRush looks
  for new files there every 5 seconds (every 2 on Linux's own disk) and says it comes in more slowly. In the
  dashboard you can type it as Windows gives it, or choose it with Browse folders → Windows folder (inside your
  Windows user's folder; never a whole drive).
- **Your Linux home in File Explorer**: **Linux** → **Ubuntu** in the navigation pane, or `\\wsl.localhost\Ubuntu\home\<you>`.
  Keep footage, MetaRush's folder and the storage folder there.
- **Delivering to a Windows folder** works (`metarush deliver --revision <version> --out "C:\Users\<you>\Desktop\Client"`,
  or a `/mnt/c/…` path), more slowly. Delivered files are named so Windows keeps them. A sync folder is one to
  MetaRush, as on a Mac — what may leave a client's projects is asked first, and MetaRush never writes a key or
  password there: OneDrive, Dropbox, Google Drive, Box or iCloud Drive in your Windows user's folder
  (`C:\Users\<you>\OneDrive`, `OneDrive - <organization>`, any case) or at the top of any drive (`D:\Dropbox`,
  `E:\OneDrive`), Google Drive's own drive (`G:\My Drive`), your organization's SharePoint and Teams libraries that
  OneDrive syncs (`C:\Users\<you>\<organization>`, beside `OneDrive - <organization>`), iCloud Photos
  (`Pictures\iCloud Photos`), and wherever Dropbox's own settings say its folders are (`info.json` in your Windows
  user's `AppData`). A sync folder moved deeper than the top of a drive, other than Dropbox's, isn't recognised: keep
  deliveries for clients outside it or check its name.
- **Editing apps on Windows**: a timeline sent to an editing app (`metarush export timeline … --media <folder>`) with
  its footage folder on the Windows side gets copies of the footage there (never Ubuntu's links, which Windows apps
  can't follow); the timeline names the footage by its Ubuntu address, so relink it once in the editing app.
- **Text files**: cut sheets, scripts and notes you edit in a Windows editor (with Windows line endings) are read as
  usual; what MetaRush writes has plain line endings, which Notepad and editing apps read.

doctor's **WINDOWS** checks name MetaRush's own folder, the storage folder and watched or linked folders on the
Windows side.

## GPU notes

- **NVIDIA**: WSL 2 hands an NVIDIA card to Ubuntu through Windows' own NVIDIA driver; never install a Linux NVIDIA
  driver in Ubuntu. `nvidia-smi` works in Ubuntu (it lives in `/usr/lib/wsl/lib`, where MetaRush also looks), and
  studio voices ([sound.md](sound.md)) use the card's CUDA build when it answers.
- Renders draw in a headless browser on the processor, transcripts and the open models that look at your footage run
  on the processor, as on any Linux; nothing else uses a graphics card in WSL.
- Apple Vision is a Mac's: in WSL MetaRush looks at your footage with the open models ([visual-index.md](visual-index.md)).

## What MetaRush says

| Where | What |
|---|---|
| `metarush setup` | that it runs in Ubuntu on Windows (WSL 2); without systemd, that MetaRush can't start by itself yet and what turns it on; at the end, what to know (below) |
| `metarush doctor` | **WINDOWS**: which WSL, that only this PC can open the dashboard, systemd, keeping it running, the Windows browser, mirrored networking or localhost forwarding when set, MetaRush's folders on the Windows side, and a Claude Code installed only on Windows; `--details` adds each command |
| Home's Getting started | **On Windows**: the same notes, each ready or with what to do (the command behind Details) |
| Settings → General | "MetaRush runs in Ubuntu on Windows (WSL 2)" and the notes still to do; Your computers shows Windows (WSL 2) |
| `metarush service install`, `service status` | without systemd, why it can't be set up and what turns it on; that Windows stops Ubuntu after its last window closes |
| Adding, watched and linked folders | files and folders on the Windows side, and that copying into your Linux home is faster |

## What isn't supported

- **WSL 1** (setup refuses it, with `wsl --set-version <distribution> 2`).
- **A Windows build of MetaRush**, and MetaRush's folder, its database or its storage on a Windows drive.
- **Opening MetaRush from other computers through WSL's own address** or mirrored networking: it stays on this PC;
  use Tailscale.
- **Apple Vision** and the Mac app (`MetaRush.app`).
- **A Windows editing app opening a timeline's footage directly**: relink it once (above).
- Waiting for the battery (heavy work waits on a low battery on a Mac or a Linux laptop) sees only what WSL shows
  Ubuntu.

## Checking it on a real PC

MetaRush's tests play WSL with stand-in files (`internal/wsl`), and CI runs the smoke test below in Ubuntu 24.04 on
WSL 2 on a Windows runner when anything about WSL changes. On a real PC, run it inside Ubuntu from a checkout of
MetaRush (it uses the `metarush` on your PATH, or `METARUSH=<program>`, and never your own MetaRush's settings):

```bash
scripts/wsl-smoke.sh            # what MetaRush detects, doctor, and Windows reaching Ubuntu's 127.0.0.1
scripts/wsl-smoke.sh --setup    # also a throwaway MetaRush: setup with the database on this computer (a download of
                                # about 85 MB), a Windows path, the real opener (a browser window opens), then
                                # everything removed
```

It checks what MetaRush detects (WSL 2, the distribution, interop, systemd) and doctor's WINDOWS checks, and —
through PowerShell — that Windows reaches a server on Ubuntu's 127.0.0.1. With `--setup` it also sets up a MetaRush of
its own in a temporary folder (the database on this computer on WSL's kernel, no components, no background service),
asks which program would open the dashboard, has Windows ask the dashboard for its health at 127.0.0.1 (and at
localhost, which WSL forwards too), adds a sound file by its `C:\Users\Public\…` path (a dry run), then opens a
sign-in link with `&` and `%` in it through the real opener: Windows' default browser opens (a window appears) and a
stand-in on the dashboard's port checks the address arrived exactly as MetaRush printed it. Then it stops and removes
it all.
Then by hand, in Edge and Chrome:

1. `metarush dashboard`: the dashboard opens in Windows' default browser, signed in.
2. Drag a clip from File Explorer onto a project; add one from the terminal by its `C:\…` path: both arrive, and the
   terminal says the second came from the Windows side.
3. Set a watched folder in `C:\Users\<you>\Videos` from the dashboard (type it, or Browse folders → Windows folder);
   save a recording there: it is added within seconds.
4. Close every Ubuntu window and wait a minute: with `instanceIdleTimeout=-1`, the dashboard still answers.
5. Render a review copy, and deliver it to `C:\Users\<you>\Desktop`.

Write down the Windows, WSL (`wsl --version`) and Ubuntu versions, and what differed.
