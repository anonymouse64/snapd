// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2018 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package strace

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// ExeRuntime is the runtime of an individual executable
type ExeRuntime struct {
	Start    time.Duration
	Exe      string
	TotalSec time.Duration
}

// ExecveTiming measures the execve calls timings under strace. This is
// useful for performance analysis. It keeps the N slowest samples.
type ExecveTiming struct {
	TotalTime   float64
	exeRuntimes []ExeRuntime
	indent      string

	pidChildren *pidChildTracker

	nSlowestSamples int
}

// NewExecveTiming returns a new ExecveTiming struct that keeps
// the given amount of the slowest exec samples.
// if nSlowestSamples is equal to 0, all exec samples are kept
func NewExecveTiming(nSlowestSamples int) *ExecveTiming {
	return &ExecveTiming{nSlowestSamples: nSlowestSamples}
}

func (stt *ExecveTiming) addExeRuntime(start float64, exe string, totalSec float64) {

	stt.exeRuntimes = append(stt.exeRuntimes, ExeRuntime{
		Start:    time.Duration(start * float64(time.Second)),
		Exe:      exe,
		TotalSec: time.Duration(totalSec * float64(time.Second)),
	})
	if stt.nSlowestSamples > 0 {
		stt.prune()
	}
}

// prune() ensures the number of exeRuntimes stays with the nSlowestSamples
// limit
func (stt *ExecveTiming) prune() {
	for len(stt.exeRuntimes) > stt.nSlowestSamples {
		fastest := 0
		for idx, rt := range stt.exeRuntimes {
			if rt.TotalSec < stt.exeRuntimes[fastest].TotalSec {
				fastest = idx
			}
		}
		// delete fastest element
		stt.exeRuntimes = append(stt.exeRuntimes[:fastest], stt.exeRuntimes[fastest+1:]...)
	}
}

// Display shows the final exec timing output
func (stt *ExecveTiming) Display(w io.Writer) {
	if len(stt.exeRuntimes) == 0 {
		return
	}

	fmt.Fprintf(w, "%d exec calls during snap run:\n", len(stt.exeRuntimes))
	fmt.Fprintf(w, "\tStart\tStop\tElapsed\tExec\n")

	sort.Slice(stt.exeRuntimes, func(i, j int) bool {
		return stt.exeRuntimes[i].Start < stt.exeRuntimes[j].Start
	})

	// TODO: this shows processes linearly, when really I think we want a
	// tree/forest style output showing forked processes indented underneath the
	// parent, with exec'd processes lined up with their previous executable
	// but note that doing so in the most generic case isn't neat since you can
	// have processes that are forked much later than others and will be aligned
	// with previous executables much earlier in the output
	for _, rt := range stt.exeRuntimes {
		relativeStart := rt.Start - stt.exeRuntimes[0].Start
		fmt.Fprintf(w,
			"\t%d\t%d\t%d\t%s\n",
			int64(relativeStart/time.Microsecond),
			int64((relativeStart+rt.TotalSec)/time.Microsecond),
			int64(rt.TotalSec/time.Microsecond),
			rt.Exe,
		)
	}

	fmt.Fprintf(w, "Total time: %2.3fs\n", stt.TotalTime)
}

type childPidStart struct {
	start float64
	pid   string
}

type pidChildTracker struct {
	pidToChildrenPIDs map[string][]childPidStart
}

func newPidChildTracker() *pidChildTracker {
	return &pidChildTracker{
		pidToChildrenPIDs: make(map[string][]childPidStart),
	}
}

func (pct *pidChildTracker) Add(pid string, child string, start float64) {
	if _, ok := pct.pidToChildrenPIDs[pid]; !ok {
		pct.pidToChildrenPIDs[pid] = []childPidStart{}
	}
	pct.pidToChildrenPIDs[pid] = append(pct.pidToChildrenPIDs[pid], childPidStart{start: start, pid: child})
}

type exeStart struct {
	start float64
	exe   string
}

type pidTracker struct {
	pidToExeStart map[string]exeStart
}

func newPidTracker() *pidTracker {
	return &pidTracker{
		pidToExeStart: make(map[string]exeStart),
	}
}

func (pt *pidTracker) Get(pid string) (startTime float64, exe string) {
	if exeStart, ok := pt.pidToExeStart[pid]; ok {
		return exeStart.start, exeStart.exe
	}
	return 0, ""
}

func (pt *pidTracker) Add(pid string, startTime float64, exe string) {
	pt.pidToExeStart[pid] = exeStart{start: startTime, exe: exe}
}

func (pt *pidTracker) Del(pid string) {
	delete(pt.pidToExeStart, pid)
}

