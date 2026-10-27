package localdb

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Archive is one pinned download of MongoDB Community Server.
type Archive struct {
	URL    string // on MongoDB's own download server (fastdl.mongodb.org)
	SHA256 string // of the archive as downloaded
}

// Pin is the version of the database MetaRush sets up, with its archive for
// each kind of computer.
type Pin struct {
	Version  string
	Archives map[string]Archive // target (see Target) → archive
	// SizeMB is about how much the download is, for the person's sake.
	SizeMB int
}

// Current is the version this build of MetaRush sets up (D254): MongoDB 8.0,
// the release the repository's development and test databases run. The
// archives and their SHA-256 are MongoDB's own, from its download catalogue
// (downloads.mongodb.org/full.json, checked 2026-10-25 and again 2026-10-27
// against the .sha256 file fastdl.mongodb.org publishes next to each archive).
// It is the newest 8.0 (27 Oct 2026); no 8.0 runs on Linux kernels 6.19
// through 7.0.13, which get Older instead (D339). 8.0.35, when MongoDB
// publishes it, also starts on Ubuntu kernels that carry the fix but still
// call themselves 7.0.0 (KernelRefuses). A newer pin is an upgrade a person
// starts (`metarush database upgrade`); it never happens by itself.
var Current = Pin{
	Version: "8.0.34",
	SizeMB:  85,
	Archives: map[string]Archive{
		"darwin/amd64": {URL: "https://fastdl.mongodb.org/osx/mongodb-macos-x86_64-8.0.34.tgz",
			SHA256: "3d391b8c41df069671afaa88ca21def057178aecce1813437539143b71f28152"},
		"darwin/arm64": {URL: "https://fastdl.mongodb.org/osx/mongodb-macos-arm64-8.0.34.tgz",
			SHA256: "3255cbf2e70cc5de04220046e006067d20068656d1bfa674828768a51d20d5a6"},
		"linux/amd64/amazon2023": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-amazon2023-8.0.34.tgz",
			SHA256: "b9fe750304de8a2f9e7f1c306e92c718a4c22d0c22d9c5ba20c8ab8426e899f7"},
		"linux/amd64/debian12": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-debian12-8.0.34.tgz",
			SHA256: "0e869e044162d101d4ee468e89bc139628c4c9e041bdcdaec19bf99331827215"},
		"linux/amd64/rhel10": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-rhel10-8.0.34.tgz",
			SHA256: "e35cc984522d24b922b0c7d3d1c81bee99060a72adf5e1820b312e9cbb23bdd8"},
		"linux/amd64/rhel8": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-rhel8-8.0.34.tgz",
			SHA256: "675c97f72f57d64d177006f82aabccda68730ed7a13e7c27d4244cfa1cf729e8"},
		"linux/amd64/rhel93": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-rhel93-8.0.34.tgz",
			SHA256: "6caa90892d5b3693789d1955761237ac686ac0a8b7bede201aacc25dc20be677"},
		"linux/amd64/suse15": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-suse15-8.0.34.tgz",
			SHA256: "3d3b418c180318bdaf20221820fde72a858333cbccd926bfe49b271cc55fb2a3"},
		"linux/amd64/ubuntu2004": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2004-8.0.34.tgz",
			SHA256: "f85e7aafabc52231e00ac05762e5a612f0a8e120364ab0d73a43ad00cade5ba3"},
		"linux/amd64/ubuntu2204": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2204-8.0.34.tgz",
			SHA256: "8eef2a821651ac20f732b6636731a51b407675674ced8d7f1f1578ebc7aced43"},
		"linux/amd64/ubuntu2404": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2404-8.0.34.tgz",
			SHA256: "3e55cdcf7013fbc805d29d8195230df4b23c7688733ee15ad58ac6bc2a416611"},
		"linux/arm64/amazon2023": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-amazon2023-8.0.34.tgz",
			SHA256: "eee86a2859ea1d2ea639395e47c1c843001a394db418d888373fe6d333ce53e9"},
		"linux/arm64/rhel10": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-rhel10-8.0.34.tgz",
			SHA256: "91ab354eed1859ca3e768c27b91c8a653c0f4b255fb82c3e45bab2f2422986b7"},
		"linux/arm64/rhel8": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-rhel8-8.0.34.tgz",
			SHA256: "d34edb8e846c284983f6e62321bb624c82d27ebc58895b4bb84a2535a8d9b7b9"},
		"linux/arm64/rhel93": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-rhel93-8.0.34.tgz",
			SHA256: "f13892a11eb5b38ecb45b8079edb7b77f9724b63873c3a34f7fd4d6fa0d12f94"},
		"linux/arm64/ubuntu2004": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-ubuntu2004-8.0.34.tgz",
			SHA256: "35db4afa4fa391b6d19a009f8f73425eb284512f9931259386cbf060a3185b6b"},
		"linux/arm64/ubuntu2204": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-ubuntu2204-8.0.34.tgz",
			SHA256: "fe918ca853000d5b48fcd3b2bc6119c72f4aab9e2b04a9a10e42923cb3aef6a4"},
		"linux/arm64/ubuntu2404": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-ubuntu2404-8.0.34.tgz",
			SHA256: "52655dd90adf0dbcbb87011075233285f44e262048ea30a9a48ad2a834c67736"},
	},
}

