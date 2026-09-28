package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func readDACL(t *testing.T, name string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

func assertPrivateDACL(t *testing.T, name string) {
	t.Helper()
	sd := readDACL(t, name)
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL is not protected: %v, %v", control, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("missing or null DACL: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	want, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	// Compare canonical ACEs, allowing Windows to retain automatic-inheritance
	// control flags. No additional or inherited grants may remain.
	gotText, wantText := sd.String(), want.String()
	i, j := strings.IndexByte(gotText, '('), strings.IndexByte(wantText, '(')
	if i < 0 || j < 0 || gotText[i:] != wantText[j:] {
		t.Fatalf("unexpected access grants: %q; want %q", gotText, wantText)
	}
}

func TestRestrictWritableFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "entry")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// This is the ordinary write-only handle that previously failed with
	// ERROR_ACCESS_DENIED when passed directly to SetSecurityInfo.
	if err := restrictFile(f); err != nil {
		t.Fatal(err)
	}
	assertPrivateDACL(t, name)
	if _, err := f.WriteString("synthetic"); err != nil {
		t.Fatal("original write handle was damaged:", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// The file must remain removable after the original handle is closed.
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictFileUsesHandleNotName(t *testing.T) {
	dir := t.TempDir()
	actual, decoy := filepath.Join(dir, "actual"), filepath.Join(dir, "decoy")
	if err := os.WriteFile(decoy, nil, 0600); err != nil {
		t.Fatal(err)
	}
	before := readDACL(t, decoy).String()
	f, err := os.OpenFile(actual, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(f.Fd()), windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	alias := os.NewFile(uintptr(duplicate), decoy)
	defer alias.Close()
	if err := restrictFile(alias); err != nil {
		t.Fatal(err)
	}
	assertPrivateDACL(t, actual)
	if after := readDACL(t, decoy).String(); after != before {
		t.Fatal("modified permissions on the path instead of the open file")
	}
}

func TestPrivateACLsAfterInitializeAndReplace(t *testing.T) {
	s := fixture(t)
	assertPrivateDACL(t, s.Config.Identities)
	assertPrivateDACL(t, filepath.Join(s.Config.Dir, ".age-recipients"))
	for _, value := range []string{"first", "replacement"} {
		if err := s.Write("nested/entry", []byte(value)); err != nil {
			t.Fatal(err)
		}
		assertPrivateDACL(t, filepath.Join(s.Config.Dir, "nested", "entry.age"))
		got, err := s.Read("nested/entry")
		if err != nil || string(got) != value {
			t.Fatalf("round trip: %q, %v", got, err)
		}
	}
}

func TestRestrictClosedFileError(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err = restrictFile(f)
	var pathErr *os.PathError
	if !errors.Is(err, os.ErrClosed) || !errors.As(err, &pathErr) || pathErr.Path != f.Name() || pathErr.Op != "restrict permissions" {
		t.Fatalf("missing wrapped error context: %v", err)
	}
}
