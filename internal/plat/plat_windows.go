//go:build windows

package plat

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
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

// Facts on Windows carries only what the judge can use without ACL walks:
// link count and ownership are left neutral.
func Facts(fi fs.FileInfo) FileFacts {
	return FileFacts{UID: Who(), Nlink: 1, Device: fi.Mode()&fs.ModeDevice != 0}
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

func KillGroup(pgid int, hard bool) {}

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
