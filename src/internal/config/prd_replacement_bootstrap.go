package config

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

const replacementLinuxBaseSystem = "Ubuntu 26.04"

// BuildFreshPRDReplacementConfigPair creates the two reviewed V2 route
// documents needed when linux-host has no PRD installation. The Mac keeps its
// mailbox roots, credentials, policies, and every non-Linux route; the Linux
// config is derived from an existing PRD worker template and points only at
// the verified replacement address. TLS certificate and SSH known-host
// material are verified and installed by the guarded host transition.
func BuildFreshPRDReplacementConfigPair(currentMac, linuxTemplate []byte) (macCandidate, linuxCandidate []byte, err error) {
	macCandidate, err = BuildFreshPRDReplacementMacRoute(currentMac)
	if err != nil {
		return nil, nil, err
	}
	linuxCandidate, err = BuildFreshPRDReplacementLinuxConfig(linuxTemplate)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateFreshPRDReplacementLinuxRoute(currentMac, macCandidate, linuxCandidate); err != nil {
		return nil, nil, err
	}
	return macCandidate, linuxCandidate, nil
}

// BuildFreshPRDReplacementLinuxConfig derives the replacement host's PRD V2
// configuration from one of the two existing Linux PRD worker configurations.
// It changes only the target identity, its fixed direct route, and the OS label
// for Linux-target environments. Policies, service paths, controller
// identities, secrets references, and all other fields remain byte-semantic
// equivalents of the reviewed source document.
func BuildFreshPRDReplacementLinuxConfig(template []byte) ([]byte, error) {
	if !isV2HostConfiguration(template, HostKindLinux) {
		return nil, fmt.Errorf("%w: expected an existing Linux V2 PRD template", ErrInvalidLaneProfiles)
	}
	before, err := decodeFileDocument(template)
	if err != nil || before.Linux == nil || len(before.EnvironmentRegistry) == 0 {
		return nil, fmt.Errorf("%w: invalid Linux PRD template", ErrInvalidLaneProfiles)
	}
	oldProfile := before.Linux.RemoteTargetProfile
	oldRoute, ok := lookupPRDReplacementTemplateRoute(oldProfile)
	if !ok || before.Linux.DirectHTTPSBind != oldRoute.bind || before.Linux.DirectPublicEndpoint != oldRoute.endpoint {
		return nil, fmt.Errorf("%w: Linux PRD template is not a verified existing worker route", ErrInvalidLaneProfiles)
	}
	for _, environment := range before.EnvironmentRegistry {
		if environment.EffectiveAccount != LinuxAccount || !sameTargets(environment.AllowedTargets,
			targetDocument{Kind: "remote", Profile: oldProfile}) ||
			len(environment.AllowedSourceModes) != 1 || environment.AllowedSourceModes[0] != "empty" ||
			!replacementLinuxControllersValid(environment.AllowedControllers) {
			return nil, fmt.Errorf("%w: Linux PRD template has a noncanonical target policy", ErrInvalidLaneProfiles)
		}
	}

	after := before
	linux := *before.Linux
	linux.RemoteTargetProfile = "linux-host"
	linux.DirectHTTPSBind = PRDReplacementLinuxPrivateIP + ":8443"
	linux.DirectPublicEndpoint = "https://" + SimpleReplacementLinuxPublicIP + ":8443"
	after.Linux = &linux
	after.EnvironmentRegistry = make(map[string]environmentDocument, len(before.EnvironmentRegistry))
	for name, environment := range before.EnvironmentRegistry {
		clone := environment
		clone.AllowedTargets = append([]targetDocument(nil), environment.AllowedTargets...)
		for index := range clone.AllowedTargets {
			clone.AllowedTargets[index].Profile = "linux-host"
		}
		clone.BaseSystem = replacementLinuxBaseSystem
		after.EnvironmentRegistry[name] = clone
	}

	var syntax yaml.Node
	if err := yaml.Unmarshal(template, &syntax); err != nil || syntax.Kind != yaml.DocumentNode || len(syntax.Content) != 1 {
		return nil, fmt.Errorf("%w: decode Linux PRD template syntax", ErrInvalidLaneProfiles)
	}
	root := syntax.Content[0]
	linuxNode, ok := yamlMappingValue(root, "linux")
	if !ok || !setYAMLString(linuxNode, "remote_target_profile", "linux-host") ||
		!setYAMLString(linuxNode, "direct_https_bind", PRDReplacementLinuxPrivateIP+":8443") ||
		!setYAMLString(linuxNode, "direct_public_endpoint", "https://"+SimpleReplacementLinuxPublicIP+":8443") {
		return nil, fmt.Errorf("%w: edit Linux PRD route nodes", ErrInvalidLaneProfiles)
	}
	registry, ok := yamlMappingValue(root, "environment_registry")
	if !ok || registry.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: missing Linux PRD environment registry", ErrInvalidLaneProfiles)
	}
	for index := 0; index+1 < len(registry.Content); index += 2 {
		environment := registry.Content[index+1]
		if environment.Kind != yaml.MappingNode || !setYAMLString(environment, "base_system", replacementLinuxBaseSystem) {
			return nil, fmt.Errorf("%w: edit Linux PRD environment identity", ErrInvalidLaneProfiles)
		}
		targets, ok := yamlMappingValue(environment, "allowed_targets")
		if !ok || targets.Kind != yaml.SequenceNode || len(targets.Content) != 1 ||
			!setYAMLString(targets.Content[0], "profile", "linux-host") {
			return nil, fmt.Errorf("%w: edit Linux PRD environment target", ErrInvalidLaneProfiles)
		}
	}
	candidate, err := yaml.Marshal(&syntax)
	if err != nil {
		return nil, fmt.Errorf("%w: encode replacement Linux PRD config", ErrInvalidLaneProfiles)
	}
	parsed, err := decodeFileDocument(candidate)
	if err != nil || !reflect.DeepEqual(after, parsed) {
		return nil, fmt.Errorf("%w: replacement Linux PRD config contains unexpected changes", ErrInvalidLaneProfiles)
	}
	loaded, err := parse(candidate)
	if err != nil || loaded.SchemaVersion() != VersionV2 || loaded.Kind() != HostKindLinux || loaded.ReleaseLane() != ReleaseLanePRD {
		return nil, fmt.Errorf("%w: generated replacement Linux config is invalid", ErrInvalidLaneProfiles)
	}
	settings, ok := loaded.LinuxSettings()
	parsedDocument, documentErr := decodeFileDocument(candidate)
	if !ok || documentErr != nil || parsedDocument.Linux == nil || parsedDocument.Linux.RemoteTargetProfile != "linux-host" ||
		settings.DirectHTTPSBind != PRDReplacementLinuxPrivateIP+":8443" ||
		settings.DirectPublicEndpoint != "https://"+SimpleReplacementLinuxPublicIP+":8443" {
		return nil, fmt.Errorf("%w: generated replacement Linux route is invalid", ErrInvalidLaneProfiles)
	}
	return candidate, nil
}

