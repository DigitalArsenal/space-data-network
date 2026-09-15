package keys

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// AllowEphemeralHostnameEnv lets an operator who understands the consequence
// seal under a hostname this package considers ephemeral.
const AllowEphemeralHostnameEnv = "SDN_ALLOW_EPHEMERAL_HOSTNAME"

// containerDefaultHostname matches the hostname Docker and Podman assign when
// --hostname is not given: the leading 12 hex digits of the container ID (the
// full 64-digit ID appears in some configurations).
//
// Deliberately narrow. A Kubernetes pod name is also ephemeral under a
// Deployment but stable under a StatefulSet, and an operator-chosen
// --hostname is stable by definition; refusing those would break correct
// deployments to catch a case that is not actually ambiguous. This shape is:
// nobody types it, and it is different every time the container is created.
var containerDefaultHostname = regexp.MustCompile(`^[0-9a-f]{12}$|^[0-9a-f]{64}$`)

// runningInContainer reports whether this process is inside a container.
// Several markers, because no single one covers every runtime: Docker writes
// /.dockerenv, Podman writes /run/.containerenv, Kubernetes injects a service
// host, and everything else shows up in PID 1's cgroup path.
func runningInContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		cgroup := string(data)
		for _, needle := range []string{"docker", "containerd", "kubepods", "lxc", "podman"} {
			if strings.Contains(cgroup, needle) {
				return true
			}
		}
	}
	return false
}

// EphemeralHostname reports whether the hostname feeding the machine-derived
// at-rest key will not survive this node's container being recreated.
//
// The at-rest key binds to (machine, user), and inside a container the
// "machine" half is the container's own auto-generated hostname. Recreating the
// container — a routine `docker rm && docker run`, an image upgrade, a compose
// `up --force-recreate` — draws a new one, and the node then cannot open the
// keys in its own volume. It fails closed, correctly, but by then the identity
// is already sealed under a name that no longer exists anywhere.
//
// That is why this is checked BEFORE sealing rather than reported after: at
// first boot the operator can still choose, and nothing has been lost.
func EphemeralHostname() (reason string, ephemeral bool) {
	if strings.TrimSpace(os.Getenv(AllowEphemeralHostnameEnv)) != "" {
		return "", false
	}
	if !runningInContainer() {
		return "", false
	}
	host := hostnameForFingerprint()
	if !containerDefaultHostname.MatchString(host) {
		return "", false
	}
	return fmt.Sprintf("container hostname %q is auto-generated and changes every time the container is created", host), true
}

// EphemeralHostnameSealError is the refusal returned instead of sealing a new
// identity under a hostname that will not come back.
func EphemeralHostnameSealError(reason string) error {
	return fmt.Errorf(`refusing to create a node identity that cannot survive this container: %s.

The at-rest key is derived from (machine, user), and here the machine half is
that hostname. Recreating the container draws a new one and this node can no
longer open the keys in its own volume — the identity, and the PeerID other
nodes know it by, would be lost.

Pick one, then start again:

  1. Give the container a stable name (simplest):
       docker run --hostname sdn-node-1 ...
       # compose: set the service's hostname: key to sdn-node-1
     Use the same value every time; it is not a secret and may be anything.

  2. Supply the at-rest password yourself, which unbinds the key from the
     machine entirely:
       docker run -e SDN_KEY_PASSWORD_FILE=/run/secrets/sdn-key ...

  3. Accept the risk deliberately (the node WILL lose its identity when the
     container is recreated; only sensible for a throwaway):
       docker run -e %s=1 ...`, reason, AllowEphemeralHostnameEnv)
}
