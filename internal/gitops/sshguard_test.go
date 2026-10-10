// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/netguard"
)

// OUT-1: SSH remotes are subject to the outbound destination policy on the resolved address, like https remotes.
func TestSSHRemotesGoThroughTheDestinationPolicy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	addr := ln.Addr().String()

	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	keyFile := filepath.Join(dir, "id")
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(blk), 0o600))
	hostKey, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	known := filepath.Join(dir, "known_hosts")
	require.NoError(t, os.WriteFile(known, []byte(knownhosts.Line([]string{addr}, hostKey.PublicKey())+"\n"), 0o600))

	cfg := config.GitOpsConfig{
		Enabled: true, RepoURL: fmt.Sprintf("ssh://git@%s/repo.git", addr), Branch: "main", CloneDir: filepath.Join(dir, "clone"),
		Auth: config.GitAuthConfig{SSHKeyFile: keyFile, KnownHostsFile: known},
	}
	fetch := func(p *netguard.Policy) error {
		src, err := newSource(cfg, p)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = src.fetch(ctx)
		return err
	}

	// production: the loopback destination is refused before a connection is made
	denied, err := netguard.New(nil)
	require.NoError(t, err)
	err = fetch(denied)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbound policy")
	assert.Zero(t, accepted.Load(), "no connection may reach a forbidden destination")

	// an operator opens the range explicitly: the connection is attempted (and fails later at the SSH handshake)
	allowed, err := netguard.New([]string{"127.0.0.0/8"})
	require.NoError(t, err)
	require.Error(t, fetch(allowed))
	assert.NotZero(t, accepted.Load())

	// dev mode (permissive) does not restrict
	accepted.Store(0)
	require.Error(t, fetch(netguard.Permissive()))
	assert.NotZero(t, accepted.Load())
}
