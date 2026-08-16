//go:build linux

package db

import (
	"bufio"
	"bytes"
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
	return parseProcLocks(data, stat.Dev, stat.Ino, requested, func(pid int) (bool, bool) {
		return inspectProcessFileDescriptors("/proc/"+strconv.Itoa(pid)+"/fd", fileInfo)
	})
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
		holder := LockHolder{PID: pid, Mode: mode}
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
