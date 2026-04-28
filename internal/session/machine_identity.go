package session

import (
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

var (
	machineIDOnce sync.Once
	machineIDVal  string
)

func machineIdentity() string {
	machineIDOnce.Do(func() {
		switch runtime.GOOS {
		case "darwin":
			machineIDVal = detectDarwinMachineID()
		case "linux":
			machineIDVal = detectLinuxMachineID()
		case "windows":
			machineIDVal = detectWindowsMachineID()
		}
		machineIDVal = strings.TrimSpace(machineIDVal)
	})
	return machineIDVal
}

func userIdentity() string {
	u, err := user.Current()
	if err != nil || u == nil {
		return ""
	}
	parts := []string{}
	if strings.TrimSpace(u.Uid) != "" {
		parts = append(parts, "uid="+u.Uid)
	}
	if strings.TrimSpace(u.Username) != "" {
		parts = append(parts, "username="+u.Username)
	}
	if strings.TrimSpace(u.HomeDir) != "" {
		parts = append(parts, "home="+u.HomeDir)
	}
	return strings.Join(parts, ",")
}

func detectDarwinMachineID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`IOPlatformUUID" = "([^"]+)"`)
	m := re.FindStringSubmatch(string(out))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func detectLinuxMachineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(path)
		if err == nil {
			v := strings.TrimSpace(string(b))
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func detectWindowsMachineID() string {
	// Read MachineGuid from registry.
	out, err := exec.Command("reg", "query", `HKLM\\SOFTWARE\\Microsoft\\Cryptography`, "/v", "MachineGuid").Output()
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`MachineGuid\s+REG_SZ\s+([^\r\n]+)`)
	m := re.FindStringSubmatch(string(out))
	if len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}
