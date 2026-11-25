//go:build windows

package plat

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func stateRoot() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "meant")
	}
	return filepath.Join(RealHome(), ".meant")
}

func exeNames(name string) []string {
	if filepath.Ext(name) != "" {
		return []string{name}
	}
	return []string{name + ".exe", name + ".cmd", name + ".bat", name}
}

func isExec(fi fs.FileInfo) bool { return true }

// Windows identities are SIDs. The judge compares small integers, so each
// SID seen gets a stable number here, and keeps its account name for people.
var (
	sids  = map[string]int{}
	names = map[int]string{}
)

func uidOf(sid *syscall.SID) int {
	str, err := sid.String()
	if err != nil {
		return -1
	}
	if n, ok := sids[str]; ok {
		return n
	}
	n := len(sids) + 1000
	sids[str] = n
	if acct, dom, _, err := sid.LookupAccount(""); err == nil {
		names[n] = dom + `\` + acct
	} else {
		names[n] = str
	}
	return n
}

// Who is the account this process runs as.
func Who() int {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return -1
	}
	defer t.Close()
	u, err := t.GetTokenUser()
	if err != nil {
		return -1
	}
	return uidOf(u.User.Sid)
}

// RealHome is the profile directory of the account, not %USERPROFILE%.
func RealHome() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	h, _ := os.UserHomeDir()
	return h
}

func UserName(uid int) string {
	if n, ok := names[uid]; ok {
		return n
	}
	return "uid " + strconv.Itoa(uid)
}

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	getNamedSecurityInfo = advapi32.NewProc("GetNamedSecurityInfoW")
)

// Facts reads the owner from the file's security descriptor and the link
// count from its handle: two calls per file, which is what a recursive
// judgement costs on Windows.
func Facts(path string, fi fs.FileInfo) FileFacts {
	f := FileFacts{UID: Who(), Nlink: 1, Device: fi.Mode()&fs.ModeDevice != 0}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return f
	}
	const seFileObject, ownerInfo = 1, 1
	var owner *syscall.SID
	var sd uintptr
	if r, _, _ := getNamedSecurityInfo.Call(uintptr(unsafe.Pointer(p)), seFileObject, ownerInfo,
		uintptr(unsafe.Pointer(&owner)), 0, 0, 0, uintptr(unsafe.Pointer(&sd))); r == 0 && owner != nil {
		f.UID = uidOf(owner)
		syscall.LocalFree(syscall.Handle(sd))
	}
	const readAttributes = 0x80
	h, err := syscall.CreateFile(p, readAttributes, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		var info syscall.ByHandleFileInformation
		if syscall.GetFileInformationByHandle(h, &info) == nil {
			f.Nlink = uint64(info.NumberOfLinks)
			f.Dev = uint64(info.VolumeSerialNumber)
		}
		syscall.CloseHandle(h)
	}
	return f
}

// IsFSRoot reports whether dir is a drive root or a reparse point (junction,
// mount point): the places a recursive delete leaves the tree it was aimed at.
func IsFSRoot(dir string) bool {
	if filepath.Dir(dir) == dir {
		return true
	}
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&fs.ModeIrregular != 0 {
		return true
	}
	return false
}

func SystemDirs() []string {
	root := os.Getenv("SystemDrive")
	if root == "" {
		root = "C:"
	}
	return []string{root + `\Windows`, root + `\Program Files`, root + `\Program Files (x86)`, root + `\ProgramData`}
}

func TempDirs() []string { return []string{os.TempDir()} }

// Procs walks the toolhelp snapshot. Windows gives no way to read another
// process's environment cheaply, so markers stay empty and ownership falls
// back to ancestry from the command's runner.
func Procs() map[int]Proc {
	out := map[int]Proc{}
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		pid := int(e.ProcessID)
		out[pid] = Proc{PID: pid, PPID: int(e.ParentProcessID), UID: Who(), Cmd: syscall.UTF16ToString(e.ExeFile[:])}
	}
	return out
}

// Exec cannot replace the process image on Windows; it runs the real program
// as a child with the same streams and exits with its status.
func Exec(path string, argv []string) error {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func Kill(pid int, hard bool) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// Link makes dst an alias of the meant binary: a hard link where the volume
// allows it, a copy otherwise.
func Link(self, dst string) error {
	if err := os.Link(self, dst); err == nil {
		return nil
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func ShimName(name string) string {
	if strings.HasSuffix(name, ".exe") {
		return name
	}
	return name + ".exe"
}
