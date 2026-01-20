//go:build windows

package osutils

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	WM_SYSCOMMAND   = 0x0112
	SC_MONITORPOWER = 0xF170
	HWND_BROADCAST  = 0xffff
	MONITOR_OFF     = 2
)

var (
	// Reuse user32 from wake_windows.go if available, otherwise redefine here locally if needed.
	// Since order of init is not guaranteed, safer to load what we need or use the one from wake_windows.go 
	// assuming it's package level. But wake_windows.go vars are unexported (lowercase user32).
	// So we should define our own to be safe and independent.
	modUser32        = syscall.NewLazyDLL("user32.dll")
	procPostMessageW = modUser32.NewProc("PostMessageW")
)

// TurnOffDisplay puts the monitor to sleep (low power mode)
func TurnOffDisplay() error {
	// PostMessage(HWND_BROADCAST, WM_SYSCOMMAND, SC_MONITORPOWER, MONITOR_OFF)
	// We use PostMessage to avoid blocking if a window is hung.
	ret, _, err := procPostMessageW.Call(
		uintptr(HWND_BROADCAST),
		uintptr(WM_SYSCOMMAND),
		uintptr(SC_MONITORPOWER),
		uintptr(MONITOR_OFF),
	)
	if ret == 0 {
		return fmt.Errorf("PostMessage failed: %v", err)
	}
	return nil
}

// IsAdmin checks if the current process has administrative privileges
func IsAdmin() bool {
	var token windows.Token
	h, _ := windows.GetCurrentProcess()
	err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	var sid *windows.SID
	err = windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	member, err := token.IsMember(sid)
	if err != nil {
		return false
	}

	return member
}

// EnsureFirewallRule checks if a firewall rule for the VKVM port exists,
// and if not, attempts to create it using PowerShell with admin elevation.
func EnsureFirewallRule(port int) error {
	ruleName := "VKVM Remote Switch"

	log.Printf("Firewall: Checking status for rule '%s' on port %d...", ruleName, port)

	// 1. Check if rule already exists AND matches the port
	// We use netsh to be safe, but check for the port string in the output
	checkCmd := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name="+ruleName)
	output, err := checkCmd.CombinedOutput()
	outputStr := string(output)

	if err == nil && strings.Contains(outputStr, ruleName) {
		// Rule exists, check if port matches
		portStr := fmt.Sprintf("%d", port)
		if strings.Contains(outputStr, portStr) && strings.Contains(outputStr, "Allow") {
			log.Printf("Firewall: Rule '%s' already exists and matches port %d. OK.", ruleName, port)
			return nil
		}
		log.Printf("Firewall: Rule '%s' exists but port/action mismatch. Updating...", ruleName)
	} else {
		log.Printf("Firewall: Rule '%s' not found. Creating...", ruleName)
	}

	// 2. Prepare PowerShell command to create the rule
	// IMPORTANT: We REMOVE the -Program restriction to be as broad as possible
	// This ensures that even if the path changes (e.g. from debug to standard), the port stays open.
	psCommand := fmt.Sprintf(
		"Remove-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue; New-NetFirewallRule -DisplayName '%s' -Direction Inbound -LocalPort %d -Protocol TCP -Action Allow -Profile Any",
		ruleName, ruleName, port,
	)

	// 3. Execute with RunAs verb to trigger UAC if not already admin
	if !IsAdmin() {
		log.Println("Firewall: Current process is NOT elevated. Requesting UAC elevation via ShellExecute...")

		verbPtr, _ := syscall.UTF16PtrFromString("runas")
		exePtr, _ := syscall.UTF16PtrFromString("powershell.exe")
		argPtr, _ := syscall.UTF16PtrFromString(fmt.Sprintf("-NoProfile -WindowStyle Hidden -Command \"%s\"", psCommand))

		var showCmd int32 = 0 // SW_HIDE

		err := windows.ShellExecute(0, verbPtr, exePtr, argPtr, nil, showCmd)
		if err != nil {
			return fmt.Errorf("failed to launch elevated powershell via ShellExecute: %w", err)
		}
		log.Println("Firewall: UAC prompt requested. Please check your screen/taskbar.")
	} else {
		log.Println("Firewall: Already running as admin. Applying simplified port-based rule directly.")
		cmd := exec.Command("powershell", "-NoProfile", "-Command", psCommand)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create firewall rule: %w (Output: %s)", err, string(output))
		}
		log.Printf("Firewall: Successfully applied simplified rule for port %d", port)
	}

	return nil
}
