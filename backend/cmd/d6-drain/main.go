// Command d6-drain is the target-bound operator control process. It runs the
// App-VM container controls and delegates DB-VM session/writer controls to a
// narrow host-managed helper. deployment.DrainWorkflow makes quiescence a real
// in-process admission seam for d6-migrate.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"power-iot-backend/internal/deployment"
)

type commandRunner func(context.Context, ...string) (string, error)

type composeControls struct {
	project           string
	appCompose        string
	appEnv            string
	appControlCommand string
	dbControlCommand  string
	appRoleIdentity   string
	run               commandRunner
	stderr            io.Writer
	directControlled  bool
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("d6-drain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "rehearsal or production")
	target := flags.String("target", "", "rehearsal or exact production target tcrfid01")
	project := flags.String("project", "", "isolated Compose project")
	appCompose := flags.String("app-compose", "", "App Compose file")
	appEnv := flags.String("app-env", "", "App Compose env file")
	appControlCommand := flags.String("app-vm-control-command", "", "validated App-VM Docker control helper")
	appRoleIdentity := flags.String("app-vm-role-identity-file", "", "host-managed App-VM role identity file")
	dbControlCommand := flags.String("db-control-command", "", "validated DB-VM control helper")
	identityFile := flags.String("target-identity-file", "", "host-managed target/role identity file")
	privateKeyFile := flags.String("admission-private-key", "", "host-managed Ed25519 admission signing key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	if *mode != "production" && *mode != "rehearsal" {
		_, _ = fmt.Fprintln(stderr, "-mode must be production or rehearsal")
		return 2
	}
	if *mode == "production" && *target != "tcrfid01" {
		_, _ = fmt.Fprintln(stderr, "production target must be tcrfid01")
		return 2
	}
	if *mode == "rehearsal" && *target != "rehearsal" {
		_, _ = fmt.Fprintln(stderr, "rehearsal target must be rehearsal")
		return 2
	}
	for name, value := range map[string]string{
		"project": *project, "app-compose": *appCompose,
		"app-env": *appEnv, "app-vm-control-command": *appControlCommand, "app-vm-role-identity-file": *appRoleIdentity, "db-control-command": *dbControlCommand,
		"target-identity-file": *identityFile, "admission-private-key": *privateKeyFile,
	} {
		if strings.TrimSpace(value) == "" {
			_, _ = fmt.Fprintf(stderr, "-%s is required\n", name)
			return 2
		}
	}
	if err := verifyTargetIdentity(*identityFile, *mode, *target); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if *mode == "production" {
		if err := requireRootControlledDirectory("/opt/poweriot"); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		if err := requireRootOwnedIdentity(*identityFile); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if *mode == "production" {
		if err := requireRootOwnedPrivateKey(*privateKeyFile); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if err := verifyAppVMRoleIdentity(*appRoleIdentity, *target); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if *mode == "production" {
		if err := requireRootOwnedIdentity(*appRoleIdentity); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	privateKey, err := loadAdmissionPrivateKey(*privateKeyFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	controls := &composeControls{
		project: *project, appCompose: *appCompose,
		appEnv: *appEnv, appControlCommand: *appControlCommand, dbControlCommand: *dbControlCommand,
		run: execCommand, stderr: stderr,
	}
	if result, err := controls.dbControl(context.Background(), "preflight"); err != nil || !strings.Contains(result, "DB_VM_ROLE_PREFLIGHT=PASS") {
		if err == nil {
			err = errors.New("DB_VM_CONTROL role preflight failed")
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	_, _ = fmt.Fprintln(stderr, "APP_VM_ROLE_PREFLIGHT=PASS")
	workflow := deployment.DrainWorkflow{
		HTTP:    deployment.NewWriteGate(false),
		Ingress: controls,
		MQTT:    controls,
		Restart: controls,
		Direct:  controls,
		Inspect: controls,
	}
	err = workflow.Execute(context.Background(), func(context.Context) error {
		canonical := fmt.Sprintf("D6-DRAIN-ADMISSION-V2 target=%s result=PASS\n", *target)
		signature := ed25519.Sign(privateKey, []byte(canonical))
		_, err := fmt.Fprintf(stdout, "%s sig=%s\n", strings.TrimSuffix(canonical, "\n"), base64.RawStdEncoding.EncodeToString(signature))
		return err
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func execCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (c *composeControls) appCommand(ctx context.Context, args ...string) (string, error) {
	return c.run(ctx, append([]string{c.appControlCommand}, args...)...)
}

func (c *composeControls) compose(ctx context.Context, args ...string) (string, error) {
	base := []string{c.appControlCommand, "compose", "-p", c.project, "--env-file", c.appEnv, "-f", c.appCompose}
	return c.run(ctx, append(base, args...)...)
}

func (c *composeControls) dbControl(ctx context.Context, action string) (string, error) {
	return c.run(ctx, c.dbControlCommand, action)
}

func (c *composeControls) BlockHTTPWrites(ctx context.Context) error {
	if _, err := c.compose(ctx, "stop", "reverse-proxy"); err != nil {
		return err
	}
	return c.stopAllServiceContainers(ctx, "reverse-proxy")
}

func (c *composeControls) StopIngestion(ctx context.Context) error {
	// Backend SIGTERM invokes MqttService.StopIngestion; stopping the broker
	// immediately after prevents queued/reconnect ingress during the fence.
	if _, err := c.compose(ctx, "stop", "-t", "30", "backend", "mqtt"); err != nil {
		return err
	}
	for _, service := range []string{"backend", "mqtt"} {
		if err := c.stopAllServiceContainers(ctx, service); err != nil {
			return err
		}
	}
	return nil
}

func (c *composeControls) stopAllServiceContainers(ctx context.Context, service string) error {
	ids, err := c.serviceIDs(ctx, service)
	if err != nil {
		return err
	}
	for _, id := range ids {
		running, inspectErr := c.appCommand(ctx, "container", "inspect", "-f", "{{.State.Running}}", id)
		if inspectErr != nil || strings.TrimSpace(running) != "true" {
			continue
		}
		if _, stopErr := c.appCommand(ctx, "stop", "-t", "30", id); stopErr != nil && c.containerExists(ctx, id) {
			return fmt.Errorf("stop %s container: %w", service, stopErr)
		}
	}
	return nil
}

func (c *composeControls) SuppressRestarts(ctx context.Context) error {
	for _, service := range []string{"reverse-proxy", "backend", "mqtt"} {
		containers, err := c.serviceIDs(ctx, service)
		if err != nil {
			return err
		}
		if len(containers) == 0 {
			return fmt.Errorf("%s restart suppression found no containers", service)
		}
		for _, container := range containers {
			if _, err := c.appCommand(ctx, "update", "--restart=no", container); err != nil && c.containerExists(ctx, container) {
				return err
			}
		}
	}
	return nil
}

func (c *composeControls) ControlDirectWriters(ctx context.Context) error {
	ids, err := c.serviceIDs(ctx, "backend")
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("no backend container was found for direct-writer control")
	}
	for _, id := range ids {
		state, stateErr := c.appCommand(ctx, "inspect", "-f", "{{.State.Running}}", id)
		if stateErr != nil {
			if !c.containerExists(ctx, id) {
				continue
			}
			return stateErr
		}
		if strings.TrimSpace(state) != "false" {
			return errors.New("backend must be stopped before DB-VM direct-writer control")
		}
	}
	result, err := c.dbControl(ctx, "disable-writers")
	if err != nil {
		return fmt.Errorf("DB_VM_CONTROL disable-writers failed: %w", err)
	}
	if !strings.Contains(result, "DIRECT_WRITER_CONTROL_SPLIT_VM=PASS") {
		return errors.New("DB_VM_CONTROL did not prove split-VM writer control")
	}
	_, _ = fmt.Fprintln(c.stderr, "DIRECT_WRITER_CONTROL_SPLIT_VM=PASS")
	if err := c.requireNoPublicApplicationDBExposure(ctx); err != nil {
		return err
	}
	count, err := c.applicationDBSessionCount(ctx)
	if err != nil {
		return err
	}
	if count != "0" {
		return fmt.Errorf("unknown application DB writers remain after DB-VM control: %s", count)
	}
	c.directControlled = true
	return nil
}

func (c *composeControls) Inspect(ctx context.Context) (deployment.DrainObservation, error) {
	for _, service := range []string{"reverse-proxy", "backend", "mqtt"} {
		containers, err := c.serviceIDs(ctx, service)
		if err != nil {
			return deployment.DrainObservation{}, err
		}
		if len(containers) == 0 {
			return deployment.DrainObservation{}, fmt.Errorf("%s stopped-container inspection found no containers", service)
		}
		for _, container := range containers {
			state, stateErr := c.appCommand(ctx, "inspect", "-f", "{{.State.Running}}", container)
			if stateErr != nil {
				return deployment.DrainObservation{}, stateErr
			}
			if strings.TrimSpace(state) != "false" {
				return deployment.DrainObservation{}, fmt.Errorf("%s container remains running", service)
			}
			restart, restartErr := c.appCommand(ctx, "inspect", "-f", "{{.HostConfig.RestartPolicy.Name}}", container)
			if restartErr != nil || strings.TrimSpace(restart) != "no" {
				return deployment.DrainObservation{}, fmt.Errorf("%s restart policy is not suppressed", service)
			}
		}
	}
	if !c.directControlled {
		return deployment.DrainObservation{}, errors.New("split-VM direct writer control was not applied")
	}
	if _, err := c.dbControl(ctx, "inspect"); err != nil {
		return deployment.DrainObservation{}, fmt.Errorf("DB_VM_CONTROL inspect failed: %w", err)
	}
	_, _ = fmt.Fprintln(c.stderr, "DB_VM_CONTROL=PASS")
	if err := c.requireNoPublicApplicationDBExposure(ctx); err != nil {
		return deployment.DrainObservation{}, err
	}
	count, err := c.applicationDBSessionCount(ctx)
	if err != nil {
		return deployment.DrainObservation{}, err
	}
	if count != "0" {
		return deployment.DrainObservation{}, errors.New("database inspection found a remaining writer")
	}
	return deployment.DrainObservation{
		HTTPWritesBlocked: true, MQTTIngestionBlocked: true, RestartsSuppressed: true,
		DirectSQLControlled: true, InFlightWrites: 0, OldWriters: 0, UnknownWriters: 0,
		ProcessStateInspected: true, IngressStateInspected: true, BrokerStateInspected: true, DatabaseStateInspected: true,
	}, nil
}

func (c *composeControls) containerExists(ctx context.Context, id string) bool {
	_, err := c.appCommand(ctx, "container", "inspect", id)
	return err == nil
}

func (c *composeControls) serviceIDs(ctx context.Context, service string) ([]string, error) {
	for attempt := 0; attempt < 20; attempt++ {
		out, err := c.appCommand(ctx, "ps", "-a", "--no-trunc", "--filter", "label=com.docker.compose.project="+c.project, "--filter", "label=com.docker.compose.service="+service, "--format", "{{.ID}}")
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, 1)
		for _, candidate := range strings.Fields(out) {
			if _, inspectErr := c.appCommand(ctx, "container", "inspect", candidate); inspectErr == nil {
				ids = append(ids, candidate)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("%s stopped-container inspection found no containers", service)
}

func (c *composeControls) requireNoPublicApplicationDBExposure(ctx context.Context) error {
	result, err := c.dbControl(ctx, "no-public-db-port")
	if err != nil {
		return fmt.Errorf("DB_VM_CONTROL public exposure check failed: %w", err)
	}
	if !strings.Contains(result, "PUBLIC_DB_EXPOSURE=NO") {
		return errors.New("DB_VM_CONTROL did not prove public DB exposure is disabled")
	}
	_, _ = fmt.Fprintln(c.stderr, "PUBLIC_DB_EXPOSURE=NO")
	return nil
}

func verifyAppVMRoleIdentity(path, target string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("App VM role preflight failed: %w", err)
	}
	expected := "target=" + target + "\nrole=power-iot-app\n"
	if string(data) != expected {
		return errors.New("App VM role preflight failed: exact target/role identity mismatch")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&022 != 0 {
		return errors.New("App VM role preflight failed: identity must be a regular non-world-writable file")
	}
	return nil
}

func requireRootControlledDirectory(path string) error {
	for _, directory := range []string{"/", "/opt", path} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm()&022 != 0 {
			return fmt.Errorf("production security directory preflight failed: %s must be root-owned and not group/world writable", directory)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return fmt.Errorf("production security directory preflight failed: %s must be root-owned", directory)
		}
	}
	return nil
}

func requireRootOwnedPrivateKey(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&077 != 0 {
		return errors.New("admission private key ownership preflight failed: regular owner-only file required")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("admission private key ownership preflight failed: production key must be root-owned")
	}
	return nil
}

func requireRootOwnedIdentity(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("target identity ownership preflight failed: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("target identity ownership preflight failed: production identity must be root-owned")
	}
	return nil
}

func loadAdmissionPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("admission private key load failed: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("admission private key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("admission private key parse failed: %w", err)
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("admission private key is not Ed25519")
	}
	return privateKey, nil
}

func verifyTargetIdentity(path, mode, target string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("target identity preflight failed: %w", err)
	}
	expected := "target=" + target + "\nrole=power-iot-a3-" + mode + "-operator\n"
	if string(data) != expected {
		return errors.New("target identity preflight failed: exact host-managed target/role identity mismatch")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&022 != 0 {
		return errors.New("target identity preflight failed: identity must be a regular non-world-writable file")
	}
	return nil
}

func (c *composeControls) applicationDBSessionCount(ctx context.Context) (string, error) {
	out, err := c.dbControl(ctx, "sessions")
	return strings.TrimSpace(out), err
}
