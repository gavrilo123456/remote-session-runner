package laneinstall

import (
	"net"
	"os/user"
	"runtime"

	"remote-session-runner/src/internal/config"
)

const (
	prdReplacementLinuxPublicURL = "https://" + config.SimpleReplacementLinuxPublicIP + ":8443"
	prdReplacementLinuxBind      = config.PRDReplacementLinuxPrivateIP + ":8443"
)

// prdLinuxRouteMatchesPlan accepts the historical PRD route everywhere. The
// replacement route is valid only for the linux-host namespace and only on
// the specifically identified replacement worker. Paths and service identity
// remain derived from the canonical PRD plan.
func prdLinuxRouteMatchesPlan(plan PRDUpgradePlan, settings config.LinuxSettings, replacementHost bool) bool {
	expected, err := BuildPRDUpgradePlan(plan.HostID)
	if err != nil || expected.Namespace != plan.Namespace || plan.Namespace.Host != config.HostKindLinux ||
		settings.Account != plan.Namespace.Account || settings.ServiceRoot != plan.Namespace.ServiceRoot ||
		settings.Database != plan.Namespace.Database {
		return false
	}
	if settings.DirectHTTPSBind == plan.Namespace.DirectHTTPSBind && settings.DirectPublicEndpoint == plan.Namespace.DirectEndpointURL {
		return true
	}
	return plan.HostID == "linux-host" && replacementHost &&
		settings.DirectHTTPSBind == prdReplacementLinuxBind && settings.DirectPublicEndpoint == prdReplacementLinuxPublicURL
}

// configuredPRDHostIDForLinux matches one canonical unsuffixed namespace and
// permits the exact replacement address only when the local OS, account and
// private interface identify the approved replacement worker. The values are
// injectable so the guard can be exercised without host or service changes.
func configuredPRDHostIDForLinux(settings config.LinuxSettings, goos, goarch, account string, localIPv4 []string) (string, error) {
	replacementHost := replacementLinuxIdentityMatches("linux-host", goos, goarch, account, localIPv4)
	matched := ""
	for _, hostID := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		plan, err := BuildPRDUpgradePlan(hostID)
		if err != nil {
			continue
		}
		if !prdLinuxRouteMatchesPlan(plan, settings, replacementHost) {
			continue
		}
		if matched != "" {
			return "", ErrInvalidCatalogRelease
		}
		matched = hostID
	}
	if matched == "" || !CatalogHostMatchesPlatform(matched, goos, goarch) {
		return "", ErrInvalidCatalogRelease
	}
	return matched, nil
}

func replacementLinuxIdentityMatches(hostID, goos, goarch, account string, localIPv4 []string) bool {
	if hostID != "linux-host" || goos != "linux" || goarch != "amd64" || account != config.LinuxAccount {
		return false
	}
	for _, address := range localIPv4 {
		if parsed := net.ParseIP(address); parsed != nil && parsed.To4() != nil && parsed.String() == ReplacementLinuxPrivateIP {
			return true
		}
	}
	return false
}

func replacementLinuxLocalIPv4() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil {
				result = append(result, ip.String())
			}
		}
	}
	return result
}

func replacementLinuxLocalIdentityMatches() bool {
	current, err := user.Current()
	return err == nil && replacementLinuxIdentityMatches("linux-host", runtime.GOOS, runtime.GOARCH, current.Username, replacementLinuxLocalIPv4())
}