func replacementLinuxControllersValid(controllers []controllerDocument) bool {
	if len(controllers) != 2 {
		return false
	}
	want := map[string]bool{
		"queued_mac\x00" + MacAccount:  false,
		"direct_mtls\x00" + MacAccount: false,
	}
	for _, controller := range controllers {
		key := string(controller.Type) + "\x00" + controller.ID
		if _, ok := want[key]; !ok || want[key] {
			return false
		}
		want[key] = true
	}
	for _, seen := range want {
		if !seen {
			return false
		}
	}
	return true
}

func ValidateFreshPRDReplacementLinuxConfig(template, candidate []byte) error {
	want, err := BuildFreshPRDReplacementLinuxConfig(template)
	if err != nil || !reflect.DeepEqual(normalizeYAML(candidate), normalizeYAML(want)) {
		return fmt.Errorf("%w: fresh replacement Linux config differs from the reviewed derivation", ErrInvalidLaneProfiles)
	}
	return nil
}

type prdReplacementTemplateAddress struct {
	bind     string
	endpoint string
}

func lookupPRDReplacementTemplateRoute(profile string) (prdReplacementTemplateAddress, bool) {
	switch profile {
	case "sandbox-host":
		return prdReplacementTemplateAddress{bind: "10.0.0.14:8443", endpoint: "https://132.226.205.205:8443"}, true
	case "factory-sandbox":
		return prdReplacementTemplateAddress{bind: "10.0.0.30:8443", endpoint: "https://145.241.255.66:8443"}, true
	default:
		return prdReplacementTemplateAddress{}, false
	}
}

func setYAMLString(mapping *yaml.Node, key, value string) bool {
	field, ok := yamlMappingValue(mapping, key)
	if !ok || field.Kind != yaml.ScalarNode || field.Tag != "!!str" || strings.ContainsRune(value, '\x00') {
		return false
	}
	field.Value = value
	return true
}

func normalizeYAML(data []byte) string {
	var node yaml.Node
	if yaml.Unmarshal(data, &node) != nil {
		return ""
	}
	encoded, err := yaml.Marshal(&node)
	if err != nil {
		return ""
	}
	return string(encoded)
}
