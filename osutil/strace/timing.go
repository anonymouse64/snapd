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
	"strconv"
	"github.com/guillermo/go.proc-stat"
	"sort"
)

// ExeRuntime is the runtime of an individual executable
type ExeRuntime struct {
	Processed bool
	PID int
	PPID int
	Start float64
	Exe string
	// FIXME: move to time.Duration
	TotalSec float64
}

// ExecveTiming measures the execve calls timings under strace. This is
// useful for performance analysis. It keeps the N slowest samples.
type ExecveTiming struct {
	TotalTime   float64
	exeRuntimes []ExeRuntime
	indent      string

	nSlowestSamples int
}

// NewExecveTiming returns a new ExecveTiming struct that keeps
// the given amount of the slowest exec samples.
func NewExecveTiming(nSlowestSamples int) *ExecveTiming {
	return &ExecveTiming{nSlowestSamples: nSlowestSamples}
}

func (stt *ExecveTiming) addExeRuntime(pid int, ppid int, start float64, exe string, totalSec float64) {
	stt.exeRuntimes = append(stt.exeRuntimes, ExeRuntime{
		Processed: false,
		PID:       pid,
		PPID:      ppid,
		Start:     start,
		Exe:       exe,
		TotalSec:  totalSec,
	})
	stt.prune()
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

func (stt *ExecveTiming) PrintRt(w io.Writer, thisrt *ExeRuntime) {
	stt.indent += "\t"
	fmt.Fprintf(w, stt.indent + "%2.3f  %2.3f  %.5d  %.5d  %s\n", thisrt.Start-stt.exeRuntimes[0].Start, thisrt.TotalSec, thisrt.PPID, thisrt.PID, thisrt.Exe)
	thisrt.Processed = true

	for i, _ := range stt.exeRuntimes {
		rt := &stt.exeRuntimes[i]
		if rt.Processed {
			continue
		}
		if rt.PID == thisrt.PID {
			fmt.Fprintf(w, stt.indent + "%2.3f  %2.3f  %.5d  %.5d  %s\n", rt.Start-stt.exeRuntimes[0].Start, rt.TotalSec, rt.PPID, rt.PID, rt.Exe)
			rt.Processed = true
		}
	}
	
	for i, _ := range stt.exeRuntimes {
		rt := &stt.exeRuntimes[i]
		if rt.Processed {
			continue
		}
    	if rt.PPID == thisrt.PID {
	    	stt.PrintRt(w, rt)
		}
	}
	
	stt.indent = stt.indent[0:len(stt.indent)-1]
}

func (stt *ExecveTiming) Display(w io.Writer) {
	if len(stt.exeRuntimes) == 0 {
		return
	}
	fmt.Fprintf(w, "%d exec calls during snap run:\n", len(stt.exeRuntimes))
	fmt.Printf("\tSTART  ELAPSE PPID   PID    EXE\n")
	fmt.Printf("\t-------------------------------\n")

	sort.Slice(stt.exeRuntimes, func(i, j int) bool {
		return stt.exeRuntimes[i].Start < stt.exeRuntimes[j].Start
	  })

	for i, _ := range stt.exeRuntimes {
		rt := &stt.exeRuntimes[i]
		if rt.Processed {
			continue
		}
		stt.PrintRt(w, rt)
	}

	fmt.Fprintf(w, "Total time: %2.3fs\n", stt.TotalTime)
}

type exeStart struct {
	ppid  int
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

func (pt *pidTracker) Get(pid string) (ppid int, startTime float64, exe string) {
	if exeStart, ok := pt.pidToExeStart[pid]; ok {
		return exeStart.ppid, exeStart.start, exeStart.exe
	}
	return 0, 0, ""
}

func (pt *pidTracker) Add(pid string, ppid int, startTime float64, exe string) {
	pt.pidToExeStart[pid] = exeStart{ppid: ppid, start: startTime, exe: exe}
}

func (pt *pidTracker) Del(pid string) {
	delete(pt.pidToExeStart, pid)
}

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

	ipid, err := strconv.Atoi(pid)
	stats := procstat.Stat{Pid: ipid}
	err = stats.Update()
	if err != nil {
		return err
	}

	// deal with subsequent execve()
	if _, start, exe := pt.Get(pid); exe != "" {
		trace.addExeRuntime(ipid, stats.PPid, start, exe, execStart-start)
	}
	pt.Add(pid, stats.PPid, execStart, exe)
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
	
	ipid, err := strconv.Atoi(sigPid)

	if ppid, start, exe := pt.Get(sigPid); exe != "" {
		trace.addExeRuntime(ipid, ppid, start, exe, sigTime-start)
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

	var line string
	var start, end, tmp float64
	trace := NewExecveTiming(nSlowest)
	r := bufio.NewScanner(slog)
	for r.Scan() {
		line = r.Text()
		if start == 0.0 {
			if _, err := fmt.Sscanf(line, "%f %f ", &tmp, &start); err != nil {
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
	}
	if _, err := fmt.Sscanf(line, "%f %f", &tmp, &end); err != nil {
		return nil, fmt.Errorf("cannot parse end of exec profile: %s", err)
	}
	trace.TotalTime = end - start

	if r.Err() != nil {
		return nil, r.Err()
	}

	return trace, nil
}
