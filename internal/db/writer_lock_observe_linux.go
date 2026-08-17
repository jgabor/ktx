//go:build linux

package db

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func observeLockHolders(file *os.File, path string, requested coordinationMode) ([]LockHolder, bool) {
	var fileInfo os.FileInfo
	var err error
	if file != nil {
		fileInfo, err = file.Stat()
	} else if path != "" {
		fileInfo, err = os.Stat(path)
	} else {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile("/proc/locks")
	if err != nil {
		return nil, false
	}
	holders, available := parseProcLocks(data, stat.Dev, stat.Ino, requested, func(pid int) (bool, bool) {
		return inspectProcessFileDescriptors("/proc/"+strconv.Itoa(pid)+"/fd", fileInfo)
	})
	if !available {
		return nil, false
	}
	currentExecutable, err := os.Executable()
	if err != nil {
		return holders, true
	}
	currentInfo, err := os.Stat(currentExecutable)
	if err != nil {
		return holders, true
	}
	for i := range holders {
		holder := holders[i]
		holders[i].Command = inspectProcessCommand("/proc", holder.PID, currentInfo, func() bool {
			latest, err := os.ReadFile("/proc/locks")
			if err != nil {
				return false
			}
			observed, ok := parseProcLocks(latest, stat.Dev, stat.Ino, requested, func(pid int) (bool, bool) {
				return inspectProcessFileDescriptors("/proc/"+strconv.Itoa(pid)+"/fd", fileInfo)
			})
			if !ok {
				return false
			}
			for _, candidate := range observed {
				if candidate.PID == holder.PID && candidate.Mode == holder.Mode {
					return true
				}
			}
			return false
		})
	}
	return holders, true
}

func inspectProcessCommand(procRoot string, pid int, currentExecutable os.FileInfo, stillOwnsLock func() bool) string {
	directory := procRoot + "/" + strconv.Itoa(pid)
	started, err := processStartTime(directory + "/stat")
	if err != nil {
		return "unknown"
	}
	executable, err := os.Stat(directory + "/exe")
	if err != nil || !os.SameFile(executable, currentExecutable) {
		return "unknown"
	}
	argv, err := readProcessArgv(directory + "/cmdline")
	if err != nil {
		return "unknown"
	}
	if stillOwnsLock == nil || !stillOwnsLock() {
		return "unknown"
	}
	confirmed, err := processStartTime(directory + "/stat")
	if err != nil || confirmed != started {
		return "unknown"
	}
	return BlockerCommandLabel(argv)
}

func processStartTime(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	closing := bytes.LastIndexByte(data, ')')
	if closing < 0 {
		return "", os.ErrInvalid
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) < 20 {
		return "", os.ErrInvalid
	}
	return fields[19], nil
}

func readProcessArgv(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil || len(data) == 0 {
		return nil, os.ErrInvalid
	}
	parts := bytes.Split(data, []byte{0})
	if data[len(data)-1] != 0 {
		parts = parts[:len(parts)-1]
	}
	argv := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			argv = append(argv, string(part))
		}
	}
	if len(argv) == 0 {
		return nil, os.ErrInvalid
	}
	return argv, nil
}

func inspectProcessFileDescriptors(directory string, target os.FileInfo) (bool, bool) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, false
	}
	complete := true
	for _, entry := range entries {
		info, err := os.Stat(directory + "/" + entry.Name())
		if err != nil {
			complete = false
			continue
		}
		if os.SameFile(info, target) {
			return true, true
		}
	}
	return false, complete
}

func parseProcLocks(data []byte, device, inode uint64, requested coordinationMode, sameFile func(int) (bool, bool)) ([]LockHolder, bool) {
	var holders []LockHolder
	seen := make(map[LockHolder]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[1] == "->" || fields[1] != "FLOCK" {
			continue
		}
		if len(fields) != 8 {
			return nil, false
		}
		mode := ""
		switch fields[3] {
		case "READ":
			mode = "shared"
		case "WRITE":
			mode = "exclusive"
		default:
			return nil, false
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
			return nil, false
		}
		parts := strings.Split(fields[5], ":")
		if len(parts) != 3 {
			return nil, false
		}
		major, errMajor := strconv.ParseUint(parts[0], 16, 64)
		minor, errMinor := strconv.ParseUint(parts[1], 16, 64)
		lockInode, errInode := strconv.ParseUint(parts[2], 10, 64)
		if errMajor != nil || errMinor != nil || errInode != nil {
			return nil, false
		}
		if lockInode != inode {
			continue
		}
		if major != linuxDeviceMajor(device) || minor != linuxDeviceMinor(device) {
			if sameFile == nil {
				continue
			}
			match, observed := sameFile(pid)
			if !observed {
				return nil, false
			}
			if !match {
				continue
			}
		}
		if requested == coordinationShared && mode == "shared" {
			continue
		}
		holder := LockHolder{PID: pid, Mode: mode, Command: "unknown"}
		if _, ok := seen[holder]; !ok {
			seen[holder] = struct{}{}
			holders = append(holders, holder)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false
	}
	sort.Slice(holders, func(i, j int) bool {
		if holders[i].PID != holders[j].PID {
			return holders[i].PID < holders[j].PID
		}
		return holders[i].Mode < holders[j].Mode
	})
	if len(holders) > maxReportedLockHolders {
		holders = holders[:maxReportedLockHolders]
	}
	return holders, true
}

func linuxDeviceMajor(device uint64) uint64 {
	return (device>>8)&0xfff | (device >> 32 & ^uint64(0xfff))
}

func linuxDeviceMinor(device uint64) uint64 {
	return device&0xff | (device >> 12 & ^uint64(0xff))
}