// Older is MongoDB 7.0, which a new database on this computer gets instead of
// Current when the computer's Linux kernel is one MongoDB 8 refuses (6.19
// through 7.0.13, see kernel.go; D339). MetaRush needs nothing from 8.0 (its
// tests run on 7.0 where CI's kernel refuses 8), and once the kernel lets 8.0
// run, `metarush database upgrade` moves the database on to Current (one
// release series, never back). MongoDB supports 7.0 until 31 August 2027.
// MongoDB makes no 7.0 for Ubuntu 24.04 or RHEL 9.3: those computers get the
// Ubuntu 22.04 and RHEL 9.0 builds (they need only libraries those systems
// have: glibc, OpenSSL 3, libcurl). Archives and SHA-256 from MongoDB's
// download catalogue (downloads.mongodb.org/full.json), checked 2026-10-27
// against the .sha256 file fastdl.mongodb.org publishes next to each archive.
var Older = Pin{
	Version: "7.0.45",
	SizeMB:  85,
	Archives: map[string]Archive{
		"linux/amd64/amazon2023": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-amazon2023-7.0.45.tgz",
			SHA256: "2dcbaf1443a95744727a5c73ef6fb473cd49d1ca39bd4b75a581fe1a7dd95028"},
		"linux/amd64/debian12": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-debian12-7.0.45.tgz",
			SHA256: "e97e79211a7106a065d9119039ead401f32cfbbfa1681977fdeba4b4a4bf1f63"},
		"linux/amd64/rhel10": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-rhel10-7.0.45.tgz",
			SHA256: "8a1141175d7f7cd0bfe3f009ff18ba4e312844420330b16ef8eda90f82d31b92"},
		"linux/amd64/rhel8": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-rhel8-7.0.45.tgz",
			SHA256: "6c5729b53bfee1de1ad6329ce60049641a1bdebfe06b0aa8a2c1f558cec808c3"},
		"linux/amd64/rhel93": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-rhel90-7.0.45.tgz",
			SHA256: "975f59b27245c347d5c70d67e63652ac62a812de25c98e549754470d2817faba"},
		"linux/amd64/suse15": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-suse15-7.0.45.tgz",
			SHA256: "9c144e69807204c18520474b5139de0bf323758398f0163dd0849259efed6302"},
		"linux/amd64/ubuntu2004": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2004-7.0.45.tgz",
			SHA256: "9a9c20d284f91f12ba142b730085a97db7e86ad8d9676d71f88a0f0f005cfd2e"},
		"linux/amd64/ubuntu2204": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2204-7.0.45.tgz",
			SHA256: "c2eec1e4aaee967838430ac57fc7d46c22f566c718eda6033e32add80d506f8b"},
		"linux/amd64/ubuntu2404": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2204-7.0.45.tgz",
			SHA256: "c2eec1e4aaee967838430ac57fc7d46c22f566c718eda6033e32add80d506f8b"},
		"linux/arm64/amazon2023": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-amazon2023-7.0.45.tgz",
			SHA256: "5550fad541622972236c692a27996dde264036efb2be07c3e6ffd87e9e6613d9"},
		"linux/arm64/rhel10": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-rhel10-7.0.45.tgz",
			SHA256: "d9a0d0470ec3bf914e9e75f4c6252d7e2cc4b511d3f77c62d57f933c0747f164"},
		"linux/arm64/rhel8": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-rhel8-7.0.45.tgz",
			SHA256: "d7bd5f8c54cfa6a86e48ada336a30a6c3f6ae5c28f57e8276ffd04a219751062"},
		"linux/arm64/rhel93": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-rhel90-7.0.45.tgz",
			SHA256: "21cd7ee218515a3e929013bb5dbf0c5b9883f9553b9063ce4c8e611f8a58f386"},
		"linux/arm64/ubuntu2004": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-ubuntu2004-7.0.45.tgz",
			SHA256: "e3dae6e60b0ce606b0c6bc1670d7a58b1fbcfbb0a846b5e0d0119ad4aedfd742"},
		"linux/arm64/ubuntu2204": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-ubuntu2204-7.0.45.tgz",
			SHA256: "dfdfb314e2f6fc48fe01588d39d2b44b168fa589b5df12d354a0dbd68e71f95a"},
		"linux/arm64/ubuntu2404": {URL: "https://fastdl.mongodb.org/linux/mongodb-linux-aarch64-ubuntu2204-7.0.45.tgz",
			SHA256: "dfdfb314e2f6fc48fe01588d39d2b44b168fa589b5df12d354a0dbd68e71f95a"},
	},
}

