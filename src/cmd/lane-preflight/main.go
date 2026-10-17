// Command lane-preflight builds a read-only Dev/QAS installation dry-run from
// explicit profile, capacity-policy, measurement, and live-observation files.
// It never opens credential paths or applies its rendered artifacts.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/lanecapacity"
	"remote-session-runner/src/internal/laneinstall"
)

const maxInputBytes = 4 << 20

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("lane-preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var peerPaths pathList
	host := flags.String("host", "", "canonical host ID")
	lane := flags.String("lane", "", "Dev or QAS lane")
	profilesPath := flags.String("profiles", "", "V3 profile YAML path")
	flags.Var(&peerPaths, "peer-profile", "paired V3 profile YAML path (repeatable)")
	policyPath := flags.String("policy", "", "lane-runtime capacity policy JSON path")
	measurementPath := flags.String("measurement", "", "capacity measurement JSON path")
	livePath := flags.String("live", "", "fresh live observation JSON path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *host == "" || *lane == "" || *profilesPath == "" || len(peerPaths) == 0 || *policyPath == "" || *measurementPath == "" || *livePath == "" {
		fmt.Fprintln(stderr, "usage: lane-preflight --host HOST --lane dev|qas --profiles FILE --peer-profile FILE [--peer-profile FILE ...] --policy FILE --measurement FILE --live FILE")
		return 2
	}
	profileData, err := readInput(*profilesPath)
	if err != nil {
		fmt.Fprintln(stderr, "preflight failed: cannot read bounded profile input")
		return 1
	}
	profiles, err := config.ParseV3ProfileBindings(profileData)
	if err != nil {
		fmt.Fprintln(stderr, "preflight failed: profile bindings are invalid")
		return 1
	}
	peers := make([]config.ValidatedV3Profiles, 0, len(peerPaths))
	for _, peerPath := range peerPaths {
		peerData, err := readInput(peerPath)
		if err != nil {
			fmt.Fprintln(stderr, "preflight failed: cannot read bounded peer profile input")
			return 1
		}
		peer, err := config.ParseV3ProfileBindings(peerData)
		if err != nil {
			fmt.Fprintln(stderr, "preflight failed: paired profile bindings are invalid")
			return 1
		}
		peers = append(peers, peer)
	}
	var policy lanecapacity.Policy
	var measurement lanecapacity.Measurement
	var live lanecapacity.LiveObservation
	if readJSON(*policyPath, &policy) != nil || readJSON(*measurementPath, &measurement) != nil || readJSON(*livePath, &live) != nil {
		fmt.Fprintln(stderr, "preflight failed: a bounded capacity input is invalid")
		return 1
	}
	plan, err := laneinstall.BuildLanePreflightPlan(laneinstall.PreflightInput{
		HostID: *host, Lane: config.ReleaseLane(*lane), Profiles: profiles, PeerProfiles: peers,
		Policy: policy, Measurement: measurement, Live: live, Now: time.Now().UTC(),
	})
	if err != nil {
		fmt.Fprintln(stderr, "preflight failed: selected lane inputs do not satisfy fixed policy")
		return 1
	}
	report, err := plan.Report()
	if err != nil {
		fmt.Fprintln(stderr, "preflight failed: validated plan integrity check failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "preflight failed: cannot encode safe report")
		return 1
	}
	return 0
}

func readInput(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil || len(data) > maxInputBytes {
		return nil, errors.New("input exceeds limit or cannot be read")
	}
	return data, nil
}

func readJSON(path string, target any) error {
	data, err := readInput(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON input")
	}
	return nil
}

type pathList []string

func (paths *pathList) String() string { return strings.Join(*paths, ",") }

func (paths *pathList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("peer profile path is empty")
	}
	*paths = append(*paths, value)
	return nil
}