// TODO: can execve calls be "interrupted" like clone() below?
// lines look like:
// PID   TIME              SYSCALL
// 17363 1542815326.700248 execve("/snap/brave/44/usr/bin/update-mime-database", ["update-mime-database", "/home/egon/snap/brave/44/.local/"...], 0x1566008 /* 69 vars */) = 0
var execveRE = regexp.MustCompile(`([0-9]+)\ +([0-9.]+) execve\(\"([^"]+)\"`)

// lines look like:
// PID   TIME              SYSCALL
// 14157 1542875582.816782 execveat(3, "", ["snap-update-ns", "--from-snap-confine", "test-snapd-tools"], 0x7ffce7dd6160 /* 0 vars */, AT_EMPTY_PATH) = 0
var execveatRE = regexp.MustCompile(`([0-9]+)\ +([0-9.]+) execveat\(.*\["([^"]+)"`)

// lines look like (both SIGTERM and SIGCHLD need to be handled):
// PID   TIME                  SIGNAL
// 17559 1542815330.242750 --- SIGCHLD {si_signo=SIGCHLD, si_code=CLD_EXITED, si_pid=17643, si_uid=1000, si_status=0, si_utime=0, si_stime=0} ---
var sigChldTermRE = regexp.MustCompile(`[0-9]+\ +([0-9.]+).*SIG(CHLD|TERM)\ {.*si_pid=([0-9]+),`)

// lines look like
// PID   TIME                            SIGNAL
// 20882 1573257274.988650 +++ killed by SIGKILL +++
var sigkillRE = regexp.MustCompile(`([0-9]+)\ +([0-9.]+) \+\+\+ killed by SIGKILL \+\+\+`)

// lines look like (note the SIGCHLD flag must be present identifying a new process, not thread is being created):
// PID   TIME                                                                                    SIGCHLD flag                            NEW-PID
// 47727 1573226458.207845 clone(child_stack=NULL, flags=CLONE_CHILD_CLEARTID|CLONE_CHILD_SETTID|SIGCHLD, child_tidptr=0x7f5cf2e4aed0) = 47769
// various other forms that also match: (i.e. the SIGCHLD flag moving around) and if clone gets interrupted:
// 47839 1573226458.649941 <... clone resumed> child_stack=NULL, flags=CLONE_CHILD_CLEARTID|CLONE_CHILD_SETTID|SIGCHLD, child_tidptr=0x7f19d6fdca10) = 47840
// 47727 1573226458.207845 clone(child_stack=NULL, flags=CLONE_CHILD_CLEARTID|CLONE_CHILD_SETTID|SIGCHLD, child_tidptr=0x7f5cf2e4aed0) = 47769
// 47727 1573226458.207845 clone(child_stack=NULL, flags=SIGCHLD|CLONE_CHILD_CLEARTID|CLONE_CHILD_SETTID, child_tidptr=0x7f5cf2e4aed0) = 47769
// 47727 1573226458.207845 clone(child_stack=NULL, flags=CLONE_CHILD_CLEARTID|SIGCHLD|CLONE_CHILD_SETTID, child_tidptr=0x7f5cf2e4aed0) = 47769
// 47839 1573226458.649941 <... clone resumed> child_stack=NULL, flags=SIGCHLD|CLONE_CHILD_CLEARTID|CLONE_CHILD_SETTID, child_tidptr=0x7f19d6fdca10) = 47840
// 47839 1573226458.649941 <... clone resumed> child_stack=NULL, flags=CLONE_CHILD_CLEARTID|SIGCHLD|CLONE_CHILD_SETTID, child_tidptr=0x7f19d6fdca10) = 47840
// 47839 1573226458.649941 <... clone resumed> child_stack=NULL, flags=CLONE_CHILD_CLEARTID|CLONE_CHILD_SETTID|SIGCHLD, child_tidptr=0x7f19d6fdca10) = 47840
// this should not match the following which is creating a thread not a new process:
// 47727 1573226458.893288 clone(child_stack=0x7fc562a7fbf0, flags=CLONE_VM|CLONE_FS|CLONE_FILES|CLONE_SIGHAND|CLONE_THREAD|CLONE_SYSVSEM|CLONE_SETTLS|CLONE_PARENT_SETTID|CLONE_CHILD_CLEARTID, parent_tidptr=0x7fc562a809d0, tls=0x7fc562a80700, child_tidptr=0x7fc562a809d0) = 47849
var cloneRE = regexp.MustCompile(`([0-9]+)\ +([0-9.]+) (?:<\.\.\. ){0,1}clone(?:\(| resumed>).*flags=(?:(?:[A-Z_]+\|)*SIGCHLD).*\) = ([0-9]+)`)

func handleExecMatch(trace *ExecveTiming, pt *pidTracker, match []string) error {
	if len(match) == 0 {
		return nil
	}
	// the pid of the process that does the execve{,at}()
	pid := match[1]
	execStart, err := strconv.ParseFloat(match[2], 64)
	if err != nil {
		return err
	}
	exe := match[3]

	// deal with subsequent execve()
	if start, exe := pt.Get(pid); exe != "" {
		trace.addExeRuntime(start, exe, execStart-start)
	}
	pt.Add(pid, execStart, exe)
	return nil
}

