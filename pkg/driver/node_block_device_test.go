package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceDeviceRescanFlushesOnlyTargetDevice(t *testing.T) {
	binDir := t.TempDir()
	commandLog := filepath.Join(t.TempDir(), "commands.log")

	writeCommand := func(name, body string) {
		t.Helper()
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("write fake %s command: %v", name, err)
		}
	}

	writeCommand("sync", `printf 'sync\n' >> "$COMMAND_LOG"`)
	writeCommand("blockdev", `printf 'blockdev %s\n' "$*" >> "$COMMAND_LOG"`)
	writeCommand("udevadm", `printf 'udevadm %s\n' "$*" >> "$COMMAND_LOG"`)

	t.Setenv("COMMAND_LOG", commandLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := forceDeviceRescan(context.Background(), "/dev/test-lun"); err != nil {
		t.Fatalf("forceDeviceRescan returned an error: %v", err)
	}

	output, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	commands := string(output)

	if strings.Contains(commands, "sync\n") {
		t.Fatal("forceDeviceRescan must not run host-wide sync")
	}
	const expected = "" +
		"blockdev --flushbufs /dev/test-lun\n" +
		"udevadm trigger --action=change /dev/test-lun\n" +
		"udevadm settle --timeout=5\n"
	if commands != expected {
		t.Fatalf("unexpected rescan command sequence:\n%s", commands)
	}
}
