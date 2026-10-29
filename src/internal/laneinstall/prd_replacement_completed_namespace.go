package laneinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
)

const (
	completedBootstrapFileLimit   = int64(1 << 20)
	completedBootstrapDatabaseMax = int64(4 << 30)
	completedBootstrapSidecarMax  = int64(64 << 20)
)

type completedBootstrapPathEvidence struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Mode   uint32 `json:"mode"`
	Nlink  uint64 `json:"nlink"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// validatePRDReplacementBootstrapCompletedNamespace checks the durable,
// already-created namespace. It is deliberately separate from
// validatePRDReplacementBootstrapDirectories, which accepts only the empty
// pre-install state. The returned digest binds the correction intent to the
// complete bounded file and directory inventory, including authorized_keys.
func validatePRDReplacementBootstrapCompletedNamespace(plan PRDReplacementBootstrapPlan,
	journal PRDReplacementBootstrapJournal) (string, error) {
	ns := plan.Namespace
	root := ns.ServiceRoot
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	if plan.OperationID != prdBootstrapRootModeOperationID || journal.OperationID != plan.OperationID ||
		journal.Absence != plan.Absence || !equalPRDStrings(journal.CompletedSteps, prdReplacementBootstrapSteps) ||
		journal.DatabaseSchema <= 0 || !bundleDigestPattern.MatchString(journal.WorkInventorySHA256) ||
		!bundleDigestPattern.MatchString(journal.ConfigurationSHA256) ||
		!bundleDigestPattern.MatchString(journal.DatabaseSHA256) ||
		journal.DatabaseStagingPath != filepath.Join(filepath.Dir(ns.Database), "catalog-prd-bootstrap-"+plan.OperationID+".db") ||
		validateCatalogNamespaceAbsenceReportForNamespace(plan.Absence, "linux-host", config.ReleaseLanePRD,
			config.ReleaseLanePRD, ns) != nil || !completedBootstrapNamespacePathsCanonical(ns) {
		return "", fmt.Errorf("completed replacement namespace journal or path binding: %w", ErrCatalogOperationInvalid)
	}

	var rootStat unix.Stat_t
	canonicalRoot := filepath.Join("/home", config.LinuxAccount, ".local", "share", "remote-session-runner")
	parentValid := root != canonicalRoot || validateCatalogAbsenceParent(filepath.Dir(root), uid) == nil
	if !parentValid || unix.Lstat(root, &rootStat) != nil ||
		rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Uid != uid || rootStat.Gid != gid ||
		(rootStat.Mode&0o7777 != prdBootstrapRootModeBefore && rootStat.Mode&0o7777 != prdBootstrapRootModeAfter) {
		return "", fmt.Errorf("completed replacement namespace root identity or mode: %w", ErrCatalogOperationInvalid)
	}
	rootNames := []string{"backups", "bin", "config", "logs", "run", "secrets", "staging", "state", "tmp", "workspaces"}
	var toolchainDigest string
	if plan.Absence.ServiceRootState == "toolchain-only" {
		rootNames = append(rootNames, "toolchains")
		observed, err := inspectCatalogToolchainRoot(filepath.Join(root, "toolchains"), uid)
		if err != nil || observed.State != plan.Absence.ServiceRootState ||
			observed.ToolchainState != plan.Absence.ToolchainState || observed.ToolchainVersion != plan.Absence.ToolchainVersion ||
			observed.ToolchainBinarySHA256 != plan.Absence.ToolchainBinarySHA256 {
			return "", errors.Join(ErrCatalogOperationInvalid, err)
		}
		toolchainDigest = observed.ToolchainBinarySHA256
	} else if plan.Absence.ServiceRootState != "absent" {
		return "", fmt.Errorf("completed replacement namespace predecessor state: %w", ErrCatalogOperationInvalid)
	}
	if err := requireDirectoryEntries(root, rootNames); err != nil {
		return "", fmt.Errorf("completed replacement namespace root entries: %w", err)
	}

	evidence := []completedBootstrapPathEvidence{bootstrapDirectoryEvidence(".", rootStat)}
	// The current root mode is bound separately in the observation and the
	// correction record. Normalize only that authorized mode transition here
	// so retries after chmod retain the same artifact digest.
	evidence[0].Mode = prdBootstrapRootModeBefore
	directorySpecs := []struct {
		path     string
		children []string
	}{
		{filepath.Join(root, "config"), []string{"client-principals.yaml", "linux.yaml", "ssh-controller-map.yaml"}},
		{filepath.Join(root, "secrets"), []string{"client-ca.pem", "server.key", "server.pem"}},
		{filepath.Dir(ns.LifecycleLock), []string{filepath.Base(ns.LifecycleLock)}},
		{ns.Workspaces, nil},
		{filepath.Dir(ns.ScriptTempRoot), []string{filepath.Base(ns.ScriptTempRoot)}},
		{ns.ScriptTempRoot, nil},
		{ns.Backups, nil},
		{ns.Logs, nil},
		{ns.Binaries, nil},
		{ns.Staging, []string{"catalog-prd-bootstrap-database.json", catalogFreshInstallBaselineFile}},
	}
	for _, spec := range directorySpecs {
		if err := requireDirectoryEntries(spec.path, spec.children); err != nil {
			return "", fmt.Errorf("completed replacement namespace directory contents: %w", err)
		}
		var stat unix.Stat_t
		if unix.Lstat(spec.path, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
			stat.Uid != uid || stat.Gid != gid || stat.Mode&0o7777 != 0o700 {
			return "", fmt.Errorf("completed replacement namespace directory identity: %w", ErrCatalogOperationInvalid)
		}
		relative, err := filepath.Rel(root, spec.path)
		if err != nil || relative == ".." || filepath.IsAbs(relative) || stringsHasParent(relative) {
			return "", fmt.Errorf("completed replacement namespace directory path: %w", ErrCatalogOperationInvalid)
		}
		evidence = append(evidence, bootstrapDirectoryEvidence(filepath.ToSlash(relative), stat))
	}

	files := []struct {
		path        string
		mode        uint32
		maximum     int64
		allowEmpty  bool
		expectedSHA string
		capture     bool
	}{
		{ns.ConfigFile, 0o600, config.MaxConfigBytes, false, journal.ConfigurationSHA256, false},
		{filepath.Join(root, "config", "ssh-controller-map.yaml"), 0o600, 64 << 10, false, "", false},
		{filepath.Join(root, "config", "client-principals.yaml"), 0o600, 64 << 10, false, "", false},
		{filepath.Join(root, "secrets", "server.pem"), 0o600, completedBootstrapFileLimit, false, "", false},
		{filepath.Join(root, "secrets", "server.key"), 0o600, completedBootstrapFileLimit, false, "", false},
		{filepath.Join(root, "secrets", "client-ca.pem"), 0o600, completedBootstrapFileLimit, false, "", false},
		{ns.Database, 0o600, completedBootstrapDatabaseMax, false, journal.DatabaseSHA256, false},
		{ns.LifecycleLock, 0o600, 0, true, "", false},
		{filepath.Join(ns.Staging, "catalog-prd-bootstrap-database.json"), 0o600, 4096, false, "", true},
		{catalogFreshInstallBaselinePath(planToUpgradePlan(plan)), 0o600, completedBootstrapFileLimit, false, "", false},
	}
	for _, sidecar := range []string{ns.DatabaseWAL, ns.DatabaseSHM} {
		if _, err := os.Lstat(sidecar); err == nil {
			files = append(files, struct {
				path        string
				mode        uint32
				maximum     int64
				allowEmpty  bool
				expectedSHA string
				capture     bool
			}{sidecar, 0o600, completedBootstrapSidecarMax, true, "", false})
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("completed replacement namespace sidecar lookup: %w", ErrCatalogOperationInvalid)
		}
	}
	stateNames := []string{filepath.Base(ns.Database)}
	for _, sidecar := range []string{ns.DatabaseWAL, ns.DatabaseSHM} {
		if _, err := os.Lstat(sidecar); err == nil {
			stateNames = append(stateNames, filepath.Base(sidecar))
		}
	}
	if err := requireDirectoryEntries(filepath.Dir(ns.Database), stateNames); err != nil {
		return "", fmt.Errorf("completed replacement namespace state contents: %w", err)
	}
	var stateStat unix.Stat_t
	if unix.Lstat(filepath.Dir(ns.Database), &stateStat) != nil || stateStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stateStat.Uid != uid || stateStat.Gid != gid || stateStat.Mode&0o7777 != 0o700 {
		return "", fmt.Errorf("completed replacement namespace state directory identity: %w", ErrCatalogOperationInvalid)
	}
	evidence = append(evidence, bootstrapDirectoryEvidence("state", stateStat))

	var databaseMarker []byte
	for _, spec := range files {
		relative, err := filepath.Rel(root, spec.path)
		if err != nil || relative == ".." || filepath.IsAbs(relative) || stringsHasParent(relative) {
			return "", fmt.Errorf("completed replacement namespace artifact path: %w", ErrCatalogOperationInvalid)
		}
		item, content, err := inspectCompletedBootstrapFile(spec.path, filepath.ToSlash(relative), uid, gid,
			spec.mode, spec.maximum, spec.allowEmpty, spec.expectedSHA, spec.capture)
		if err != nil {
			return "", fmt.Errorf("completed replacement namespace file metadata or digest: %w", err)
		}
		evidence = append(evidence, item)
		switch spec.path {
		case filepath.Join(ns.Staging, "catalog-prd-bootstrap-database.json"):
			databaseMarker = content
		}
	}
	if err := validateCompletedBootstrapDatabaseMarker(databaseMarker, plan.OperationID, journal.InputSHA256); err != nil {
		return "", fmt.Errorf("completed replacement namespace database marker: %w", err)
	}
	if journal.AuthorizedKeysPath != ns.SSHAuthorityPath || !filepath.IsAbs(journal.AuthorizedKeysPath) ||
		filepath.Clean(journal.AuthorizedKeysPath) != journal.AuthorizedKeysPath ||
		!bundleDigestPattern.MatchString(journal.AuthorizedKeysBefore) || !bundleDigestPattern.MatchString(journal.AuthorizedKeysAfter) ||
		digestBytes(journal.AuthorizedKeysBeforeContents) != journal.AuthorizedKeysBefore || journal.AuthorizedKeysMode&0o022 != 0 {
		return "", fmt.Errorf("completed replacement namespace authorized-key journal: %w", ErrCatalogOperationInvalid)
	}
	if journal.AuthorizedKeysExisted && (journal.AuthorizedKeysMode == 0 || len(journal.AuthorizedKeysBeforeContents) == 0) ||
		!journal.AuthorizedKeysExisted && (journal.AuthorizedKeysMode != 0o600 || len(journal.AuthorizedKeysBeforeContents) != 0 ||
			journal.AuthorizedKeysBefore != digestBytes(nil)) {
		return "", fmt.Errorf("completed replacement namespace authorized-key baseline: %w", ErrCatalogOperationInvalid)
	}
	if err := validateAuthorizedKeysParent(journal.AuthorizedKeysPath); err != nil {
		return "", fmt.Errorf("completed replacement namespace authorized-key parent: %w", err)
	}
	keyMode := journal.AuthorizedKeysMode
	if !journal.AuthorizedKeysExisted {
		keyMode = 0o600
	}
	keyEvidence, _, err := inspectCompletedBootstrapFile(journal.AuthorizedKeysPath, "external/authorized_keys", uid, gid,
		keyMode, 1<<20, false, journal.AuthorizedKeysAfter, false)
	if err != nil {
		return "", fmt.Errorf("completed replacement namespace authorized key: %w", err)
	}
	evidence = append(evidence, keyEvidence)

	sort.Slice(evidence, func(i, j int) bool { return evidence[i].Path < evidence[j].Path })
	encoded, err := json.Marshal(struct {
		Version         int                              `json:"version"`
		ToolchainSHA256 string                           `json:"toolchain_sha256,omitempty"`
		Entries         []completedBootstrapPathEvidence `json:"entries"`
	}{1, toolchainDigest, evidence})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func completedBootstrapNamespacePathsCanonical(ns config.LaneNamespace) bool {
	root := ns.ServiceRoot
	paths := []string{ns.ConfigFile, ns.Database, ns.DatabaseWAL, ns.DatabaseSHM, ns.Workspaces,
		ns.ScriptTempRoot, ns.Backups, ns.Logs, ns.Binaries, ns.Staging, ns.LifecycleLock}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !pathContained(root, path) {
			return false
		}
	}
	return filepath.Dir(ns.ConfigFile) == filepath.Join(root, "config") &&
		filepath.Dir(ns.Database) == filepath.Join(root, "state") &&
		filepath.Dir(ns.LifecycleLock) == filepath.Join(root, "run") &&
		ns.Workspaces == filepath.Join(root, "workspaces") &&
		ns.ScriptTempRoot == filepath.Join(root, "tmp", "scripts") &&
		ns.Backups == filepath.Join(root, "backups") && ns.Logs == filepath.Join(root, "logs") &&
		ns.Binaries == filepath.Join(root, "bin") && ns.Staging == filepath.Join(root, "staging") &&
		ns.DatabaseWAL == ns.Database+"-wal" && ns.DatabaseSHM == ns.Database+"-shm"
}

func requireDirectoryEntries(path string, expected []string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return errors.Join(ErrCatalogOperationInvalid, err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if len(got) != len(want) {
		return ErrCatalogOperationInvalid
	}
	for index := range got {
		if got[index] != want[index] {
			return ErrCatalogOperationInvalid
		}
	}
	return nil
}

func bootstrapDirectoryEvidence(path string, stat unix.Stat_t) completedBootstrapPathEvidence {
	return completedBootstrapPathEvidence{Path: path, Kind: "directory", Device: uint64(stat.Dev), Inode: stat.Ino,
		UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode & 0o7777), Nlink: uint64(stat.Nlink), Size: int64(stat.Size)}
}

func inspectCompletedBootstrapFile(path, evidencePath string, uid, gid, mode uint32, maximum int64,
	allowEmpty bool, expectedSHA string, capture bool) (completedBootstrapPathEvidence, []byte, error) {
	var empty completedBootstrapPathEvidence
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || maximum < 0 {
		return empty, nil, ErrCatalogOperationInvalid
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return empty, nil, errors.Join(ErrCatalogOperationInvalid, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var before, named unix.Stat_t
	beforeInfo, infoErr := file.Stat()
	if infoErr != nil || unix.Fstat(fd, &before) != nil || unix.Lstat(path, &named) != nil ||
		before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uid || before.Gid != gid ||
		uint32(before.Mode&0o7777) != mode || before.Nlink != 1 || before.Size < 0 || before.Size > maximum ||
		(!allowEmpty && before.Size == 0) || before.Dev != named.Dev || before.Ino != named.Ino ||
		beforeInfo.Size() != before.Size || !beforeInfo.Mode().IsRegular() ||
		named.Mode&unix.S_IFMT != unix.S_IFREG {
		return empty, nil, ErrCatalogOperationInvalid
	}
	hash := sha256.New()
	var captured bytes.Buffer
	writer := io.Writer(hash)
	if capture {
		if before.Size > completedBootstrapFileLimit {
			return empty, nil, ErrCatalogOperationInvalid
		}
		writer = io.MultiWriter(hash, &captured)
	}
	read, err := io.CopyN(writer, file, before.Size)
	if err != nil || read != before.Size {
		return empty, nil, errors.Join(ErrCatalogOperationInvalid, err)
	}
	var after unix.Stat_t
	var namedAfter unix.Stat_t
	afterInfo, afterInfoErr := file.Stat()
	if afterInfoErr != nil || unix.Fstat(fd, &after) != nil || unix.Lstat(path, &namedAfter) != nil ||
		before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Uid != after.Uid ||
		before.Gid != after.Gid || before.Mode != after.Mode || before.Nlink != after.Nlink ||
		!beforeInfo.ModTime().Equal(afterInfo.ModTime()) ||
		namedAfter.Dev != before.Dev || namedAfter.Ino != before.Ino || namedAfter.Uid != before.Uid || namedAfter.Gid != before.Gid ||
		namedAfter.Mode != before.Mode || namedAfter.Nlink != before.Nlink || namedAfter.Mode&unix.S_IFMT != unix.S_IFREG {
		return empty, nil, ErrCatalogOperationInvalid
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA != "" && digest != expectedSHA {
		return empty, nil, ErrCatalogOperationInvalid
	}
	rel := filepath.ToSlash(evidencePath)
	if rel == "" || filepath.IsAbs(rel) || filepath.Clean(filepath.FromSlash(rel)) != filepath.FromSlash(rel) || stringsHasParent(rel) {
		return empty, nil, ErrCatalogOperationInvalid
	}
	return completedBootstrapPathEvidence{Path: rel, Kind: "file", Device: uint64(before.Dev), Inode: before.Ino,
		UID: before.Uid, GID: before.Gid, Mode: uint32(before.Mode & 0o7777), Nlink: uint64(before.Nlink), Size: before.Size,
		SHA256: digest}, captured.Bytes(), nil
}

func validateCompletedBootstrapDatabaseMarker(data []byte, operationID, inputSHA string) error {
	var marker struct {
		OperationID string `json:"operation_id"`
		InputSHA256 string `json:"input_sha256"`
	}
	if len(data) == 0 || len(data) > 4096 || !validCatalogOperationID(operationID) || !bundleDigestPattern.MatchString(inputSHA) {
		return ErrCatalogOperationInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		marker.OperationID != operationID || marker.InputSHA256 != inputSHA {
		return ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(marker, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func stringsHasParent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func validatePRDReplacementBootstrapConfiguration(ns config.LaneNamespace, journal PRDReplacementBootstrapJournal) error {
	loaded, err := config.LoadFile(ns.ConfigFile)
	if err != nil || loaded.SchemaVersion() != config.VersionV2 || loaded.Kind() != config.HostKindLinux ||
		loaded.ReleaseLane() != config.ReleaseLanePRD || loaded.SourceSHA256() != journal.ConfigurationSHA256 {
		return errors.Join(ErrCatalogOperationInvalid, err)
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || settings.Account != config.LinuxAccount || settings.ServiceRoot != ns.ServiceRoot ||
		settings.RemoteTargetProfile != "linux-host" ||
		settings.Database != ns.Database || settings.PrivateSocket != ns.PrivateSocket || settings.Workspaces != ns.Workspaces ||
		settings.ScriptTempRoot != ns.ScriptTempRoot || settings.Backups != ns.Backups ||
		settings.DirectHTTPSBind != config.PRDReplacementLinuxPrivateIP+":8443" ||
		settings.DirectPublicEndpoint != "https://"+config.SimpleReplacementLinuxPublicIP+":8443" ||
		settings.ServerCertificate != filepath.Join(ns.ServiceRoot, "secrets", "server.pem") ||
		settings.ClientCA != filepath.Join(ns.ServiceRoot, "secrets", "client-ca.pem") ||
		settings.ClientPrincipalMap != filepath.Join(ns.ServiceRoot, "config", "client-principals.yaml") ||
		settings.TLSMinVersion != "1.3" || settings.RuntimeAdapter != "linux-host-process" {
		return ErrCatalogOperationInvalid
	}
	secret, ok := loaded.SecretReference(config.SecretLinuxServerTLSKey)
	if !ok || secret.File != filepath.Join(ns.ServiceRoot, "secrets", "server.key") {
		return ErrCatalogOperationInvalid
	}
	return nil
}