func handleSignalMatch(trace *ExecveTiming, pt *pidTracker, match []string) error {
	if len(match) == 0 {
		return nil
	}
	sigTime, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return err
	}
	sigPid := match[3]

	if start, exe := pt.Get(sigPid); exe != "" {
		trace.addExeRuntime(start, exe, sigTime-start)
		pt.Del(sigPid)
	}
	return nil
}

func handleSigkillMatch(trace *ExecveTiming, pt *pidTracker, match []string) error {
	if len(match) == 0 {
		return nil
	}
	pid := match[1]
	sigTime, err := strconv.ParseFloat(match[2], 64)
	if err != nil {
		return err
	}

	if start, exe := pt.Get(pid); exe != "" {
		trace.addExeRuntime(start, exe, sigTime-start)
		pt.Del(pid)
	}
	return nil
}

func handleCloneMatch(trace *ExecveTiming, pct *pidChildTracker, match []string) error {
	if len(match) == 0 {
		return nil
	}
	// the pid of the parent process clone()ing a new child
	ppid := match[1]

	// the time the child was created
	execStart, err := strconv.ParseFloat(match[2], 64)
	if err != nil {
		return err
	}

	// the pid of the new child
	pid := match[3]
	pct.Add(ppid, pid, execStart)
	return nil
}

// TraceExecveTimings will read an strace log and produce a timing report of the
// n slowest exec's
func TraceExecveTimings(straceLog string, nSlowest int) (*ExecveTiming, error) {
	slog, err := os.Open(straceLog)
	if err != nil {
		return nil, err
	}
	defer slog.Close()

	// pidTracker maps the "pid" string to the executable
	pidTracker := newPidTracker()

	// pidChildTracker := newPidChildTracker()

	var line string
	var start, end float64
	var startPID, endPID int
	trace := NewExecveTiming(nSlowest)
	r := bufio.NewScanner(slog)
	for r.Scan() {
		line = r.Text()
		if start == 0.0 {
			if _, err := fmt.Sscanf(line, "%d %f ", &startPID, &start); err != nil {
				return nil, fmt.Errorf("cannot parse start of exec profile: %s", err)
			}
		}
		// handleExecMatch looks for execve{,at}() calls and
		// uses the pidTracker to keep track of execution of
		// things. Because of fork() we may see many pids and
		// within each pid we can see multiple execve{,at}()
		// calls.
		// An example of pids/exec transitions:
		// $ snap run --trace-exec test-snapd-sh -c "/bin/true"
		//    pid 20817 execve("snap-confine")
		//    pid 20817 execve("snap-exec")
		//    pid 20817 execve("/snap/test-snapd-sh/x2/bin/sh")
		//    pid 20817 execve("/bin/sh")
		//    pid 2023  execve("/bin/true")
		match := execveRE.FindStringSubmatch(line)
		if err := handleExecMatch(trace, pidTracker, match); err != nil {
			return nil, err
		}
		match = execveatRE.FindStringSubmatch(line)
		if err := handleExecMatch(trace, pidTracker, match); err != nil {
			return nil, err
		}
		// handleSignalMatch looks for SIG{CHLD,TERM} signals and
		// maps them via the pidTracker to the execve{,at}() calls
		// of the terminating PID to calculate the total time of
		// an execve{,at}() call.
		match = sigChldTermRE.FindStringSubmatch(line)
		if err := handleSignalMatch(trace, pidTracker, match); err != nil {
			return nil, err
		}

		// handleSignalMatch looks for SIGKILL signals for processes and uses
		// the time that SIGKILL happens to calculate the total time of an
		// execve{,at}() call.
		match = sigkillRE.FindStringSubmatch(line)
		if err := handleSigkillMatch(trace, pidTracker, match); err != nil {
			return nil, err
		}

		// handleCloneMatch looks for clone() with SIGCHLD indicating a new
		// process is being created that we will use to associated exe's with
		// it's parent when displaying
		// match = cloneRE.FindStringSubmatch(line)
		// if err := handleCloneMatch(trace, pidChildTracker, match); err != nil {
		// 	return nil, err
		// }
	}
	if _, err := fmt.Sscanf(line, "%v %f", &endPID, &end); err != nil {
		return nil, fmt.Errorf("cannot parse end of exec profile: %s", err)
	}

	// handle processes which don't execute anything
	if startPID == endPID {
		pidString := strconv.Itoa(startPID)
		if start, exe := pidTracker.Get(pidString); exe != "" {
			trace.addExeRuntime(start, exe, end-start)
			pidTracker.Del(pidString)
		}
	}
	trace.TotalTime = end - start
	// trace.pidChildren = pidChildTracker

	if r.Err() != nil {
		return nil, r.Err()
	}

	return trace, nil
}
