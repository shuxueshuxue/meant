//go:build windows

package plat

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// On Windows a command's process group is a job object. A process cannot
// leave the job it was born into, so like a Unix process group it survives
// `&`, `start` and the parent exiting; unlike one, its membership can be
// asked about from outside. Jobs are named after the command's shell pid, so
// later commands of the session can open them again. A name lasts only while
// some process holds a handle to the job, so a runner leaving members behind
// hands each of them one (Outlive).

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObject          = kernel32.NewProc("CreateJobObjectW")
	openJobObject            = kernel32.NewProc("OpenJobObjectW")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	terminateJobObject       = kernel32.NewProc("TerminateJobObject")
	isProcessInJob           = kernel32.NewProc("IsProcessInJob")
	duplicateHandle          = kernel32.NewProc("DuplicateHandle")
	ntResumeProcess          = syscall.NewLazyDLL("ntdll.dll").NewProc("NtResumeProcess")
)

const (
	createSuspended   = 0x4
	jobQuery          = 0x4
	jobTerminate      = 0x8
	processQueryLimit = 0x1000
	processDupHandle  = 0x40
	sameAccess        = 0x2
	processAll        = 0x1F0FFF
)

func jobName(group int) *uint16 {
	p, _ := syscall.UTF16PtrFromString(`Local\meant-` + strconv.Itoa(group))
	return p
}

// StartSuspended makes cmd start without running, so that JoinGroup can put
// it in its job before it can start anything.
func StartSuspended(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
}

// JoinGroup creates the job for a process started suspended, puts the
// process in it and lets it run. The job handle is kept for the life of the
// runner.
func JoinGroup(pid int) {
	h, err := syscall.OpenProcess(processAll, false, uint32(pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	if job, _, _ := createJobObject.Call(0, uintptr(unsafe.Pointer(jobName(pid)))); job != 0 {
		assignProcessToJobObject.Call(job, uintptr(h))
	}
	ntResumeProcess.Call(uintptr(h))
}

var jobs = map[int]uintptr{}

func openJob(group int) uintptr {
	if h, ok := jobs[group]; ok {
		return h
	}
	h, _, _ := openJobObject.Call(jobQuery|jobTerminate, 0, uintptr(unsafe.Pointer(jobName(group))))
	jobs[group] = h
	return h
}

// InGroup reports whether a process belongs to the job of a group.
func InGroup(p Proc, group int) bool {
	if group <= 1 {
		return false
	}
	job := openJob(group)
	if job == 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimit, false, uint32(p.PID))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var in int32
	r, _, _ := isProcessInJob.Call(uintptr(h), job, uintptr(unsafe.Pointer(&in)))
	return r != 0 && in != 0
}

// Outlive keeps a group's job nameable after the runner exits, by giving
// every process still in it a handle to the job. The handle closes when that
// process exits, so the name goes with the last member.
func Outlive(group int, procs map[int]Proc) {
	job := openJob(group)
	if job == 0 {
		return
	}
	self, _ := syscall.GetCurrentProcess()
	for _, p := range procs {
		if !InGroup(p, group) {
			continue
		}
		h, err := syscall.OpenProcess(processDupHandle, false, uint32(p.PID))
		if err != nil {
			continue
		}
		var dup uintptr
		duplicateHandle.Call(uintptr(self), job, uintptr(h), uintptr(unsafe.Pointer(&dup)), 0, 0, sameAccess)
		syscall.CloseHandle(h)
	}
}

// KillGroup ends every process in a group's job.
func KillGroup(group int, hard bool) {
	if job := openJob(group); job != 0 {
		terminateJobObject.Call(job, 1)
	}
}

// NativePID turns a pid as a Git Bash (MSYS) program names it into the
// Windows pid it stands for. MSYS keeps its own pid space; the mapping is
// readable only from inside it, at /proc/<pid>/winpid. A pid that MSYS does
// not know is taken to be a Windows pid already.
func NativePID(pid int) int {
	cat := LookReal("cat")
	if cat == "" {
		return pid
	}
	out, err := exec.Command(cat, filepath.ToSlash("/proc/"+strconv.Itoa(pid)+"/winpid")).Output()
	if err != nil {
		return pid
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
		return n
	}
	return pid
}
