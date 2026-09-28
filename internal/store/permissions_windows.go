package store

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

var reOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

func restrictFile(f *os.File) (err error) {
	defer func() {
		if err != nil {
			err = &os.PathError{Op: "restrict permissions", Path: f.Name(), Err: err}
		}
	}()
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		return fmt.Errorf("create security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("get private DACL: %w", err)
	}

	// Go opens writable files with GENERIC_WRITE, which does not include
	// WRITE_DAC. SetSecurityInfo's file path also reads existing security
	// information, so the reopened handle needs READ_CONTROL as well.
	// Reopen the existing object with both security rights instead of
	// resolving f.Name(): rooted files can have relative names, and a path
	// lookup could target a different object after a rename or substitution.
	if err = reOpenFile.Find(); err != nil {
		return fmt.Errorf("find ReOpenFile: %w", err)
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("access file handle: %w", err)
	}
	var handle windows.Handle
	var reopenErr error
	err = raw.Control(func(fd uintptr) {
		r, _, callErr := reOpenFile.Call(fd, uintptr(windows.READ_CONTROL|windows.WRITE_DAC),
			uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE), 0)
		handle = windows.Handle(r)
		if handle == windows.InvalidHandle {
			reopenErr = callErr
		}
	})
	if err != nil {
		return fmt.Errorf("access file handle: %w", err)
	}
	if reopenErr != nil {
		return fmt.Errorf("reopen file with READ_CONTROL|WRITE_DAC: %w", reopenErr)
	}
	defer func() {
		if closeErr := windows.CloseHandle(handle); err == nil && closeErr != nil {
			err = fmt.Errorf("close ACL handle: %w", closeErr)
		}
	}()
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("set private DACL: %w", err)
	}
	return nil
}