// Target names the archive this computer needs: "darwin/arm64" on a Mac, or
// "linux/<arch>/<distribution>" on Linux, where MongoDB builds for each
// distribution. ok is false when MongoDB has no build for it (the person
// uses a database elsewhere: MongoDB Atlas, or their own server).
func Target() (string, bool) {
	rel := ""
	if runtime.GOOS == "linux" {
		rel = readOSRelease("/etc/os-release")
	}
	return TargetFor(runtime.GOOS, runtime.GOARCH, rel)
}

// TargetFor is Target for a given system: osRelease is the text of
// /etc/os-release on Linux.
func TargetFor(goos, goarch, osRelease string) (string, bool) {
	switch goos {
	case "darwin":
		if goarch == "arm64" || goarch == "amd64" {
			return "darwin/" + goarch, true
		}
		return "", false
	case "linux":
	default:
		return "", false
	}
	f := parseOSRelease(osRelease)
	id, like, ver := f["ID"], " "+f["ID_LIKE"]+" ", f["VERSION_ID"]
	major, minor := versionParts(ver)
	dist := ""
	switch {
	case id == "ubuntu" || f["UBUNTU_CODENAME"] != "" && strings.Contains(like, " ubuntu "):
		// Ubuntu and the distributions built on it (Mint, Pop!_OS) say which
		// Ubuntu they follow.
		switch code := f["UBUNTU_CODENAME"]; {
		case code == "noble", id == "ubuntu" && (major > 24 || major == 24 && minor >= 4):
			dist = "ubuntu2404"
		case code == "jammy", id == "ubuntu" && (major > 22 || major == 22 && minor >= 4):
			dist = "ubuntu2204"
		case code == "focal", id == "ubuntu" && major >= 20:
			dist = "ubuntu2004"
		}
	case id == "debian":
		if major >= 12 {
			dist = "debian12"
		}
	case id == "amzn":
		if major >= 2023 {
			dist = "amazon2023"
		}
	case id == "rhel" || id == "rocky" || id == "almalinux" || id == "centos" || id == "ol" || strings.Contains(like, " rhel "):
		switch {
		case major >= 10:
			dist = "rhel10"
		case major == 9:
			dist = "rhel93"
		case major == 8:
			dist = "rhel8"
		}
	case id == "sles" || id == "opensuse-leap":
		if major == 15 {
			dist = "suse15"
		}
	}
	if dist == "" {
		return "", false
	}
	key := "linux/" + goarch + "/" + dist
	return key, true
}

// For returns the pinned archive for a target.
func (p Pin) For(target string) (Archive, bool) {
	a, ok := p.Archives[target]
	return a, ok && a.URL != "" && a.SHA256 != ""
}

// Series is a version's release series ("8.0" for 8.0.34): what MongoDB's
// feature compatibility version names.
func Series(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

func versionParts(v string) (int, int) {
	a, b, _ := strings.Cut(v, ".")
	major, _ := strconv.Atoi(a)
	minor, _ := strconv.Atoi(strings.SplitN(b, ".", 2)[0])
	return major, minor
}

func readOSRelease(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseOSRelease(s string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}
