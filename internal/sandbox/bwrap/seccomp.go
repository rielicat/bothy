package bwrap

import (
	"bytes"
	"encoding/binary"
	"fmt"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
)

// denied syscalls (SPEC §2.12) return EPERM. Names missing from the build
// architecture's table (e.g. create_module on arm64) are skipped. The new
// mount API is listed with mount: it mounts just the same.
var denied = []string{
	"ptrace", "process_vm_readv", "process_vm_writev", "bpf", "perf_event_open", "userfaultfd",
	"io_uring_setup", "io_uring_enter", "io_uring_register", "keyctl", "add_key", "request_key",
	"mount", "umount", "umount2", "pivot_root", "chroot", "unshare", "setns",
	"fsopen", "fsconfig", "fsmount", "fspick", "move_mount", "open_tree", "mount_setattr",
	"kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module", "create_module", "query_module",
	"acct", "swapon", "swapoff", "reboot", "open_by_handle_at",
}

const (
	afUnix  = 1
	afInet  = 2
	afInet6 = 10
	// personality(0xffffffff) only queries the current persona.
	personalityQuery = 0xffffffff
)

// Policy is the jail's seccomp policy for the build architecture: allow by
// default, EPERM for the denied list, personality other than 0/query, and
// socket families other than AF_UNIX/AF_INET/AF_INET6. Assemble adds the
// architecture check and blocks the x32 ABI on amd64.
func Policy() (*seccomp.Policy, error) {
	info, err := arch.GetInfo("")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range denied {
		if _, ok := info.SyscallNames[n]; ok {
			names = append(names, n)
		}
	}
	return &seccomp.Policy{
		DefaultAction: seccomp.ActionAllow,
		Syscalls: []seccomp.SyscallGroup{
			{Action: seccomp.ActionErrno, Names: names},
			{Action: seccomp.ActionErrno, NamesWithCondtions: []seccomp.NameWithConditions{
				{Name: "personality", Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.NotEqual, Value: 0},
					{Argument: 0, Operation: seccomp.NotEqual, Value: personalityQuery},
				}},
				{Name: "socket", Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.NotEqual, Value: afUnix},
					{Argument: 0, Operation: seccomp.NotEqual, Value: afInet},
					{Argument: 0, Operation: seccomp.NotEqual, Value: afInet6},
				}},
			}},
		},
	}, nil
}

// Program returns the policy as the struct sock_filter array that
// `bwrap --seccomp <fd>` reads. Nothing is loaded into this process.
func Program() ([]byte, error) {
	p, err := Policy()
	if err != nil {
		return nil, err
	}
	insts, err := p.Assemble()
	if err != nil {
		return nil, fmt.Errorf("seccomp: %w", err)
	}
	raw, err := bpf.Assemble(insts)
	if err != nil {
		return nil, fmt.Errorf("seccomp: %w", err)
	}
	var b bytes.Buffer
	for _, ri := range raw {
		// struct sock_filter { __u16 code; __u8 jt; __u8 jf; __u32 k; }, native endian
		var rec [8]byte
		binary.NativeEndian.PutUint16(rec[0:2], ri.Op)
		rec[2], rec[3] = ri.Jt, ri.Jf
		binary.NativeEndian.PutUint32(rec[4:8], ri.K)
		b.Write(rec[:])
	}
	return b.Bytes(), nil
}
