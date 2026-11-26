package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

const (
	taskName   = "classrec"
	installDir = `C:\classrec`
)

// currentUser is DOMAIN\name of the user running classrec.
func currentUser() string { return os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME") }

// taskXML is the scheduled task that starts the agent at her logon, as her, only while she is
// logged on; runs a queued start as soon as the running agent ends (how a self-update restarts
// it); retries a start that fails; and never stops it for being on battery or running long.
func taskXML(exe, user string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s)) // writing to a strings.Builder cannot fail
		return b.String()
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>classrec records online classes with OBS</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%[2]s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>%[2]s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>Queue</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>%[1]s</Command><Arguments>agent --background</Arguments><WorkingDirectory>%[3]s</WorkingDirectory></Exec></Actions>
</Task>
`, esc(exe), esc(user), esc(filepath.Dir(exe)))
}

// registerTask creates or replaces the scheduled task for exe.
func registerTask(ctx context.Context, exe string) error {
	file := filepath.Join(os.TempDir(), "classrec-task.xml")
	if err := os.WriteFile(file, utf16LE(taskXML(exe, currentUser())), 0o644); err != nil {
		return err
	}
	defer os.Remove(file)
	return schtasks(ctx, "/Create", "/TN", taskName, "/XML", file, "/F")
}

// taskRunsAs returns the user the scheduled task runs as; an error if there is no task.
func taskRunsAs(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "schtasks", "/Query", "/TN", taskName, "/XML").Output()
	if err != nil {
		return "", errors.New("no classrec scheduled task")
	}
	var task struct {
		UserID string `xml:"Principals>Principal>UserId"`
	}
	if err := xml.Unmarshal(utf8XML(out), &task); err != nil {
		return "", err
	}
	return task.UserID, nil
}

func schtasks(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "schtasks", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// utf16LE encodes s as UTF-16 little-endian with a byte-order mark, as schtasks wants.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := []byte{0xFF, 0xFE}
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// utf8XML makes schtasks' XML parseable by encoding/xml, which reads only UTF-8.
func utf8XML(b []byte) []byte {
	return bytes.Replace(b, []byte(`encoding="UTF-16"`), []byte(`encoding="UTF-8"`), 1)
}

// runUninstall removes the scheduled task. Recordings and the OBS profile stay.
func runUninstall(ctx context.Context, _ []string) error {
	if err := schtasks(ctx, "/Delete", "/TN", taskName, "/F"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "scheduled task removed; recordings, logs and the classrec OBS profile are left as they are")
	return nil
}
